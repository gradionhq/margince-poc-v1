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
)

// ErrIdentityDrift marks a ReembedWorkspace call whose target identity
// (ReembedPass.Identity, captured when the run was claimed) no longer matches
// what the embedder compose actually injects — an operator changed the live
// embed binding config after enqueue. The caller maps this to
// river.JobCancel rather than a retry: retrying would burn attempts
// against an identity nothing serves anymore, when what the fleet
// actually needs is a NEW job enqueued under the CURRENT config.
var ErrIdentityDrift = errors.New("search: embedder identity drifted from the job's target identity")

// ReembedPass is one workspace's slice of a run: the run it reports its progress
// to, the identity it must still be re-embedding under, and the clock that
// progress reporting is paced by.
type ReembedPass struct {
	// Run is the claim this pass belongs to. Every marker write fences on it, so
	// a pass that outlived its own run reports into nothing.
	Run ids.UUID
	// Identity is the embed binding captured when the run was claimed. It is
	// compared against what the embedder reports NOW, and a mismatch is drift.
	Identity string
	// Now is the clock the progress pacing reads. Nil takes the wall clock,
	// which is what the worker passes; a suite pins it, because the alternative
	// is a test that waits out a real interval.
	Now func() time.Time
}

// ReembedWorkspace rebuilds one workspace's embedding corpus under
// pass.Identity. It is resumable BY CONSTRUCTION, not by tracking its own
// progress: UpsertEmbedding's content-hash + identity skip-compare
// (embedding.go) makes a row already current under that identity free to
// revisit, so a crash, a retry, or a deliberate second run all cost
// nothing for entities already done — this routine simply calls
// UpsertEmbedding for every live entity every time and lets that
// skip-compare decide what actually needs a model call.
//
// Every UpsertEmbedding error propagates as-is (fail-loud): the job row
// carrying this workspace fails and is retried, rather than this routine
// silently leaving a partially re-embedded corpus behind a green pass.
//
// It also reports its own progress onto the run's marker as it goes, because a
// pass this long is otherwise indistinguishable from one that died: nothing else
// moves the marker between the fan-out and the workspace finishing, and
// ReembedClaim.StealAfter reads exactly that gap.
func (s *Store) ReembedWorkspace(ctx context.Context, pass ReembedPass, wsID ids.WorkspaceID, embedder Embedder) error {
	// The entry guard catches a job that started running after the
	// operator swapped the live binding config out from under it: the
	// embedder compose hands this call is always the CURRENT one, so a
	// mismatch here means the pass's identity is stale. Re-embedding anyway
	// would index this workspace under a model the run does not target, and the
	// run would go on to stamp populated_identity over it.
	if identity, _ := embedder.EmbedIdentity(); identity != pass.Identity {
		return ErrIdentityDrift
	}
	now := pass.Now
	if now == nil {
		now = time.Now
	}

	// system principal: re-embedding rebuilds an index over the WHOLE
	// workspace, not one caller's row scope — the same posture as
	// EmbedGen (embedgen.go:51-56) and pendingStats.
	wsCtx := systemWorkspaceContext(ctx, wsID.UUID)
	noted := now()
	for entityType, src := range pendingSources {
		items, err := s.liveEntitiesOf(wsCtx, entityType, src)
		if err != nil {
			return err
		}
		for _, item := range items {
			if _, err := s.UpsertEmbedding(wsCtx, entityType, item.id, item.text, embedder); err != nil {
				return fmt.Errorf("search: reembedding %s %s: %w", entityType, item.id, err)
			}
			// Paced by the clock and not by a count of entities: an entity is
			// not a unit of time, so a count would have to be divided by the
			// slowest an entity can be, and would then carry that model timeout
			// into how stale a working run's marker may read. This carries it
			// once — the entity in flight when the interval elapses.
			if elapsed := now(); elapsed.Sub(noted) >= ReembedProgressStaleness {
				if err := s.noteReembedProgress(ctx, pass.Run); err != nil {
					return err
				}
				noted = elapsed
			}
		}
	}
	return nil
}

// liveEntity is one row selected for re-embedding: an id plus the exact
// source text pendingSources declares for its entity type.
type liveEntity struct {
	id   ids.UUID
	text string
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
