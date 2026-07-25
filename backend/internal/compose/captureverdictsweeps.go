// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The verdict engine's SWEEP stages — the passes that run whether or not a model
// is configured, because none of them asks one anything.
//
// Judging is the only stage that needs AI. These three are the obligations that
// outlive it: a row nobody can process still has to reach a human, a decline
// still has to close the question, mail a judged-noise sender keeps sending
// still has to be hidden, and content already hidden still has to be redacted on
// schedule. Turning AI off is not consent to retain the content of messages the
// workspace already decided were noise.

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/platform/database"
)

// ReconcileLedger runs the two housekeeping transitions that keep the ledger
// from silently filling up: a row that spent its attempts without ever getting
// an answer retires to `unsure` so a human can take it, and a row whose offer a
// human declined closes as `rejected` so it stops holding a slot.
//
// Both are claim-free and idempotent, and both run BEFORE staging in the pass —
// retiring is what puts a stranded row in front of the review queue in the first
// place, and reconciling declines is what keeps staging from re-asking a
// question that has already been answered.
func (e *CounterpartyVerdictEngine) ReconcileLedger(ctx context.Context) error {
	workspaces, err := liveWorkspaceIDs(ctx, e.pool)
	if err != nil {
		return err
	}
	for _, ws := range workspaces {
		wsCtx := e.workspaceCtx(ctx, ws)
		retired, err := e.pending.RetireExhausted(wsCtx,
			"no usable verdict within the attempt bound")
		if err != nil {
			return err
		}
		if retired > 0 {
			e.log.InfoContext(ctx, "counterparty verdict: retired exhausted dispositions",
				"workspace", ws.String(), "count", retired)
		}
		if _, err := e.pending.ReconcileDeclined(wsCtx); err != nil {
			return err
		}
	}
	return nil
}

// StageReviews offers every `unsure` disposition without an offer yet to a
// human. Run after a verdict pass — and independently of it, so a staging that
// failed while the model was answering is picked up on the next cycle rather
// than leaving a row nobody can act on.
func (e *CounterpartyVerdictEngine) StageReviews(ctx context.Context, maxRows int) error {
	if maxRows <= 0 {
		maxRows = verdictCatchUpCap
	}
	workspaces, err := liveWorkspaceIDs(ctx, e.pool)
	if err != nil {
		return err
	}
	for _, ws := range workspaces {
		wsCtx := e.workspaceCtx(ctx, ws)
		rows, err := e.pending.AwaitingReview(wsCtx, maxRows)
		if err != nil {
			return err
		}
		for _, row := range rows {
			proposalID, err := stageCounterpartyReview(wsCtx, e.approvals, row)
			if err != nil {
				e.log.WarnContext(ctx, "counterparty verdict: staging a review offer failed",
					"disposition", row.ID.String(), "err", err)
				continue
			}
			if proposalID.IsZero() {
				continue
			}
			if err := e.pending.LinkProposal(wsCtx, row.ID, proposalID); err != nil {
				return err
			}
		}
	}
	return nil
}

// HideNoiseStragglers archives mail that arrived from an already-judged noise
// sender after their verdict landed. The verdict hides everything that sender
// had written at the time, but they can keep writing, and the ladder no longer
// asks a question about them — so without this pass their later messages would
// sit on the timeline indefinitely and the promise would hold only for mail that
// happened to arrive first.
//
// Idempotent and cheap: each address's UPDATE matches nothing once its mail is
// hidden, which is the steady state.
func (e *CounterpartyVerdictEngine) HideNoiseStragglers(ctx context.Context) error {
	workspaces, err := liveWorkspaceIDs(ctx, e.pool)
	if err != nil {
		return err
	}
	for _, ws := range workspaces {
		wsCtx := e.workspaceCtx(ctx, ws)
		addresses, err := e.pending.NoiseAddresses(wsCtx, capture.PendingDeferralCap)
		if err != nil {
			return err
		}
		for _, email := range addresses {
			hidden := 0
			err := database.WithWorkspaceTx(wsCtx, e.pool, func(tx pgx.Tx) error {
				var err error
				hidden, err = e.activities.HideCapturedNoiseTx(wsCtx, tx, email)
				return err
			})
			if err != nil {
				return fmt.Errorf("verdict: hiding later noise mail: %w", err)
			}
			if hidden > 0 {
				e.log.InfoContext(ctx, "counterparty verdict: hid later mail from a noise sender",
					"workspace", ws.String(), "messages", hidden)
			}
		}
	}
	return nil
}
