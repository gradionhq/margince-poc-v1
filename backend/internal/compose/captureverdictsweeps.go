// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The verdict engine's SWEEP stages — the passes that run whether or not a model
// is configured, because none of them asks one anything.
//
// Judging is the only stage that needs AI. These are the obligations that
// outlive it: a row nobody can process still has to reach a human, a decline
// still has to close the question, mail a judged-noise sender keeps sending
// still has to be hidden, and content already hidden still has to be redacted on
// schedule. Turning AI off is not consent to retain the content of messages the
// workspace already decided were noise.

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
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

// staleReviewBatch bounds one age-out pass. The backlog is a query, so what a
// pass leaves behind the next tick takes.
const staleReviewBatch = 200

// AgeOutStaleReviews closes the questions nobody answered. An `unsure` row waits
// UnsureReviewWindow for a human; past that the ledger stops asking, and the
// offer standing in the review queue is withdrawn with it.
//
// Without this the queue only ever grows. A staged offer expires after a day and
// StageReviews honestly re-offers the row, so an unanswered question cycles
// forever — holding a slot against the deferral ceiling and against its sender's
// address the whole time. That is the tail an outsider can lean on: mail from
// enough fresh addresses and the workspace never defers anyone new again.
//
// Closing as `rejected` creates nothing and touches no mail, and the sender is
// not shut out: the live-unique index covers only `pending` and `unsure`, so
// their next message opens a fresh row and gets a fresh verdict.
//
// The withdrawal and the ledger write share one transaction, so the inbox can
// never hold an offer whose accept would resolve nothing.
func (e *CounterpartyVerdictEngine) AgeOutStaleReviews(ctx context.Context, window time.Duration) error {
	return e.eachWorkspace(ctx, func(wsCtx context.Context, ws ids.UUID) error {
		stale, err := e.pending.StaleReviews(wsCtx, window, staleReviewBatch)
		if err != nil {
			return err
		}
		closed := 0
		for _, row := range stale {
			aged, err := e.ageOutOneReview(wsCtx, row)
			if err != nil {
				return fmt.Errorf("verdict: ageing out an unanswered review: %w", err)
			}
			if aged {
				closed++
			}
		}
		if closed > 0 {
			e.log.InfoContext(ctx, "counterparty verdict: closed unanswered review questions",
				"workspace", ws.String(), "count", closed)
		}
		return nil
	})
}

// ageOutOneReview closes one question and withdraws its offer in ONE
// transaction, in the order that makes a concurrent human decision safe: lock
// the ledger row, then take the offer off the inbox, then close the row. A
// decision that got there first leaves the offer no longer pending, and this
// backs out having changed nothing.
func (e *CounterpartyVerdictEngine) ageOutOneReview(ctx context.Context, row capture.StaleReview) (bool, error) {
	aged := false
	err := database.WithWorkspaceTx(ctx, e.pool, func(tx pgx.Tx) error {
		proposalID, ok, err := e.pending.ClaimReviewForAgeOut(ctx, tx, row.ID)
		if err != nil || !ok {
			return err
		}
		if proposalID != nil {
			withdrawn, err := e.approvals.WithdrawInTx(ctx, tx, ids.From[ids.ApprovalKind](*proposalID),
				"no decision within the review window")
			if err != nil {
				return err
			}
			// The offer was decided while this pass was scanning. The human's
			// answer, and the effect it releases, owns the row.
			if !withdrawn {
				return nil
			}
		}
		if err := e.pending.AgeOutReviewTx(ctx, tx, row.ID); err != nil {
			return err
		}
		aged = true
		return nil
	})
	return aged, err
}

// HideNoiseStragglers archives captured mail from judged-noise senders that is
// still visible — the messages that arrived after their verdict, and any the
// verdict transaction did not reach.
//
// Driven from the MAIL, not from a list of addresses: the work is bounded by
// what is actually outstanding, so a workspace with more noise senders than any
// page size cannot silently stop covering the oldest of them. Idempotent, and a
// no-op in the steady state.
func (e *CounterpartyVerdictEngine) HideNoiseStragglers(ctx context.Context) error {
	return e.eachWorkspace(ctx, func(wsCtx context.Context, ws ids.UUID) error {
		due, err := e.pending.NoiseMailToHide(wsCtx, noiseSweepBatch)
		if err != nil {
			return err
		}
		if len(due) == 0 {
			return nil
		}
		hidden := 0
		err = database.WithWorkspaceTx(wsCtx, e.pool, func(tx pgx.Tx) error {
			var err error
			hidden, err = e.activities.HideCapturedNoiseTx(wsCtx, tx, due)
			return err
		})
		if err != nil {
			return fmt.Errorf("verdict: hiding noise mail: %w", err)
		}
		if hidden > 0 {
			e.log.InfoContext(ctx, "counterparty verdict: hid mail from judged-noise senders",
				"workspace", ws.String(), "messages", hidden)
		}
		return nil
	})
}

// RedactNoise is the second stage of the noise disposition: content-keyed, so it
// covers whatever is outstanding rather than firing once per disposition and
// retaining everything that sender wrote afterwards.
//
// There is no completion flag to set. The absence of unredacted mail IS the
// completed state, which makes a crash mid-sweep cost nothing and a re-run
// finish the job — where a one-shot marker could be stamped on a row whose
// content survived, and nothing would ever revisit it.
func (e *CounterpartyVerdictEngine) RedactNoise(ctx context.Context, window time.Duration, maxRows int) error {
	if maxRows <= 0 {
		maxRows = noiseSweepBatch
	}
	return e.eachWorkspace(ctx, func(wsCtx context.Context, ws ids.UUID) error {
		due, err := e.pending.NoiseMailToRedact(wsCtx, window, maxRows)
		if err != nil {
			return err
		}
		// ONE transaction for the whole destruction: the activity's text, the
		// vectors derived from it, and the provider original behind it commit
		// together or not at all.
		//
		// Splitting them was not survivable. Once the activity's content is
		// nulled it no longer looks like outstanding work, so a failure between
		// two transactions would strand the original in raw_capture with nothing
		// left that would ever collect it — a silent, permanent retention of the
		// exact message the workspace decided to destroy.
		redacted := 0
		err = database.WithWorkspaceTx(wsCtx, e.pool, func(tx pgx.Tx) error {
			done, err := e.activities.RedactCapturedNoiseTx(wsCtx, tx, due)
			if err != nil {
				return err
			}
			// Keyed on what was actually redacted, never on what was proposed: a
			// message a human un-archived since the backlog was read keeps its
			// content, and must keep its original with it.
			if err := e.pending.PurgeRawCaptureTx(wsCtx, tx, done); err != nil {
				return err
			}
			redacted = len(done)
			return nil
		})
		if err != nil {
			return fmt.Errorf("verdict: redacting noise mail: %w", err)
		}
		if redacted > 0 {
			e.log.InfoContext(ctx, "counterparty verdict: redacted hidden mail past its undo window",
				"workspace", ws.String(), "messages", redacted)
		}
		return nil
	})
}

// noiseSweepBatch bounds one sweep pass. The backlog is a query, so what a pass
// leaves behind the next one picks up — the bound limits the work per tick, not
// the coverage.
const noiseSweepBatch = 500

// eachWorkspace runs fn under every live workspace's own principal and GUC. The
// sweeps all share this shape, and sharing it is what keeps a new one from
// quietly running under the wrong workspace.
func (e *CounterpartyVerdictEngine) eachWorkspace(ctx context.Context, fn func(context.Context, ids.UUID) error) error {
	workspaces, err := liveWorkspaceIDs(ctx, e.pool)
	if err != nil {
		return err
	}
	for _, ws := range workspaces {
		if err := fn(e.workspaceCtx(ctx, ws), ws); err != nil {
			return err
		}
	}
	return nil
}
