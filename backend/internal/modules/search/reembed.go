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
)

// ErrIdentityDrift marks a ReembedCorpus call whose target identity
// (argsIdentity, minted when the job was enqueued) no longer matches what
// the embedder compose actually injects — an operator changed the live
// embed binding config after enqueue. Task 15 maps this to
// river.JobCancel rather than a retry: retrying would burn 25 attempts
// against an identity nothing serves anymore, when what the fleet
// actually needs is a NEW job enqueued under the CURRENT config.
var ErrIdentityDrift = errors.New("search: embedder identity drifted from the job's target identity")

// ReembedCorpus rebuilds the embedding corpus fleet-wide under
// argsIdentity. It is resumable BY CONSTRUCTION, not by tracking its own
// progress: UpsertEmbedding's content-hash + identity skip-compare
// (embedding.go) makes a row already current under argsIdentity free to
// revisit, so a crash, a retry, or a deliberate second run all cost
// nothing for entities already done — this routine simply calls
// UpsertEmbedding for every live entity every time and lets that
// skip-compare decide what actually needs a model call.
func (s *Store) ReembedCorpus(ctx context.Context, embedder Embedder, argsIdentity string) error {
	// The entry guard catches a job that started running after the
	// operator swapped the live binding config out from under it: the
	// embedder compose hands this call is always the CURRENT one, so a
	// mismatch here means argsIdentity is stale. Finishing anyway would
	// call CompleteReembedding and stamp populated_identity with an
	// identity the store was never actually re-embedded under — a lie
	// that would only surface later, as a ReindexNeeded that never fires.
	if identity, _ := embedder.EmbedIdentity(); identity != argsIdentity {
		return ErrIdentityDrift
	}

	workspaces, err := s.fleetWorkspaceIDs(ctx)
	if err != nil {
		return err
	}
	for _, wsID := range workspaces {
		// system principal: re-embedding rebuilds an index over the WHOLE
		// workspace, not one caller's row scope — the same posture as
		// EmbedGen (embedgen.go:51-56) and pendingStats.
		wsCtx := systemWorkspaceContext(ctx, wsID.UUID)

		if err := s.reembedWorkspace(wsCtx, embedder); err != nil {
			return fmt.Errorf("search: reembedding workspace %s: %w", wsID, err)
		}
	}

	return s.CompleteReembedding(ctx, argsIdentity)
}

// liveEntity is one row selected for re-embedding: an id plus the exact
// source text pendingSources declares for its entity type.
type liveEntity struct {
	id   ids.UUID
	text string
}

// reembedWorkspace re-embeds every live entity of every embeddable type
// for the workspace bound in ctx. Every UpsertEmbedding error propagates
// as-is (fail-loud): River retries the job rather than this routine
// silently leaving a partially re-embedded corpus.
func (s *Store) reembedWorkspace(ctx context.Context, embedder Embedder) error {
	for entityType, src := range pendingSources {
		items, err := s.liveEntitiesOf(ctx, entityType, src)
		if err != nil {
			return err
		}
		for _, item := range items {
			if _, err := s.UpsertEmbedding(ctx, entityType, item.id, item.text, embedder); err != nil {
				return fmt.Errorf("search: reembedding %s %s: %w", entityType, item.id, err)
			}
		}
	}
	return nil
}

// liveEntitiesOf selects every live (non-archived) row's id and source
// text for one embeddable entity type, in the set-form pendingSources
// declares — the same source text pendingStats sums lengths over. The
// SELECT runs in its own short transaction, separate from the
// UpsertEmbedding calls that follow: those each open their own tx and can
// run many model calls, so this scan must not hold a workspace tx open
// underneath the whole re-embed pass.
func (s *Store) liveEntitiesOf(ctx context.Context, entityType string, src pendingSource) ([]liveEntity, error) {
	var items []liveEntity
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		sql := fmt.Sprintf(`SELECT t.id, %s FROM %s t WHERE t.archived_at IS NULL`, src.text, src.table)
		rows, err := tx.Query(ctx, sql)
		if err != nil {
			return fmt.Errorf("search: selecting live %s rows: %w", entityType, err)
		}
		defer rows.Close()
		for rows.Next() {
			var item liveEntity
			if err := rows.Scan(&item.id, &item.text); err != nil {
				return fmt.Errorf("search: scanning live %s row: %w", entityType, err)
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
