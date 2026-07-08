// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package deals

// The overnight follow-up reconciliation pass (features/07 §8a,
// B-E06.2a): the deterministic half of the overnight agent's
// "reconcile the day's captured calls/mail/meetings into staged
// proposals" contract. Over every open deal that had a real captured
// interaction (call/mail/meeting) in the reconciliation window but has
// NO open follow-up on its timeline, the pass stages a "draft this
// follow-up" proposal — never a silent write. A human confirms (or
// edits) it in the morning approval inbox; only then is the follow-up
// task created. It follows the close-date corrector's shape exactly:
// one read pass per live workspace, the truth decided in Go over an
// injectable clock, staging through an approvals seam the composition
// root fills, and the confirm effect owned there too.
//
// Scope note (B-E06.2a): the value-judgement proposals in §8a — a stage
// / next-step / amount change inferred from what was SAID on a call —
// are model reasoning (ai-operational-spec §3.2, AIUC-15) that ride the
// Surface-B runner, not this deterministic sweep; the corrected
// expected_close_date is already the close-date corrector's job
// (formulas §11). This pass owns the one reconciliation discrepancy that
// is honestly deterministic: a real interaction with no next step
// planned.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// FollowUpReconcileKind is the approvals staging kind the pass surfaces
// through; its decision grant lives in the approvals module and its
// confirm effect is injected at the composition root.
const FollowUpReconcileKind = "deal_follow_up"

const (
	// reconcileLookback is the interaction window the pass reconciles: a
	// deal counts as "touched" when a call/mail/meeting landed on it
	// within it. Set a little wider than the nightly cadence so a skipped
	// run does not silently drop a follow-up — the pending-check keeps a
	// still-undecided proposal from being restaged.
	reconcileLookback = 48 * time.Hour
	// followUpLeadDays is how far ahead the proposed follow-up is due.
	// §8a names no cadence, so this is the smallest sensible default (the
	// human edits the date on confirm); it is not a spec constant.
	followUpLeadDays = 3
	// reconcileBatch bounds how many follow-ups one workspace pass stages
	// — a first run over a migrated backlog drains across nights.
	reconcileBatch = 200
)

// FollowUpProposal is the staged proposed_change payload: everything a
// human needs to confirm (or edit) the drafted follow-up, and the
// confirm effect needs to create it. Its evidence is resolvable (the
// triggering activity id) — a proposal never floats free of the record
// it came from (P5/P12).
type FollowUpProposal struct {
	DealID ids.DealID `json:"deal_id"`
	// DueDate is the proposed follow-up due date, date-only wire form.
	DueDate string `json:"due_date"`
	Subject string `json:"subject"`
	Body    string `json:"body"`
	// EvidenceActivityID resolves to the interaction that triggered the
	// proposal; EvidenceKind/EvidenceOccurredAt render it without a join.
	EvidenceActivityID ids.ActivityID `json:"evidence_activity_id"`
	EvidenceKind       string         `json:"evidence_kind"`
	EvidenceOccurredAt time.Time      `json:"evidence_occurred_at"`
}

// UnmarshalFollowUpProposal decodes a staged (possibly human-edited)
// proposal back into the typed form the confirm effect creates.
func UnmarshalFollowUpProposal(raw json.RawMessage) (FollowUpProposal, error) {
	var p FollowUpProposal
	if err := json.Unmarshal(raw, &p); err != nil {
		return FollowUpProposal{}, fmt.Errorf("deal_follow_up payload: %w", err)
	}
	if p.DealID.IsZero() {
		return FollowUpProposal{}, errors.New("deal_follow_up payload names no deal")
	}
	if _, err := time.Parse(time.DateOnly, p.DueDate); err != nil {
		return FollowUpProposal{}, fmt.Errorf("deal_follow_up payload due date: %w", err)
	}
	if p.Subject == "" {
		return FollowUpProposal{}, errors.New("deal_follow_up payload carries no subject")
	}
	return p, nil
}

// FollowUpStager is the approvals seam the composition root fills (a
// module never imports a sibling): stage a follow-up proposal, and ask
// whether one is already pending so a nightly sweep — whose proposal
// moves with "today" — cannot stack duplicates on one still awaiting a
// decision.
type FollowUpStager interface {
	HasPendingFollowUp(ctx context.Context, dealID ids.UUID) (bool, error)
	StageFollowUp(ctx context.Context, dealID ids.UUID, summary string, proposal FollowUpProposal) error
}

// FollowUpReconciler drives the pass; the worker ticks it nightly.
type FollowUpReconciler struct {
	pool   *pgxpool.Pool
	stager FollowUpStager
	log    *slog.Logger
	// now is the pass's clock so the reconciliation window and the
	// proposed due date reproduce under a fixed test clock.
	now func() time.Time
}

func NewFollowUpReconciler(pool *pgxpool.Pool, stager FollowUpStager, log *slog.Logger) *FollowUpReconciler {
	return &FollowUpReconciler{pool: pool, stager: stager, log: log, now: time.Now}
}

// Reconcile is one pass over every live workspace. Like the close-date
// corrector, the workspace list is bounded by fleet size, and one
// tenant's failure must not starve the rest.
func (r *FollowUpReconciler) Reconcile(ctx context.Context) error {
	rows, err := r.pool.Query(ctx, `SELECT id FROM workspace WHERE archived_at IS NULL ORDER BY created_at`)
	if err != nil {
		return err
	}
	workspaces, err := pgx.CollectRows(rows, pgx.RowTo[ids.UUID])
	if err != nil {
		return err
	}
	for _, wsID := range workspaces {
		wsCtx := principal.WithWorkspaceID(ctx, wsID)
		// The overnight agent is the acting principal — its writes and
		// stagings carry agent:overnight provenance (features/07 §8a),
		// and every read is workspace-bound by RLS through the GUC below.
		wsCtx = principal.WithActor(wsCtx, principal.Principal{Type: principal.PrincipalSystem, ID: "agent:overnight"})
		wsCtx = principal.WithCorrelationID(wsCtx, ids.NewV7())
		if err := r.reconcileWorkspace(wsCtx); err != nil {
			r.log.Error("follow-up reconcile: workspace pass failed", "workspace", wsID, "err", err)
		}
	}
	return nil
}

// followUpCandidate is one open deal the pass found touched-but-without-
// a-next-step, carrying the interaction that is its evidence.
type followUpCandidate struct {
	dealID       ids.DealID
	dealName     string
	activityID   ids.ActivityID
	activityKind string
	occurredAt   time.Time
	subject      *string
}

func (r *FollowUpReconciler) reconcileWorkspace(ctx context.Context) error {
	now := r.now().UTC()
	var candidates []followUpCandidate
	err := database.WithWorkspaceTx(ctx, r.pool, func(tx pgx.Tx) error {
		// The discrepancy: an open deal whose most recent real interaction
		// (call/mail/meeting) landed inside the window, and which has NO
		// open task on its timeline — a touch with no next step planned.
		// A note or a done task is not a next step; an undone task is (so
		// the sweep does not nag a rep who already has one queued).
		rows, err := tx.Query(ctx, `
			SELECT d.id, d.name, ev.activity_id, ev.kind, ev.occurred_at, ev.subject
			FROM deal d
			JOIN LATERAL (
				SELECT a.id AS activity_id, a.kind, a.occurred_at, a.subject
				FROM activity a
				JOIN activity_link l ON l.activity_id = a.id AND l.deal_id = d.id
				WHERE a.kind IN ('call','email','meeting')
				  AND a.archived_at IS NULL
				  AND a.occurred_at >= $1
				ORDER BY a.occurred_at DESC, a.id DESC
				LIMIT 1
			) ev ON true
			WHERE d.status = 'open' AND d.archived_at IS NULL
			  AND NOT EXISTS (
				SELECT 1 FROM activity t
				JOIN activity_link tl ON tl.activity_id = t.id AND tl.deal_id = d.id
				WHERE t.kind = 'task' AND t.is_done = false AND t.archived_at IS NULL
			  )
			ORDER BY d.id
			LIMIT $2`, now.Add(-reconcileLookback), reconcileBatch)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var c followUpCandidate
			if err := rows.Scan(&c.dealID, &c.dealName, &c.activityID, &c.activityKind, &c.occurredAt, &c.subject); err != nil {
				return err
			}
			candidates = append(candidates, c)
		}
		return rows.Err()
	})
	if err != nil {
		return err
	}

	for _, cand := range candidates {
		if err := r.stage(ctx, cand, now); err != nil {
			return fmt.Errorf("follow-up reconcile on %s: %w", cand.dealID, err)
		}
	}
	return nil
}

// stage records one deal's follow-up proposal unless one is already
// pending — the pass proposes, it never writes the follow-up itself.
func (r *FollowUpReconciler) stage(ctx context.Context, cand followUpCandidate, now time.Time) error {
	pending, err := r.stager.HasPendingFollowUp(ctx, cand.dealID.UUID)
	if err != nil {
		return err
	}
	if pending {
		return nil
	}
	dueDate := dateOnly(now).AddDate(0, 0, followUpLeadDays)
	proposal := FollowUpProposal{
		DealID:             cand.dealID,
		DueDate:            dueDate.Format(time.DateOnly),
		Subject:            fmt.Sprintf("Follow up on %s", cand.dealName),
		Body:               followUpBody(cand),
		EvidenceActivityID: cand.activityID,
		EvidenceKind:       cand.activityKind,
		EvidenceOccurredAt: cand.occurredAt,
	}
	summary := fmt.Sprintf("Draft a follow-up on %q — a %s on %s left no next step planned",
		cand.dealName, cand.activityKind, cand.occurredAt.Format(time.DateOnly))
	return r.stager.StageFollowUp(ctx, cand.dealID.UUID, summary, proposal)
}

// followUpBody grounds the drafted follow-up in the real last exchange
// (P5) — the human sees what it is answering before they approve.
func followUpBody(cand followUpCandidate) string {
	ref := cand.activityKind
	if cand.subject != nil && *cand.subject != "" {
		ref = fmt.Sprintf("%s “%s”", cand.activityKind, *cand.subject)
	}
	return fmt.Sprintf("Follow up on the %s from %s. No next step is on the timeline yet.",
		ref, cand.occurredAt.Format(time.DateOnly))
}

// RunFollowUpReconcile ticks the reconciler on the worker's schedule,
// the same loop shape as the retention evaluator and the close-date
// corrector.
func RunFollowUpReconcile(ctx context.Context, r *FollowUpReconciler, interval time.Duration, log *slog.Logger) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := r.Reconcile(ctx); err != nil {
			log.Error("follow-up reconcile: pass failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
