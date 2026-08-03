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

// pendingSource mirrors one embedText entry (embedgen.go:35-41) rewritten
// from a per-id lookup into a set-form expression: the same source
// columns, aliased to t, so the two never drift into indexing different
// text.
type pendingSource struct {
	table string
	text  string // expression over the aliased table t
}

// pendingSources is the set-form counterpart to embedgen.go's embedText —
// one entry per embeddable entity, in the exact source-column shape that
// module maintains per-row. Adding a searchable entity means adding a row
// to BOTH maps; they must never diverge, since the pending count and the
// live indexer must agree on what "this entity's text" means.
var pendingSources = map[string]pendingSource{
	entityPerson:       {table: entityPerson, text: "t.full_name"},
	entityOrganization: {table: entityOrganization, text: "concat_ws(' ', t.display_name, t.legal_name, t.industry)"},
	entityDeal:         {table: entityDeal, text: "t.name"},
	entityLead:         {table: entityLead, text: "concat_ws(' ', t.full_name, t.company_name, t.title)"},
	entityActivity:     {table: entityActivity, text: "concat_ws(' ', t.subject, t.body)"},
	entityProject:      {table: entityProject, text: "concat_ws(' ', t.name, t.key, t.description)"},
}

// SeedBinding plants the marker row on first boot. An empty store is
// vacuously "populated under the current binding" — seeding
// populated_identity to the LIVE config (never a sentinel) is what keeps a
// fresh install's derived ReindexNeeded false (design §5.6-swap step 1: no
// first-boot wart). ON CONFLICT DO NOTHING makes concurrent boots and
// restarts idempotent — the marker is written once, ever, outside a
// completed reindex.
func (s *Store) SeedBinding(ctx context.Context, configuredIdentity string) error {
	// rls-exempt: deployment metadata, no workspace_id (embed_store_binding, migration 0114) — this write must not ride a per-workspace GUC tx.
	_, err := s.pool.Exec(ctx, `
		INSERT INTO embed_store_binding (singleton, populated_identity, status)
		VALUES (true, $1, 'idle')
		ON CONFLICT (singleton) DO NOTHING`, configuredIdentity)
	if err != nil {
		return fmt.Errorf("search: seeding binding marker: %w", err)
	}
	return nil
}

// PopulatedIdentity is the one-PK read /readyz uses (Task 17): the marker's
// own view of what the store is populated under, the job lifecycle status,
// and when the run last made progress (updatedAt — a running pass refreshes it
// as it embeds, so it is the age of the last PROGRESS and not of the run. That
// is what lets a human tell a long reindex from a dead one, what the SPA shows
// as "last progress N ago", and what ReembedClaim.StealAfter measures).
// It never joins the live entity scan — that cost belongs to the ops
// status endpoint, not the readiness probe.
func (s *Store) PopulatedIdentity(ctx context.Context) (identity string, status string, updatedAt time.Time, err error) {
	// rls-exempt: deployment metadata, no workspace_id
	err = s.pool.QueryRow(ctx, `SELECT populated_identity, status, updated_at FROM embed_store_binding WHERE singleton`).
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
	// What makes the bound meaningful is that a WORKING run keeps its marker
	// fresh: ReembedWorkspace refreshes it as it goes, so it never reads staler
	// than ReembedProgressStaleness plus the one embed in flight when that
	// interval elapses. What makes stealing SAFE is the run id: the dispossessed
	// run's stragglers carry a Run the marker no longer names, so they act on
	// nothing.
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
	return database.WithInfraTx(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE embed_store_binding
			SET status = 'reembedding', reembedding_run = $1, reembedding_identity = $2,
			    reembedding_pending = '{}', updated_at = now()
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

// SeedReembeddingFleet records the workspaces run must still cover and runs the
// caller's enqueue in the SAME transaction, so a fan-out that failed to insert
// leaves no half-seeded set behind for the next attempt to double-count.
//
// It seeds ONCE: the marker must still name run AND still hold an empty set.
// Because the seed and the enqueue commit together, a non-empty set means this
// run's children are already queued, so a retried dispatcher re-seeding would
// put back workspaces whose children have since finished — children a ByArgs
// unique-skip may then decline to re-enqueue, leaving those workspaces in the
// set with nothing left to take them out. Either refusal is
// ErrReembeddingSuperseded, and the enqueue never happens.
func (s *Store) SeedReembeddingFleet(ctx context.Context, run ids.UUID, workspaces []ids.WorkspaceID, enqueue func(tx pgx.Tx) error) error {
	// rls-exempt: deployment metadata, no workspace_id
	return database.WithInfraTx(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE embed_store_binding SET reembedding_pending = $2, updated_at = now()
			WHERE reembedding_run = $1 AND cardinality(reembedding_pending) = 0`, run, workspaces)
		if err != nil {
			return fmt.Errorf("search: seeding the reembedding fleet: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return ErrReembeddingSuperseded
		}
		return enqueue(tx)
	})
}

// FinishWorkspaceReembedding takes workspace out of run's pending set, and
// releases the marker when it was the last one outstanding. Every terminal
// outcome a workspace can reach — re-embedded, cancelled on drift, or out of
// attempts — is a workspace this run will not come back to, so all of them
// arrive here.
//
// Removal is idempotent (array_remove of an absent element changes nothing), so
// a retried job is harmless; and it is fenced on the RUN, so a workspace
// belonging to a run that has already released matches no row and leaves the
// current run's set alone — which the target identity could not do, since a
// forced rebuild re-runs under the same one.
//
// That fence is also what makes ReembedClaim.StealAfter safe: a dispossessed
// run's children go on working and go on reporting here, and it is only because
// they name a run the marker no longer holds that they cannot empty — or
// release — the set of the run that took over from them. Relaxing this fence
// breaks the steal, not just the straggler.
func (s *Store) FinishWorkspaceReembedding(ctx context.Context, run ids.UUID, workspace ids.WorkspaceID) error {
	// rls-exempt: deployment metadata, no workspace_id
	return database.WithInfraTx(ctx, s.pool, func(tx pgx.Tx) error {
		var remaining int
		err := tx.QueryRow(ctx, `
			UPDATE embed_store_binding
			SET reembedding_pending = array_remove(reembedding_pending, $2), updated_at = now()
			WHERE reembedding_run = $1
			RETURNING cardinality(reembedding_pending)`, run, workspace).Scan(&remaining)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("search: finishing workspace %s of the reembed run: %w", workspace, err)
		}
		if remaining > 0 {
			return nil
		}
		return releaseReembeddingTx(ctx, tx, run)
	})
}

// ReembedProgressStaleness paces how often a working run says so on its marker:
// ReembedWorkspace calls noteReembedProgress once this much time has passed, so
// a pass of any length costs at most one small write per interval.
//
// It is therefore ALMOST the bound on how stale a working run's marker can read,
// but not quite: the entity being embedded when the interval elapses finishes
// first, so the true bound is this plus one embed, which the model lane's own
// per-call timeout caps. A steal window has to clear that sum, not this value.
const ReembedProgressStaleness = 5 * time.Minute

// noteReembedProgress moves run's marker forward to say the run is still
// working. Fenced on the run, like every other write here: a straggler must not
// keep the marker of the run that replaced it looking alive.
func (s *Store) noteReembedProgress(ctx context.Context, run ids.UUID) error {
	// rls-exempt: deployment metadata, no workspace_id
	_, err := s.pool.Exec(ctx, `
		UPDATE embed_store_binding SET updated_at = now() WHERE reembedding_run = $1`, run)
	if err != nil {
		return fmt.Errorf("search: recording reembed progress: %w", err)
	}
	return nil
}

// ReleaseReembedding hands the marker back from run and stamps the store
// populated under what that run targeted. It is refused while the run still has
// a workspace outstanding, so a caller that failed before it fanned out cannot
// take the marker away from children that are already working.
func (s *Store) ReleaseReembedding(ctx context.Context, run ids.UUID) error {
	// rls-exempt: deployment metadata, no workspace_id
	return database.WithInfraTx(ctx, s.pool, func(tx pgx.Tx) error {
		return releaseReembeddingTx(ctx, tx, run)
	})
}

// releaseReembeddingTx is the one spelling of the release, fenced on the run so
// a marker held by a later run is left alone. populated_identity takes the RUN's
// target, never the live config — Postgres evaluates the assignment against the
// pre-update row — because a run finishing under a binding the operator has
// since changed must not stamp the marker as if the new config were populated.
func releaseReembeddingTx(ctx context.Context, tx pgx.Tx, run ids.UUID) error {
	_, err := tx.Exec(ctx, `
		UPDATE embed_store_binding
		SET populated_identity = reembedding_identity, status = 'idle',
		    reembedding_run = NULL, reembedding_identity = NULL,
		    reembedding_pending = '{}', updated_at = now()
		WHERE reembedding_run = $1 AND cardinality(reembedding_pending) = 0`, run)
	if err != nil {
		return fmt.Errorf("search: releasing the reembedding marker: %w", err)
	}
	return nil
}

// PendingByWorkspace is the per-workspace count of live, non-empty-text
// embeddable entities that lack a current-identity embedding row — the
// same set EntitiesPending totals and TokenSumByWorkspace prices. system-
// principal enumeration (mirrors embedgen.go:51-56): this is an index-
// maintenance rollup, not a user-facing read, so it must see every live
// entity regardless of any one caller's row scope.
func (s *Store) PendingByWorkspace(ctx context.Context, currentIdentity string) (map[ids.WorkspaceID]int, error) {
	counts, _, err := s.pendingStats(ctx, currentIdentity)
	return counts, err
}

// TokenSumByWorkspace is the per-workspace SUM(octet_length(<embedText
// source>))/4 over the same pending set PendingByWorkspace counts — a
// rough 4-UTF-8-bytes-per-token estimate (the same convention as
// ai/router.go:410 and ai/fake.go:113, which count bytes not runes, so a
// non-ASCII corpus is not undercounted), feeding Task 14's advisory cost
// preview. No corpus materialization and no model call: the length lives
// in the source columns already.
func (s *Store) TokenSumByWorkspace(ctx context.Context, currentIdentity string) (map[ids.WorkspaceID]int64, error) {
	_, tokens, err := s.pendingStats(ctx, currentIdentity)
	return tokens, err
}

// EntitiesPending is the fleet-wide total — the sum of PendingByWorkspace,
// and the second operand of ReindexNeeded's OR.
func (s *Store) EntitiesPending(ctx context.Context, currentIdentity string) (int, error) {
	counts, err := s.PendingByWorkspace(ctx, currentIdentity)
	if err != nil {
		return 0, err
	}
	total := 0
	for _, c := range counts {
		total += c
	}
	return total, nil
}

// pendingStats enumerates the fleet and, per workspace, counts and sums
// (as the system principal) every embeddable entity whose source text is
// non-empty and which carries no embedding row at currentIdentity. The
// non-empty qualifier is required: an empty-text entity never gets an
// embedding row at all (embedding.go:47-48, UpsertEmbedding's early
// return), so without it such a row would count as pending forever —
// counting the row's ABSENCE, rather than requiring a stale one, is what
// also covers a wiped store (migration 0114's TRUNCATE) as a rebuild path.
func (s *Store) pendingStats(ctx context.Context, currentIdentity string) (map[ids.WorkspaceID]int, map[ids.WorkspaceID]int64, error) {
	workspaces, err := s.fleetWorkspaceIDs(ctx)
	if err != nil {
		return nil, nil, err
	}

	counts := make(map[ids.WorkspaceID]int, len(workspaces))
	tokens := make(map[ids.WorkspaceID]int64, len(workspaces))
	for _, wsID := range workspaces {
		// The generator reads AS the system, same posture as EmbedGen
		// (embedgen.go:51-56): a rollup built through one caller's row
		// scope would silently under-report entities the caller cannot see.
		wsCtx := systemWorkspaceContext(ctx, wsID.UUID)

		count, length, err := s.workspacePending(wsCtx, currentIdentity)
		if err != nil {
			return nil, nil, err
		}
		counts[wsID] = count
		tokens[wsID] = length / 4
	}
	return counts, tokens, nil
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
func (s *Store) fleetWorkspaceIDs(ctx context.Context) ([]ids.WorkspaceID, error) {
	// rls-exempt: fleet enumeration — the workspace table lists every tenant before the per-workspace tx each caller opens next (retention.go:128 precedent).
	rows, err := s.pool.Query(ctx, `SELECT id FROM workspace WHERE archived_at IS NULL ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("search: enumerating workspaces: %w", err)
	}
	workspaces, err := pgx.CollectRows(rows, pgx.RowTo[ids.WorkspaceID])
	if err != nil {
		return nil, fmt.Errorf("search: collecting workspaces: %w", err)
	}
	return workspaces, nil
}

// workspacePending runs one SET-form query per embeddable entity type,
// summing counts and text lengths across all of them for the workspace
// bound in ctx.
func (s *Store) workspacePending(ctx context.Context, currentIdentity string) (count int, length int64, err error) {
	txErr := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		for entityType, src := range pendingSources {
			sql := fmt.Sprintf(`
				SELECT count(*), coalesce(sum(octet_length(btrim(%s))), 0)
				FROM %s t
				WHERE t.archived_at IS NULL
				  AND btrim(%s) <> ''
				  AND NOT EXISTS (
				        SELECT 1 FROM embedding e
				        WHERE e.entity_type = '%s' AND e.entity_id = t.id AND e.model = $1)`,
				src.text, src.table, src.text, entityType)
			var c int
			var l int64
			if err := tx.QueryRow(ctx, sql, currentIdentity).Scan(&c, &l); err != nil {
				return fmt.Errorf("search: scanning pending %s: %w", entityType, err)
			}
			count += c
			length += l
		}
		return nil
	})
	if txErr != nil {
		return 0, 0, txErr
	}
	return count, length, nil
}
