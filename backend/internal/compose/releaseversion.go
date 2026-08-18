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
// it is the role that applies the migrations, so the schema the installation
// runs on is the schema ITS release brought. Recording its own release as the
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
// resolves itself — an operator has to re-pull the set.

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

// releaseObservationLock serializes the read-then-insert below. Several api
// replicas boot at once during a rollout, and without it each reads the same
// previous release and every one of them writes the change.
const releaseObservationLock = `
	SELECT pg_advisory_xact_lock(
		hashtext('margince:release-version:' || current_setting('app.workspace_id', true))::bigint)`

// lastObservedReleaseQuery reads the release the api recorded most recently.
//
// occurred_at leads the ordering, with id as the deterministic tiebreak, for the
// reason extensioninventory spells out: uuidv7 ids are monotonic only within one
// process, and concurrently booting replicas mint theirs independently. COALESCE
// because an absent key must read as the empty string — the same value "no
// record at all" produces, since both mean there is nothing to compare.
const lastObservedReleaseQuery = `
	SELECT COALESCE(detail->>'release_version', '')
	  FROM system_log WHERE action = $1
	 ORDER BY occurred_at DESC, id DESC LIMIT 1`

// RecordInstallationRelease records the release this api was built from as the
// installation's release, when it differs from the last one recorded.
//
// An unstamped binary records NOTHING and leaves the previous record standing.
// That is the same "unknown disables the comparison" rule buildinfo carries
// everywhere, and here it also protects the record: a locally built api run
// against a real installation must not erase the release a real one wrote.
//
// Pre-bootstrap there is no workspace to record against. The observation is
// skipped and the first boot after bootstrap records it — which is early enough,
// because the worker cannot get past its own dependency probe on a database the
// api has not migrated yet.
func RecordInstallationRelease(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger, version string) error {
	if !buildinfo.Comparable(version) {
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
	err = database.WithWorkspaceTx(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, releaseObservationLock); err != nil {
			return err
		}
		previous, err = lastObservedRelease(ctx, tx)
		if err != nil {
			return err
		}
		if previous == version {
			return nil
		}
		if _, err := storekit.LogSystem(ctx, tx, installationReleaseObserved, map[string]any{
			"release_version": version,
		}); err != nil {
			return err
		}
		recorded = true
		return nil
	})
	if err != nil {
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
		installation, err = lastObservedRelease(ctx, tx)
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
// The message names both versions and the one action that fixes it. It names no
// internals — an operator holding a crash-looping container gets everything they
// need from `kubectl logs`, and needs nothing about the ledger the answer came
// from.
func refuseMixedRelease(mine, installation string) error {
	if !buildinfo.SkewBetween(mine, installation) {
		return nil
	}
	return fmt.Errorf(
		"this role was built from release %q but this installation runs release %q: "+
			"a tag pull served images from two different releases, which the registry cannot refuse at the pull. "+
			"Re-pull every role image (api, web, worker) at one release and restart; "+
			"this role will not run half of one release beside half of another",
		mine, installation)
}

// lastObservedRelease reads the release the api recorded most recently; no
// record yet reads as the empty string, which every caller treats as "nothing to
// compare" rather than as a value.
func lastObservedRelease(ctx context.Context, tx pgx.Tx) (string, error) {
	var version string
	err := tx.QueryRow(ctx, lastObservedReleaseQuery, installationReleaseObserved).Scan(&version)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("compose: reading the last recorded release: %w", err)
	}
	return version, nil
}
