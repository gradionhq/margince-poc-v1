// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package search

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
)

// SweepEmbeddingDrift re-embeds the entities whose embed event the
// at-least-once bus lost (ADR-0069 §3a, SEARCH-AC-13): live entities with
// non-empty source text and no embedding row under the current identity.
// It runs ONLY when the configured identity matches what the store is
// populated under and no fleet-wide reindex job is live — the
// binding-change case keeps its preview→confirm human consent
// (embedreindextransport.go); this sweep never touches the binding
// marker. Idempotent by UpsertEmbedding's content-hash + identity
// skip-compare, so a concurrent ordinary embed of the same entity costs
// nothing. Returns how many entities it embedded.
func (s *Store) SweepEmbeddingDrift(ctx context.Context, embedder Embedder) (int, error) {
	configured, _ := embedder.EmbedIdentity()
	if configured == "" {
		return 0, nil
	}
	populated, status, _, err := s.PopulatedIdentity(ctx)
	if err != nil {
		return 0, err
	}
	if populated != configured || status == "reembedding" {
		return 0, nil
	}

	workspaces, err := s.fleetWorkspaceIDs(ctx)
	if err != nil {
		return 0, err
	}
	healed := 0
	for _, wsID := range workspaces {
		// system principal: the sweep repairs an index over the WHOLE
		// workspace, not one caller's row scope — the same posture as
		// EmbedGen and ReembedCorpus.
		wsCtx := systemWorkspaceContext(ctx, wsID.UUID)
		for entityType, src := range pendingSources {
			items, err := s.pendingEntitiesOf(wsCtx, entityType, src, configured)
			if err != nil {
				return healed, err
			}
			for _, item := range items {
				if _, err := s.UpsertEmbedding(wsCtx, entityType, item.id, item.text, embedder); err != nil {
					return healed, fmt.Errorf("search: drift-sweeping %s %s: %w", entityType, item.id, err)
				}
				healed++
			}
		}
	}
	return healed, nil
}

// pendingEntitiesOf selects the id and source text of every live,
// non-empty-text entity of one type that has no embedding row under
// currentIdentity — the row-form of the set workspacePending counts and
// TokenSumByWorkspace prices, kept to the same predicates so the sweep
// heals exactly what the status endpoint reports as pending. Its own
// short transaction, separate from the UpsertEmbedding calls that follow
// (liveEntitiesOf's reasoning: model calls must not run under a held
// workspace tx).
func (s *Store) pendingEntitiesOf(ctx context.Context, entityType string, src pendingSource, currentIdentity string) ([]liveEntity, error) {
	var items []liveEntity
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		sql := fmt.Sprintf(`
			SELECT t.id, %s FROM %s t
			WHERE t.archived_at IS NULL
			  AND btrim(%s) <> ''
			  AND NOT EXISTS (
			        SELECT 1 FROM embedding e
			        WHERE e.entity_type = '%s' AND e.entity_id = t.id AND e.model = $1)`,
			src.text, src.table, src.text, entityType)
		rows, err := tx.Query(ctx, sql, currentIdentity)
		if err != nil {
			return fmt.Errorf("search: selecting pending %s rows: %w", entityType, err)
		}
		defer rows.Close()
		for rows.Next() {
			var item liveEntity
			if err := rows.Scan(&item.id, &item.text); err != nil {
				return fmt.Errorf("search: scanning pending %s row: %w", entityType, err)
			}
			items = append(items, item)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return items, nil
}
