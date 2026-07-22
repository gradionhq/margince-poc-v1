// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package search

import (
	"context"
	"errors"
	"fmt"

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
	"person":       {table: "person", text: "t.full_name"},
	"organization": {table: "organization", text: "concat_ws(' ', t.display_name, t.legal_name, t.industry)"},
	"deal":         {table: "deal", text: "t.name"},
	"lead":         {table: "lead", text: "concat_ws(' ', t.full_name, t.company_name, t.title)"},
	"activity":     {table: "activity", text: "concat_ws(' ', t.subject, t.body)"},
}

// SeedBinding plants the marker row on first boot. An empty store is
// vacuously "populated under the current binding" — seeding
// populated_identity to the LIVE config (never a sentinel) is what keeps a
// fresh install's derived ReindexNeeded false (design §5.6-swap step 1: no
// first-boot wart). ON CONFLICT DO NOTHING makes concurrent boots and
// restarts idempotent — the marker is written once, ever, outside a
// completed reindex.
func (s *Store) SeedBinding(ctx context.Context, configuredIdentity string) error {
	// rls-exempt: deployment metadata, no workspace_id (embed_store_binding,
	// migration 0113) — this write must not ride a per-workspace GUC tx.
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
// own view of what the store is populated under, and the job lifecycle
// status. It never joins the live entity scan — that cost belongs to the
// ops status endpoint, not the readiness probe.
func (s *Store) PopulatedIdentity(ctx context.Context) (identity string, status string, err error) {
	// rls-exempt: deployment metadata, no workspace_id
	err = s.pool.QueryRow(ctx, `SELECT populated_identity, status FROM embed_store_binding WHERE singleton`).
		Scan(&identity, &status)
	if err != nil {
		return "", "", fmt.Errorf("search: reading binding marker: %w", err)
	}
	return identity, status, nil
}

// ReindexNeeded is the DERIVED "does the store need a re-embed" signal
// (design §5.6-swap v7) — there is no stored needs_reembed flag to
// demote, latch, or lie: this recomputes from the marker plus a live scan
// every time it is asked, so a mid-job restart, a config revert, and a
// late completion under a yet-different config are all honest by
// construction instead of depending on someone remembering to clear a bit.
func (s *Store) ReindexNeeded(ctx context.Context, configuredIdentity string) (bool, error) {
	populated, _, err := s.PopulatedIdentity(ctx)
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

// ClaimAndEnqueueReembedding runs the CAS and the caller's enqueue in ONE
// raw-pool transaction (the store-owned-tx + callback shape,
// compose/deepreadtransport.go:97-107): if enqueue errors the whole
// transaction rolls back, so the claim can never outlive a job that was
// never actually queued. The CAS itself cannot distinguish "moved from
// idle" from "was already reembedding" — PG16 has no OLD.* in RETURNING,
// and a post-update read there always sees the NEW row — so the
// 409-vs-202 decision downstream comes from the enqueue's own
// unique-skip outcome (Task 11/15), never from this statement's row count.
func (s *Store) ClaimAndEnqueueReembedding(ctx context.Context, enqueue func(tx pgx.Tx) error) error {
	// rls-exempt: deployment metadata, no workspace_id — the CAS and the
	// job enqueue share one non-tenant transaction so a rolled-back enqueue
	// always undoes the claim; WithInfraTx is the platform's cross-tenant
	// tx shape (no GUC to bind, there is no tenant here).
	return database.WithInfraTx(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE embed_store_binding SET status = 'reembedding', updated_at = now()
			WHERE status IN ('idle', 'reembedding')`)
		if err != nil {
			return fmt.Errorf("search: claiming reembedding: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return ErrBindingNotSeeded
		}
		return enqueue(tx)
	})
}

// CompleteReembedding is the job's clean-completion CAS: it only ever
// moves the row OUT of reembedding, and only writes populated_identity to
// the JOB's args identity (never the live config) — a job that finishes
// under a binding the operator has since changed must not stamp the
// marker as if the new config were populated.
func (s *Store) CompleteReembedding(ctx context.Context, jobArgsIdentity string) error {
	// rls-exempt: deployment metadata, no workspace_id
	_, err := s.pool.Exec(ctx, `
		UPDATE embed_store_binding SET populated_identity = $1, status = 'idle', updated_at = now()
		WHERE status = 'reembedding'`, jobArgsIdentity)
	if err != nil {
		return fmt.Errorf("search: completing reembedding: %w", err)
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

// TokenSumByWorkspace is the per-workspace SUM(length(<embedText
// source>))/4 over the same pending set PendingByWorkspace counts — a
// rough 4-bytes-per-token estimate (the same convention as
// ai/router.go:410 and ai/fake.go:113), feeding Task 14's advisory cost
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
// also covers a wiped store (migration 0113's TRUNCATE) as a rebuild path.
func (s *Store) pendingStats(ctx context.Context, currentIdentity string) (map[ids.WorkspaceID]int, map[ids.WorkspaceID]int64, error) {
	// rls-exempt: fleet enumeration — the workspace table lists every
	// tenant before the per-workspace tx below (retention.go:128 precedent).
	rows, err := s.pool.Query(ctx, `SELECT id FROM workspace WHERE archived_at IS NULL ORDER BY created_at`)
	if err != nil {
		return nil, nil, fmt.Errorf("search: enumerating workspaces: %w", err)
	}
	workspaces, err := pgx.CollectRows(rows, pgx.RowTo[ids.WorkspaceID])
	if err != nil {
		return nil, nil, fmt.Errorf("search: collecting workspaces: %w", err)
	}

	counts := make(map[ids.WorkspaceID]int, len(workspaces))
	tokens := make(map[ids.WorkspaceID]int64, len(workspaces))
	for _, wsID := range workspaces {
		wsCtx := principal.WithWorkspaceID(ctx, wsID.UUID)
		// The generator reads AS the system, same posture as EmbedGen
		// (embedgen.go:51-56): a rollup built through one caller's row
		// scope would silently under-report entities the caller cannot see.
		wsCtx = principal.WithActor(wsCtx, principal.Principal{Type: principal.PrincipalSystem, ID: "system"})

		count, length, err := s.workspacePending(wsCtx, currentIdentity)
		if err != nil {
			return nil, nil, err
		}
		counts[wsID] = count
		tokens[wsID] = length / 4
	}
	return counts, tokens, nil
}

// workspacePending runs one SET-form query per embeddable entity type,
// summing counts and text lengths across all of them for the workspace
// bound in ctx.
func (s *Store) workspacePending(ctx context.Context, currentIdentity string) (count int, length int64, err error) {
	txErr := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		for entityType, src := range pendingSources {
			sql := fmt.Sprintf(`
				SELECT count(*), coalesce(sum(length(btrim(%s))), 0)
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
