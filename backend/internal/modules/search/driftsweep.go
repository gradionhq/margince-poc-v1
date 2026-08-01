// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package search

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// SweepEmbeddingDrift re-embeds the entities whose embed event the
// at-least-once bus lost (ADR-0069 §3a, SEARCH-AC-13): live entities with
// non-empty source text and no embedding row under the current identity.
// It runs ONLY when the configured identity matches what the store is
// populated under and no fleet-wide reindex job is live — the
// binding-change case keeps its preview→confirm human consent
// (embedreindextransport.go); this sweep never touches the binding
// marker. The marker is re-read before every workspace so a reindex
// claimed (or a binding swapped) mid-pass stops the sweep at the next
// workspace boundary instead of racing the fleet-wide job. Idempotent by
// UpsertEmbedding's content-hash + identity skip-compare, so a concurrent
// ordinary embed of the same entity costs nothing. Returns how many
// entities it actually embedded.
func (s *Store) SweepEmbeddingDrift(ctx context.Context, embedder Embedder) (int, error) {
	configured, _ := embedder.EmbedIdentity()
	if configured == "" {
		return 0, nil
	}

	workspaces, err := s.fleetWorkspaceIDs(ctx)
	if err != nil {
		return 0, err
	}
	healed := 0
	for _, wsID := range workspaces {
		populated, status, _, err := s.PopulatedIdentity(ctx)
		if err != nil {
			return healed, err
		}
		if populated != configured || status == "reembedding" {
			return healed, nil
		}

		// system principal: the sweep repairs an index over the WHOLE
		// workspace, not one caller's row scope — the same posture as
		// EmbedGen and ReembedCorpus.
		wsCtx := systemWorkspaceContext(ctx, wsID.UUID)
		for entityType, src := range pendingSources {
			pendingIDs, err := s.pendingEntityIDs(wsCtx, entityType, src, configured)
			if err != nil {
				return healed, err
			}
			for _, id := range pendingIDs {
				fresh, err := s.healEntity(wsCtx, entityType, id, embedder)
				if err != nil {
					return healed, fmt.Errorf("search: drift-sweeping %s %s: %w", entityType, id, err)
				}
				if fresh {
					healed++
				}
			}
		}
	}
	return healed, nil
}

// healEntity re-reads the entity's CURRENT source text — never the
// pending scan's snapshot — and embeds it. The re-read closes the window
// where an edit lands between the scan and the write: embedding the
// snapshot would store obsolete text under the current identity, and the
// row would then never look pending again even though its embedding is
// wrong. An entity archived or blanked since the scan is simply no longer
// pending — skipped, not an error. Returns whether an embedding row was
// actually written (UpsertEmbedding's own fresh result — false when a
// concurrent ordinary embed won the race and the skip-compare made this
// call free).
func (s *Store) healEntity(ctx context.Context, entityType string, id ids.UUID, embedder Embedder) (bool, error) {
	var text string
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, embedText[entityType], id).Scan(&text)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("re-reading source text: %w", err)
	}
	if strings.TrimSpace(text) == "" {
		return false, nil
	}
	return s.UpsertEmbedding(ctx, entityType, id, text, embedder)
}

// pendingEntityIDs selects every live, non-empty-text entity of one type
// that has no embedding row under currentIdentity — the row-form of the
// set workspacePending counts and TokenSumByWorkspace prices, kept to the
// same predicates so the sweep heals exactly what the status endpoint
// reports as pending. Ids only: the source text is re-read per entity at
// heal time (healEntity's reasoning). Its own short transaction, separate
// from the UpsertEmbedding calls that follow (liveEntitiesOf's reasoning:
// model calls must not run under a held workspace tx).
func (s *Store) pendingEntityIDs(ctx context.Context, entityType string, src pendingSource, currentIdentity string) ([]ids.UUID, error) {
	var pendingIDs []ids.UUID
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		sql := fmt.Sprintf(`
			SELECT t.id FROM %s t
			WHERE t.archived_at IS NULL
			  AND btrim(%s) <> ''
			  AND NOT EXISTS (
			        SELECT 1 FROM embedding e
			        WHERE e.entity_type = '%s' AND e.entity_id = t.id AND e.model = $1)`,
			src.table, src.text, entityType)
		rows, err := tx.Query(ctx, sql, currentIdentity)
		if err != nil {
			return fmt.Errorf("search: selecting pending %s rows: %w", entityType, err)
		}
		collected, err := pgx.CollectRows(rows, pgx.RowTo[ids.UUID])
		if err != nil {
			return fmt.Errorf("search: collecting pending %s ids: %w", entityType, err)
		}
		pendingIDs = collected
		return nil
	})
	if err != nil {
		return nil, err
	}
	return pendingIDs, nil
}
