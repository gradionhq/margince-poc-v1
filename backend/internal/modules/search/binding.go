// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package search

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// ErrBindingNotSeeded marks a claim attempt against a marker row that does
// not exist yet — SeedBinding runs at boot, so this is a deployment that
// skipped it, not an ordinary runtime state; the CAS below reports it
// rather than silently doing nothing.
var ErrBindingNotSeeded = errors.New("search: embed store binding marker not seeded")

// ErrReembeddingInFlight refuses a claim while another run still holds the
// marker. The run id IS the claim — it is set and cleared with the status — so a
// second confirm is refused by the marker itself rather than by whether some job
// row happens to still be active.
var ErrReembeddingInFlight = errors.New("search: a fleet-wide reembed already holds the binding marker")

// ErrReembeddingSuperseded marks a job acting on a marker that no longer names
// its run, or that its run has already fanned out. Either way the work is not
// this row's to do, so the caller stops rather than retries.
var ErrReembeddingSuperseded = errors.New("search: the reembed run no longer holds the binding marker")

// ErrNoLiveWorkspace refuses a pass on an installation that has none. A
// bootstrap state rather than a fault: nothing has been created yet, so there is
// no corpus to rebuild and no handle to run the rebuild through.
var ErrNoLiveWorkspace = errors.New("search: the installation has no live workspace to bind a pass to")

// SeedBinding plants the marker row on first boot. An empty store is
// vacuously "populated under the current binding" — seeding
// populated_identity to the LIVE config (never a sentinel) is what keeps a
// fresh install's derived ReindexNeeded false (design §5.6-swap step 1: no
// first-boot wart). ON CONFLICT DO NOTHING makes concurrent boots and
// restarts idempotent — the marker is written once, ever, outside a
// completed reindex.
func (s *Store) SeedBinding(ctx context.Context, configuredIdentity string) error {
	// rls-exempt: deployment metadata, no workspace_id (embed_store_binding, migration 0114) — this write must not ride a per-workspace GUC tx.
	_, err := s.db.Pool().Exec(ctx, `
		INSERT INTO embed_store_binding (singleton, populated_identity, status)
		VALUES (true, $1, 'idle')
		ON CONFLICT (singleton) DO NOTHING`, configuredIdentity)
	if err != nil {
		return fmt.Errorf("search: seeding binding marker: %w", err)
	}
	return nil
}

// PopulatedIdentity is the one-PK read /readyz uses (Task 17): the identity the
// last run RELEASED under — never a count of what actually re-embedded, which
// releaseReembeddingTx spells out — the job lifecycle status,
// and when the run last made progress (updatedAt — a running pass refreshes it
// as it embeds, so it is the age of the last PROGRESS and not of the run. That
// is what lets a human tell a long reindex from a dead one, what the SPA shows
// as "last progress N ago", and what ReembedClaim.StealAfter measures).
// It never joins the live entity scan — that cost belongs to the ops
// status endpoint, not the readiness probe.
func (s *Store) PopulatedIdentity(ctx context.Context) (identity string, status string, updatedAt time.Time, err error) {
	// rls-exempt: deployment metadata, no workspace_id
	err = s.db.Pool().QueryRow(ctx, `SELECT populated_identity, status, updated_at FROM embed_store_binding WHERE singleton`).
		Scan(&identity, &status, &updatedAt)
	if err != nil {
		return "", "", time.Time{}, fmt.Errorf("search: reading binding marker: %w", err)
	}
	return identity, status, updatedAt, nil
}

// ReindexNeeded is the DERIVED "does the store need a re-embed" signal
// (design §5.6-swap v7) — there is no stored needs_reembed flag to
// demote, latch, or lie: this recomputes from the marker plus a live scan
// every time it is asked, so a mid-job restart, a config revert, and a
// late completion under a yet-different config are all honest by
// construction instead of depending on someone remembering to clear a bit.
func (s *Store) ReindexNeeded(ctx context.Context, configuredIdentity string) (bool, error) {
	populated, _, _, err := s.PopulatedIdentity(ctx)
	if err != nil {
		return false, err
	}
	if configuredIdentity != populated {
		return true, nil
	}
	pending, err := s.EntitiesPending(ctx, configuredIdentity)
	if err != nil {
		return false, err
	}
	return pending > 0, nil
}

// ReembedClaim is one attempt to take the marker for a new run.
type ReembedClaim struct {
	// Run identifies this run for the whole of its life. It is what every later
	// write fences on, because the identity cannot: a forced rebuild re-runs
	// deliberately under the SAME identity, so a straggler of a finished run
	// would otherwise still match the marker of the run that replaced it and
	// could release a run whose own children are still working.
	Run ids.UUID
	// TargetIdentity is the embed binding the run re-embeds under, stamped onto
	// populated_identity when the run releases.
	TargetIdentity string
	// StealAfter takes the marker from a run whose last movement is older than
	// this. The release is not airtight and cannot be: a workspace job declares
	// Timeout() == -1, which makes it exempt from River's rescuer
	// (job_rescuer.go returns ignore on a negative timeout at any age), so a
	// child whose process dies leaves a running row nothing will ever retry or
	// discard, and its workspace stays in the pending set forever. A marker held
	// for good, and no job anywhere to explain why. So a human keeps a way back.
	//
	// What makes the bound meaningful is that a working PASS keeps its marker
	// fresh: ReembedWorkspace refreshes it around every leg of its own work, so a
	// pass leaves the marker unmoved for no longer than one entity-table scan, or
	// ReembedProgressStaleness plus one embedding upsert, whichever is the longer.
	// Neither of those is a bound this code can enforce — ReembedProgressStaleness
	// says why.
	//
	// A RUN is more than its passes, and three of its legs move the marker not at
	// all: the wait between the claim and the dispatcher's fan-out, a child's
	// queue wait while no sibling of it is running, and the retry backoff between
	// a child's attempts, which River's attempt⁴ ladder stretches into minutes by
	// the last one. A window set here has to clear those too, and it clears all of
	// them the same way: by judgement about how long each plausibly takes, never
	// by proof that a healthy run cannot be dispossessed.
	//
	// What a steal stops, exactly: the dispossessed run's MARKER WRITES. Its
	// children carry a Run the marker no longer names, so their progress notes,
	// their FinishWorkspaceReembedding and their release all match no row and
	// cannot move — let alone release — the new run's set.
	//
	// What a steal does NOT stop: the children themselves. Nothing here cancels
	// a River job, and the new run's children carry a different Run in their
	// args, so ByArgs uniqueness does not suppress them either — both fleets run
	// at once. That is real model spend, not a no-op, and it is accepted rather
	// than overlooked: UpsertEmbedding's content-hash skip-compare means the
	// loser of the race per entity re-embeds nothing, so the overspend is
	// bounded by whatever was genuinely stale when the steal happened, and a
	// steal only fires against a run that has already stopped reporting progress.
	//
	// Zero never steals, which is what an ordinary confirm passes.
	StealAfter time.Duration
}

// ClaimAndEnqueueReembedding claims the marker for claim's run and runs the
// caller's enqueue in ONE raw-pool transaction (the store-owned-tx + callback
// shape, compose/deepreadtransport.go:97-107): if enqueue errors the whole
// transaction rolls back, so the claim can never outlive a job that was never
// actually queued.
//
// The claim is single-flight because it fires only on a marker no run holds (or
// one abandoned for longer than StealAfter). That is the whole of it: a run's
// dispatcher completes in milliseconds once it has fanned out, so "is some job
// still active" would stop answering the question long before the run is over.
func (s *Store) ClaimAndEnqueueReembedding(ctx context.Context, claim ReembedClaim, enqueue func(tx pgx.Tx) error) error {
	// rls-exempt: deployment metadata, no workspace_id — the CAS and the
	// job enqueue share one non-tenant transaction so a rolled-back enqueue
	// always undoes the claim; WithInfraTx is the platform's cross-tenant
	// tx shape (no GUC to bind, there is no tenant here).
	return database.WithInfraTx(ctx, s.db.Pool(), func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE embed_store_binding
			SET status = 'reembedding', reembedding_run = $1, reembedding_identity = $2,
			    updated_at = now()
			WHERE reembedding_run IS NULL
			   OR ($3 > 0 AND updated_at < now() - make_interval(secs => $3))`,
			claim.Run, claim.TargetIdentity, claim.StealAfter.Seconds())
		if err != nil {
			return fmt.Errorf("search: claiming reembedding: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return refusedClaimReason(ctx, tx)
		}
		return enqueue(tx)
	})
}

// refusedClaimReason names why a claim matched no row: the marker was never
// planted, or a run already holds it. The caller answers a different status
// code to each, so the two must not collapse into one error.
func refusedClaimReason(ctx context.Context, tx pgx.Tx) error {
	var seeded bool
	// rls-exempt: deployment metadata, no workspace_id
	err := tx.QueryRow(ctx, `SELECT true FROM embed_store_binding WHERE singleton`).Scan(&seeded)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrBindingNotSeeded
	}
	if err != nil {
		return fmt.Errorf("search: reading the refused claim's marker: %w", err)
	}
	return ErrReembeddingInFlight
}

// ReembedProgressStaleness paces how often a working run says so on its marker:
// ReembedWorkspace calls noteReembedProgress once this much time has passed, so
// a pass of any length costs at most one small write per interval, plus one on
// each side of every entity-table scan.
//
// It is therefore ALMOST the bound on how long a working PASS leaves its marker
// unmoved, and the shortfall is not a rounding error. A pass moves in two kinds
// of step, and it can only report BETWEEN them: one liveEntitiesOf scan, and one
// UpsertEmbedding. Both wait on pool acquisition and on row locks before doing
// any work of their own, and neither of those waits is bounded by anything this
// package controls — the model lane's per-call timeout caps only the model call
// inside an upsert. So the honest bound over a pass is the longer of one scan and
// this interval plus one upsert, where the second term is a wall time no code
// here can enforce. A steal window has to clear that, and the run-level legs no
// pass is running for at all (ReembedClaim.StealAfter lists them) — and no value
// of it turns any of that into a guarantee.
const ReembedProgressStaleness = 5 * time.Minute

// noteReembedProgress moves run's marker forward to say the run is still
// working. Fenced on the run, like every other write here: a straggler must not
// keep the marker of the run that replaced it looking alive.
func (s *Store) noteReembedProgress(ctx context.Context, run ids.UUID) error {
	// rls-exempt: deployment metadata, no workspace_id
	_, err := s.db.Pool().Exec(ctx, `
		UPDATE embed_store_binding SET updated_at = now() WHERE reembedding_run = $1`, run)
	if err != nil {
		return fmt.Errorf("search: recording reembed progress: %w", err)
	}
	return nil
}

// ReleaseReembedding hands the marker back from run and stamps the store
// populated under what that run targeted. It is fenced on the run, so a pass
// that outlived the run it belonged to cannot take the marker away from the run
// that replaced it.
func (s *Store) ReleaseReembedding(ctx context.Context, run ids.UUID) error {
	// rls-exempt: deployment metadata, no workspace_id
	return database.WithInfraTx(ctx, s.db.Pool(), func(tx pgx.Tx) error {
		return releaseReembeddingTx(ctx, tx, run)
	})
}

// releaseReembeddingTx is the one spelling of the release, fenced on the run so
// a marker held by a later run is left alone. populated_identity takes the RUN's
// target, never the live config — Postgres evaluates the assignment against the
// pre-update row — because a run finishing under a binding the operator has
// since changed must not stamp the marker as if the new config were populated.
//
// populated_identity therefore means "the identity the last run was RELEASED
// under", and not "the identity every workspace was re-embedded under". A run
// releases when its last workspace has no work left the run will come back to,
// and running out of attempts is one of those outcomes: a fleet whose children
// all failed still empties the set and still stamps here. The run counts no
// successes, deliberately — the cost of getting it wrong is a re-run, not a
// corrupt store — so every reader inherits the weaker claim, /readyz included
// (compose/embedreadyz.go says so where it reports it).
func releaseReembeddingTx(ctx context.Context, tx pgx.Tx, run ids.UUID) error {
	_, err := tx.Exec(ctx, `
		UPDATE embed_store_binding
		SET populated_identity = reembedding_identity, status = 'idle',
		    reembedding_run = NULL, reembedding_identity = NULL,
		    updated_at = now()
		WHERE reembedding_run = $1`, run)
	if err != nil {
		return fmt.Errorf("search: releasing the reembedding marker: %w", err)
	}
	return nil
}

// systemPrincipalID names the one system actor every index-maintenance
// pass (EmbedGen, pendingStats, ReembedWorkspace) runs as — named
// once so the three call sites share a single identity string instead of
// three copies of the same literal drifting apart.
const systemPrincipalID = "system"

// systemWorkspaceContext binds ctx to wsID under the system principal: an
// index or marker rebuilt through one caller's row scope would silently
// omit records that caller cannot see, so every index-maintenance
// pass (EmbedGen, pendingStats, ReembedWorkspace) reads and writes as the
// system actor instead.
func systemWorkspaceContext(ctx context.Context, wsID ids.UUID) context.Context {
	ctx = principal.WithWorkspaceID(ctx, wsID)
	return principal.WithActor(ctx, principal.Principal{Type: principal.PrincipalSystem, ID: systemPrincipalID})
}

// fleetWorkspaceIDs lists every live tenant workspace as the system
// principal — the enumeration pendingStats (this file) drives its
// per-workspace rollup loop from.
// anyWorkspace resolves a workspace to BIND a pass to, not to scope one.
//
// Since ADR-0091 §8 phase D no embeddable table carries a tenant, so a rebuild
// reads and writes the same rows whichever workspace the handle names — but the
// statements still run through a bound handle and WithWorkspaceTx still wants
// the GUC set. The oldest live workspace is taken for determinism rather than
// for meaning; §5 removes the need for one at all.
func (s *Store) anyWorkspace(ctx context.Context) (ids.WorkspaceID, error) {
	workspaces, err := s.fleetWorkspaceIDs(ctx)
	if err != nil {
		return ids.WorkspaceID{}, err
	}
	if len(workspaces) == 0 {
		return ids.WorkspaceID{}, ErrNoLiveWorkspace
	}
	return workspaces[0], nil
}

func (s *Store) fleetWorkspaceIDs(ctx context.Context) ([]ids.WorkspaceID, error) {
	// rls-exempt: fleet enumeration — the workspace table lists every tenant before the per-workspace tx each caller opens next (compose/dispatch.go enumerateWorkspaces is the sanctioned spelling; backend/jobfleetscan_test.go ratifies this site as a read).
	rows, err := s.db.Pool().Query(ctx, `SELECT id FROM workspace WHERE archived_at IS NULL ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("search: enumerating workspaces: %w", err)
	}
	workspaces, err := pgx.CollectRows(rows, pgx.RowTo[ids.WorkspaceID])
	if err != nil {
		return nil, fmt.Errorf("search: collecting workspaces: %w", err)
	}
	return workspaces, nil
}
