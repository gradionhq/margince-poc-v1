// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package briefs

// The brief read model (B-E05.3b): a ranking pass persists as one
// brief_run plus its brief_item rows so the home open re-reads the
// latest run instead of re-ranking, and the acted/dismissed/snoozed
// marks (B-E05.13, A77) live on the items as the per-rep queue state
// the next run's candidate filter honors. A brief is strictly personal: every
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

// Brief item states (data-model §12.5). A snooze (A77/AC-home-6) hides
// the item until `snoozed_until` passes, then it re-surfaces as
// actionable — unlike acted/dismissed, whose return needs a material
// change on the deal.
const (
	briefStateNew       = "new"
	briefStateActed     = "acted"
	briefStateDismissed = "dismissed"
	briefStateSnoozed   = "snoozed"
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
	ID           ids.UUID
	DealID       ids.UUID
	Rank         int
	Composite    float64
	Features     BriefFeatureVector
	EvidenceIDs  []ids.UUID
	State        string
	StateAt      *time.Time
	SnoozedUntil *time.Time
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
// This read is where a snooze resolves (A77/AC-home-6): an expired
// snooze flips back to actionable inside the read's own transaction,
// and a still-running one keeps its item hidden — so the returned run
// is always what the rep should see NOW, without a refresh.
//
// The queue may therefore be shorter than the run it re-reads, and the
// answer says nothing about that on purpose: a count of what the row
// scope removed is the side channel existence-hiding closes. The agent
// door already states the query-level fact for both of them —
// agents.noteRowScope raises BYO-RES-2's warning on every tool call by
// a bounded actor, which is exactly when an item can drop here.
func (e *BriefEngine) LatestRun(ctx context.Context, now time.Time) (BriefRun, error) {
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

		if err := resurfaceExpiredSnoozes(ctx, tx, run.ID, now); err != nil {
			return err
		}

		run.Items, err = readRunItems(ctx, tx, run.ID)
		return err
	})
	if err != nil {
		return BriefRun{}, err
	}
	return run, nil
}

// readRunItems reads back one run's visible queue in rank order.
//
// The join is the point: a brief item is a REFERENCE to a deal, persisted
// when the ranking queued it and served for as long as the run lives, so
// the deal's row scope is re-applied HERE rather than inherited from the
// snapshot that wrote it. A deal reassigned since then leaves the queue,
// which is the same answer the deal's own read gives on that id
// (deals.Store.GetDeal: the object gate, then auth.EnsureVisible).
//
// Unexpired snoozes stay hidden; everything else, including the rows the
// caller just re-surfaced, reads back.
func readRunItems(ctx context.Context, tx pgx.Tx, runID ids.UUID) ([]BriefRunItem, error) {
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	runPos := arg(runID)

	scope, err := auth.ScopeClauseFor(ctx, "deal", "d", arg)
	if err != nil {
		return nil, err
	}
	q := fmt.Sprintf(`
		SELECT bi.id, bi.deal_id, bi.rank, bi.composite, bi.feature_vector, bi.evidence_ids, bi.state, bi.state_at, bi.snoozed_until
		FROM brief_item bi
		JOIN deal d ON d.id = bi.deal_id
		WHERE bi.brief_run_id = $%d AND bi.state <> 'snoozed'`, runPos)
	if scope != "" {
		q += " AND " + scope
	}
	q += " ORDER BY bi.rank"

	rows, err := tx.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []BriefRunItem
	for rows.Next() {
		item, err := scanBriefItem(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// resurfaceExpiredSnoozes flips a run's expired snoozes back to
// actionable — the A77 re-surface half of the snooze contract. Each flip
// is a state change on per-rep queue data, so it is audited like the
// rep's own marks (brief rows are audit-only; no brief.* event exists in
// the events.md catalog).
func resurfaceExpiredSnoozes(ctx context.Context, tx pgx.Tx, runID ids.UUID, now time.Time) error {
	resurfaced, err := collectIDList(tx.Query(ctx, `
		UPDATE brief_item
		SET state = 'new', state_at = NULL, snoozed_until = NULL
		WHERE brief_run_id = $1 AND state = 'snoozed' AND snoozed_until <= $2
		RETURNING id`, runID, now.UTC()))
	if err != nil {
		return err
	}
	for _, itemID := range resurfaced {
		before := map[string]any{"state": briefStateSnoozed}
		after := map[string]any{"state": briefStateNew, "state_at": nil, "snoozed_until": nil}
		if _, err := storekit.Audit(ctx, tx, "update", "brief_item", itemID, before, after); err != nil {
			return err
		}
	}
	return nil
}

// MarkActed records that the rep acted on a queue item; the next run's
// candidate filter drops the deal until it materially changes.
func (e *BriefEngine) MarkActed(ctx context.Context, itemID ids.UUID, now time.Time) (BriefRunItem, error) {
	return e.markItem(ctx, itemID, briefStateActed, nil, now)
}

// MarkDismissed records that the rep dismissed a queue item; the deal
// does not reappear unless a new linked activity arrives after the mark.
func (e *BriefEngine) MarkDismissed(ctx context.Context, itemID ids.UUID, now time.Time) (BriefRunItem, error) {
	return e.markItem(ctx, itemID, briefStateDismissed, nil, now)
}

// MarkSnoozed hides a queue item until the given instant (A77/AC-home-6),
// after which it re-surfaces as actionable. The transport validates that
// `until` lies in the future; this transition only records it.
func (e *BriefEngine) MarkSnoozed(ctx context.Context, itemID ids.UUID, until, now time.Time) (BriefRunItem, error) {
	untilUTC := until.UTC()
	return e.markItem(ctx, itemID, briefStateSnoozed, &untilUTC, now)
}

// markItem is the one acted/dismissed/snoozed transition: only the run's
// owner may mark, only an actionable item transitions (a second mark is a
// conflict, not a silent overwrite), and the write is audited in the
// same transaction. An expired snooze counts as actionable — a rep who
// marks straight from a stale screen must not read differently from one
// who re-opened the brief and had the item re-surfaced first. The brief
// is per-rep personal queue state — the object gate is the deal-read
// grant the brief itself rides on, and the real authority is run
// ownership.
func (e *BriefEngine) markItem(ctx context.Context, itemID ids.UUID, state string, snoozedUntil *time.Time, now time.Time) (BriefRunItem, error) {
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
			SELECT bi.id, bi.deal_id, bi.rank, bi.composite, bi.feature_vector, bi.evidence_ids, bi.state, bi.state_at, bi.snoozed_until, br.user_id
			FROM brief_item bi
			JOIN brief_run br ON br.id = bi.brief_run_id
			WHERE bi.id = $1
			FOR UPDATE OF bi`, itemID)
		var featuresRaw []byte
		err := row.Scan(&item.ID, &item.DealID, &item.Rank, &item.Composite, &featuresRaw,
			&item.EvidenceIDs, &item.State, &item.StateAt, &item.SnoozedUntil, &owner)
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
		// The mark's twin of the join in readRunItems: the item names a deal
		// that may have moved since the run was assembled, and a mark is a
		// read-back as much as a write. Checked BEFORE actionability, so a
		// deal the rep can no longer see answers not-found rather than
		// disclosing the item's state through a conflict.
		if err := auth.EnsureVisible(ctx, tx, "deal", item.DealID); err != nil {
			return err
		}
		if !briefItemActionable(item, now) {
			return apperrors.ErrConflict
		}

		markedAt := now.UTC()
		if _, err := tx.Exec(ctx, `
			UPDATE brief_item SET state = $2, state_at = $3, snoozed_until = $4 WHERE id = $1`,
			itemID, state, markedAt, snoozedUntil); err != nil {
			return err
		}
		before := map[string]any{"state": item.State, "state_at": item.StateAt, "snoozed_until": item.SnoozedUntil}
		after := map[string]any{"state": state, "state_at": markedAt, "snoozed_until": snoozedUntil}
		if _, err := storekit.Audit(ctx, tx, "update", "brief_item", itemID, before, after); err != nil {
			return err
		}
		item.State = state
		item.StateAt = &markedAt
		item.SnoozedUntil = snoozedUntil
		return nil
	})
	if err != nil {
		return BriefRunItem{}, err
	}
	return item, nil
}

// briefItemActionable says whether a rep may still mark this item: fresh,
// or snoozed past its snoozed_until (the re-surface may not have been
// materialized by a read yet, but the item is already actionable again).
func briefItemActionable(item BriefRunItem, now time.Time) bool {
	if item.State == briefStateNew {
		return true
	}
	return item.State == briefStateSnoozed && item.SnoozedUntil != nil && !now.UTC().Before(*item.SnoozedUntil)
}

// scanBriefItem reads one brief_item row in the LatestRun column order.
func scanBriefItem(rows pgx.Rows) (BriefRunItem, error) {
	var item BriefRunItem
	var featuresRaw []byte
	if err := rows.Scan(&item.ID, &item.DealID, &item.Rank, &item.Composite,
		&featuresRaw, &item.EvidenceIDs, &item.State, &item.StateAt, &item.SnoozedUntil); err != nil {
		return BriefRunItem{}, err
	}
	if err := json.Unmarshal(featuresRaw, &item.Features); err != nil {
		return BriefRunItem{}, fmt.Errorf("brief: item %s carries an unreadable feature vector: %w", item.ID, err)
	}
	return item, nil
}
