// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The brief read model (B-E05.3b): a ranking pass persists as one
// brief_run plus its brief_item rows so the home open re-reads the
// latest run instead of re-ranking, and the acted/dismissed marks
// (B-E05.13) live on the items as the per-rep queue state the next
// run's candidate filter honors. A brief is strictly personal: every
// read and mark resolves through run.user_id = the acting principal —
// another rep's brief reads as not-found, never as forbidden.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// Brief item states (data-model §12.5).
const (
	briefStateNew       = "new"
	briefStateActed     = "acted"
	briefStateDismissed = "dismissed"
)

// BriefRun is one persisted brief for a rep: the queue snapshot plus the
// metadata that reproduces it (candidate count, the revenue norm the
// composite folded with, the as-of cutoff the next run reads "overnight"
// from).
type BriefRun struct {
	ID               ids.UUID
	UserID           ids.UUID
	GeneratedAt      time.Time
	AsOf             time.Time
	CandidateCount   int
	RevenueNormMinor int64
	Items            []BriefRunItem
}

// BriefRunItem is one persisted queue entry with its per-rep state.
type BriefRunItem struct {
	ID          ids.UUID
	DealID      ids.UUID
	Rank        int
	Composite   float64
	Features    BriefFeatureVector
	EvidenceIDs []ids.UUID
	State       string
	StateAt     *time.Time
}

// SnapshotRun ranks and persists one brief run for the acting rep at the
// given instant. The write is audited in the run's own transaction; the
// events.md §5 catalog defines no brief.* type, so the run — like voice
// DNA and lists — is audit-only by the closed-verb law (see the
// writeshape gate's ratified waivers).
func (e *BriefEngine) SnapshotRun(ctx context.Context, now time.Time) (BriefRun, error) {
	ranking, err := e.Rank(ctx, now)
	if err != nil {
		return BriefRun{}, err
	}
	userID, err := briefUser(ctx)
	if err != nil {
		return BriefRun{}, err
	}

	run := BriefRun{
		ID:               ids.NewV7(),
		UserID:           userID,
		GeneratedAt:      now,
		AsOf:             ranking.AsOf,
		CandidateCount:   ranking.CandidateCount,
		RevenueNormMinor: ranking.RevenueNormMinor,
	}
	queueDeals := make([]ids.UUID, 0, len(ranking.Queue))
	err = database.WithWorkspaceTx(ctx, e.pool, func(tx pgx.Tx) error {
		ws := storekit.MustWorkspace(ctx)
		if _, err := tx.Exec(ctx, `
			INSERT INTO brief_run (id, workspace_id, user_id, generated_at, as_of, candidate_count, revenue_norm_minor)
			VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			run.ID, ws, run.UserID, run.GeneratedAt, run.AsOf, run.CandidateCount, run.RevenueNormMinor); err != nil {
			return err
		}
		for i, item := range ranking.Queue {
			features, err := json.Marshal(item.Features)
			if err != nil {
				return err
			}
			persisted := BriefRunItem{
				ID:          ids.NewV7(),
				DealID:      item.DealID,
				Rank:        i + 1,
				Composite:   item.Composite,
				Features:    item.Features,
				EvidenceIDs: item.EvidenceIDs,
				State:       briefStateNew,
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO brief_item (id, workspace_id, brief_run_id, deal_id, rank, composite, feature_vector, evidence_ids, state)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
				persisted.ID, ws, run.ID, persisted.DealID, persisted.Rank,
				persisted.Composite, features, persisted.EvidenceIDs, briefStateNew); err != nil {
				return err
			}
			run.Items = append(run.Items, persisted)
			queueDeals = append(queueDeals, item.DealID)
		}
		_, err := storekit.Audit(ctx, tx, "create", "brief_run", run.ID, nil, map[string]any{
			"user_id":            run.UserID,
			"as_of":              run.AsOf,
			"candidate_count":    run.CandidateCount,
			"revenue_norm_minor": run.RevenueNormMinor,
			"queue_deal_ids":     queueDeals,
		})
		return err
	})
	if err != nil {
		return BriefRun{}, err
	}
	return run, nil
}

// LatestRun re-reads the acting rep's most recent persisted brief — the
// on-open path that must not re-rank. No run yet reads as not-found.
func (e *BriefEngine) LatestRun(ctx context.Context) (BriefRun, error) {
	if err := auth.Require(ctx, "deal", principal.ActionRead); err != nil {
		return BriefRun{}, err
	}
	userID, err := briefUser(ctx)
	if err != nil {
		return BriefRun{}, err
	}

	var run BriefRun
	err = database.WithWorkspaceTx(ctx, e.pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `
			SELECT id, user_id, generated_at, as_of, candidate_count, revenue_norm_minor
			FROM brief_run
			WHERE user_id = $1
			ORDER BY generated_at DESC, id DESC
			LIMIT 1`, userID).
			Scan(&run.ID, &run.UserID, &run.GeneratedAt, &run.AsOf, &run.CandidateCount, &run.RevenueNormMinor)
		if errors.Is(err, pgx.ErrNoRows) {
			return apperrors.ErrNotFound
		}
		if err != nil {
			return err
		}

		rows, err := tx.Query(ctx, `
			SELECT id, deal_id, rank, composite, feature_vector, evidence_ids, state, state_at
			FROM brief_item
			WHERE brief_run_id = $1
			ORDER BY rank`, run.ID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			item, err := scanBriefItem(rows)
			if err != nil {
				return err
			}
			run.Items = append(run.Items, item)
		}
		return rows.Err()
	})
	if err != nil {
		return BriefRun{}, err
	}
	return run, nil
}

// MarkActed records that the rep acted on a queue item; the next run's
// candidate filter drops the deal until it materially changes.
func (e *BriefEngine) MarkActed(ctx context.Context, itemID ids.UUID, now time.Time) (BriefRunItem, error) {
	return e.markItem(ctx, itemID, briefStateActed, now)
}

// MarkDismissed records that the rep dismissed a queue item; the deal
// does not reappear unless a new linked activity arrives after the mark.
func (e *BriefEngine) MarkDismissed(ctx context.Context, itemID ids.UUID, now time.Time) (BriefRunItem, error) {
	return e.markItem(ctx, itemID, briefStateDismissed, now)
}

// markItem is the one acted/dismissed transition: only the run's owner
// may mark, only a pending item transitions (a second mark is a
// conflict, not a silent overwrite), and the write is audited in the
// same transaction. The brief is per-rep personal queue state — the
// object gate is the deal-read grant the brief itself rides on, and the
// real authority is run ownership.
func (e *BriefEngine) markItem(ctx context.Context, itemID ids.UUID, state string, now time.Time) (BriefRunItem, error) {
	if err := auth.Require(ctx, "deal", principal.ActionRead); err != nil {
		return BriefRunItem{}, err
	}
	userID, err := briefUser(ctx)
	if err != nil {
		return BriefRunItem{}, err
	}

	var item BriefRunItem
	err = database.WithWorkspaceTx(ctx, e.pool, func(tx pgx.Tx) error {
		var owner ids.UUID
		row := tx.QueryRow(ctx, `
			SELECT bi.id, bi.deal_id, bi.rank, bi.composite, bi.feature_vector, bi.evidence_ids, bi.state, bi.state_at, br.user_id
			FROM brief_item bi
			JOIN brief_run br ON br.id = bi.brief_run_id
			WHERE bi.id = $1
			FOR UPDATE OF bi`, itemID)
		var featuresRaw []byte
		err := row.Scan(&item.ID, &item.DealID, &item.Rank, &item.Composite, &featuresRaw,
			&item.EvidenceIDs, &item.State, &item.StateAt, &owner)
		if errors.Is(err, pgx.ErrNoRows) {
			return apperrors.ErrNotFound
		}
		if err != nil {
			return err
		}
		if err := json.Unmarshal(featuresRaw, &item.Features); err != nil {
			return fmt.Errorf("brief: item %s carries an unreadable feature vector: %w", item.ID, err)
		}
		if owner != userID {
			// Another rep's brief: existence-hiding, like every row-scope miss.
			return apperrors.ErrNotFound
		}
		if item.State != briefStateNew {
			return apperrors.ErrConflict
		}

		markedAt := now.UTC()
		if _, err := tx.Exec(ctx, `
			UPDATE brief_item SET state = $2, state_at = $3 WHERE id = $1`,
			itemID, state, markedAt); err != nil {
			return err
		}
		before := map[string]any{"state": briefStateNew, "state_at": nil}
		after := map[string]any{"state": state, "state_at": markedAt}
		if _, err := storekit.Audit(ctx, tx, "update", "brief_item", itemID, before, after); err != nil {
			return err
		}
		item.State = state
		item.StateAt = &markedAt
		return nil
	})
	if err != nil {
		return BriefRunItem{}, err
	}
	return item, nil
}

// scanBriefItem reads one brief_item row in the LatestRun column order.
func scanBriefItem(rows pgx.Rows) (BriefRunItem, error) {
	var item BriefRunItem
	var featuresRaw []byte
	if err := rows.Scan(&item.ID, &item.DealID, &item.Rank, &item.Composite,
		&featuresRaw, &item.EvidenceIDs, &item.State, &item.StateAt); err != nil {
		return BriefRunItem{}, err
	}
	if err := json.Unmarshal(featuresRaw, &item.Features); err != nil {
		return BriefRunItem{}, fmt.Errorf("brief: item %s carries an unreadable feature vector: %w", item.ID, err)
	}
	return item, nil
}
