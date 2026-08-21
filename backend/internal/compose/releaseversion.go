// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The guard that keeps a torn release set from running.
//
// WHY THERE IS ANYTHING TO GUARD. A customer pulls each role image by tag, and
// the tag resolver answers each pull separately. Two pulls are two requests, so
// a publish landing between them hands back a set whose roles come from
// different releases — the classic case being `latest` moving mid-install. The
// OCI distribution protocol has no way to express "these three manifests, or
// none", so the registry cannot refuse it at the pull. The refusal has to happen
// where the set first exists as a whole, which is at run time.
//
// THE API IS THE AUTHORITY, and it is the authority because it already is one:
// the api image ships cmd/migrate and its entrypoint applies the schema before it
// serves, so the schema the installation runs on is the schema THIS role's release
// brought. (cmd/migrate is its own process role and the only caller of the
// migrator; what makes the api the authority is that the two ship and run
// together, not that this binary migrates.) Recording its own release as the
// installation's is therefore a statement of fact, not an election. Every other
// role compares itself against that record and refuses to start when it
// disagrees.
//
// THAT ASYMMETRY IS WHAT MAKES AN UPGRADE POSSIBLE. A symmetric rule — every
// role refuses while any peer disagrees — deadlocks the moment two roles restart
// independently, because each sees the other's old version and neither can be
// the first to move. Here the api moves first by definition: it records the new
// release, and the roles that follow match it. A rollback works for the same
// reason, which is why this compares for EQUALITY and never for order: the api
// simply states the release, so going back to an older one is an ordinary move
// rather than a special case somebody has to remember to allow.
//
// A TORN SET STOPS, AND STAYS STOPPED. If the api is release B and the worker is
// release A, the worker exits on every start and the orchestrator keeps
// restarting it. That is the intended outcome: a crash-looping role with the two
// versions in its log is visible, where a worker quietly running the wrong
// release is not. It does not resolve itself, because nothing about a torn pull
// resolves itself — an operator has to deploy the set again, at one release.
//
// TWO PROPERTIES ARE BOUNDED RATHER THAN ABSOLUTE, and a reader has to know which:
//
//   - The check happens AT BOOT, once. A worker already running when the api
//     records a new release is not re-checked, so an api-only restart leaves the
//     old worker serving until something else restarts it.
//   - The record is LAST WRITER WINS. During a rollout that has api replicas from
//     two releases alive at once, either may be the last to record, so the release
//     the installation reports can move backwards on its own.
//
// What holds regardless is the invariant this exists for: whichever release wins
// the record, the roles that disagree with it refuse, so no mixed set serves.
// Closing either bound is a design change rather than a fix — a monotonic record
// would cost rollback — so both are tracked rather than worked around here.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/buildinfo"
)

// installationReleaseObserved is the system_log action carrying the release the
// api recorded for this installation; one row per CHANGE, so the ledger reads as
// the installation's upgrade history rather than one row per restart.
const installationReleaseObserved = "release.version_observed"

// releaseLedgerFact names this fact in the advisory-lock key, so the release
// observation serializes against other release observations and against nothing
// else — the extension inventory takes its own key and the two never wait on each
// other.
const releaseLedgerFact = "release-version"

// lastObservedReleaseQuery reads the release the api recorded most recently.
//
// It carries no scope, because ADR-0091 §8 phase D took the tenant column off
// system_log and there is nothing left to scope by: one installation serves one
// organization (ADR-0061), and this ledger records that installation's releases.
//
// The residue this DOES admit, named because it is not hypothetical: an
// installation that merged an ARCHIVED predecessor still holds that
// predecessor's release rows — 0272 exempts the ledgers from the residue gate
// by name, precisely because their immutability trigger makes clearing them
// impossible — and this read can no longer tell them from its own. What keeps
// the guard honest is that RecordInstallationRelease writes the same unscoped
// ledger, so the api's own row is the newest as soon as it boots. The window is
// a worker booting against residue before that api boot, which is #2196.
//
// occurred_at leads the ordering, with id as the deterministic tiebreak, for the
// reason extensioninventory spells out: uuidv7 ids are monotonic only within one
// process, and concurrently booting replicas mint theirs independently. COALESCE
// because an absent key must read as the empty string — the same value "no
// record at all" produces, since both mean there is nothing to compare.
const lastObservedReleaseQuery = `
	SELECT COALESCE(detail->>'release_version', '')
	  FROM system_log
	 WHERE action = $1 AND detail->>'installation' = $2
	 ORDER BY occurred_at DESC, id DESC LIMIT 1`

// RecordInstallationRelease records the release this api was built from as the
// installation's release, when it differs from the last one recorded.
//
// An unstamped binary records NOTHING and leaves the previous record standing.
// That is the same "unknown disables the comparison" rule buildinfo carries
// everywhere, and here it also protects the record: a locally built api run
// against a real installation must not erase the release a real one wrote.
//
// Pre-bootstrap there is no installation to record against. The observation is
// skipped and the first boot after bootstrap records it — which is early enough,
// because the worker cannot get past its own dependency probe on a database the
// api has not migrated yet.
func RecordInstallationRelease(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger, version string) error {
	if !buildinfo.Comparable(version) {
		// Said out loud, symmetric with the assert path below. An unstamped api
		// disarms the guard for the WHOLE installation — every role that boots
		// after it has nothing to compare against — and an operator reading this
		// log must be able to tell that apart from a guard that is working.
		log.Info("release guard inactive: this api carries no release version")
		return nil
	}
	ctx, bootstrapped, err := bootLedgerScope(ctx, pool, "system:release-version")
	if err != nil {
		return fmt.Errorf("compose: resolving the installation to record its release: %w", err)
	}
	if !bootstrapped {
		log.Info("installation release not recorded: installation not bootstrapped yet", "release_version", version)
		return nil
	}

	var previous string
	recorded := false
	// The closure declares its own error at every step. Assigning the enclosing
	// err from in here would leave two variables of that name meaning different
	// things, one of which this call is about to overwrite.
	if err := database.WithWorkspaceTx(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, bootLedgerLock, releaseLedgerFact); err != nil {
			return err
		}
		last, err := lastObservedRelease(ctx, tx)
		if err != nil {
			return err
		}
		previous = last
		if last == version {
			return nil
		}
		if _, err := storekit.LogSystem(ctx, tx, installationReleaseObserved, map[string]any{
			"release_version": version,
			"installation":    installationMarker(ctx),
		}); err != nil {
			return err
		}
		recorded = true
		return nil
	}); err != nil {
		return fmt.Errorf("compose: recording the installation's release: %w", err)
	}
	// Logged only after the transaction COMMITTED, so the line never reports a
	// record the database rolled back.
	if recorded {
		// "from" is deliberately present even when empty: an operator reading
		// the first boot after this guard shipped needs to see that there was no
		// previous record, not wonder which value was omitted.
		log.Info("installation release recorded", "from", previous, "to", version)
	}
	return nil
}

// AssertInstallationRelease refuses to boot a role whose release is not the one
// the api recorded for this installation.
//
// Every role EXCEPT the api calls it. The api is the authority (see the file
// comment) and has nothing to check itself against; a role that checked its own
// record would only ever confirm it.
//
// Three outcomes are all "start": this binary is unstamped, the installation is
// not bootstrapped, or no api has recorded a release yet. None of them is a
// match, but none of them is a MISMATCH either, and refusing on the absence of a
// fact would take down installations whose only defect is that their api has not
// restarted since this guard shipped.
func AssertInstallationRelease(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger, version string) error {
	if !buildinfo.Comparable(version) {
		log.Info("release guard inactive: this binary carries no release version")
		return nil
	}
	ctx, bootstrapped, err := bootLedgerScope(ctx, pool, "system:release-version")
	if err != nil {
		return fmt.Errorf("compose: resolving the installation to check its release: %w", err)
	}
	if !bootstrapped {
		log.Info("release guard inactive: installation not bootstrapped yet", "release_version", version)
		return nil
	}

	var installation string
	if err := database.WithWorkspaceTx(ctx, pool, func(tx pgx.Tx) error {
		recorded, err := lastObservedRelease(ctx, tx)
		installation = recorded
		return err
	}); err != nil {
		return fmt.Errorf("compose: reading the installation's release: %w", err)
	}
	if err := refuseMixedRelease(version, installation); err != nil {
		return err
	}
	if installation == "" {
		log.Info("release guard inactive: no api has recorded this installation's release yet", "release_version", version)
		return nil
	}
	log.Info("release matches the installation", "release_version", version)
	return nil
}

// refuseMixedRelease answers the refusal a role owes when the installation runs
// a different release than the role was built from, or nil when there is nothing
// to refuse.
//
// The message gives both release versions and the one action that corrects it. It
// names no internals: an operator reading a role's log needs the two versions and
// what to do, and needs nothing about the ledger the answer came from.
//
// IT NAMES NO DEPLOYMENT MECHANISM EITHER. Container images and a registry are
// how the release reaches most installations, but they are not the only way — this
// software also runs from a plain host — and an error that tells an operator to
// re-pull an image is wrong for anyone who did not pull one. What is true of every
// installation is the fact and the correction: the roles disagree, and every role
// has to be at one release. How the release arrives is the deployment's business,
// and docs/deployment.md is where that belongs.
func refuseMixedRelease(mine, installation string) error {
	if !buildinfo.SkewBetween(mine, installation) {
		return nil
	}
	return fmt.Errorf(
		"this role is release %q but this installation runs release %q: "+
			"the deployment supplied two different releases. "+
			"Deploy every role (api, web, worker) at one release and restart; "+
			"this role will not run half of one release beside half of another",
		mine, installation)
}

// lastObservedRelease reads the release THIS installation's api recorded most
// recently; no such record reads as the empty string, which every caller treats
// as "nothing to compare" rather than as a value.
//
// Scoped to the installation marker rather than to the newest row of all: an
// installation that merged an archived predecessor still holds that
// predecessor's release rows, and the ledgers are exempt from the residue gate
// because their immutability trigger makes clearing them impossible.
func lastObservedRelease(ctx context.Context, tx pgx.Tx) (string, error) {
	var version string
	err := tx.QueryRow(ctx, lastObservedReleaseQuery,
		installationReleaseObserved, installationMarker(ctx)).Scan(&version)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		// Returned unwrapped: both callers already name what they were doing, and
		// wrapping here made the boot failure an operator reads say it twice.
		return "", err
	}
	return version, nil
}
