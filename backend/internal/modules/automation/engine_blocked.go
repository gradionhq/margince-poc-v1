// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package automation

// The 'blocked' terminal outcome (A72/ADR-0035 Am.1, migration 0061): a
// workflow run that staged a 🟡 approval and then saw it rejected is a
// finished run whose effect never happened — the history must say so,
// with which approval and why. The linkage rides the run row's detail
// column (workflow_run gained no separate approval_id column): the
// Apply path stamps stagedApprovalDetail(id) (rundetail.go) when it
// parks the run, and blocking matches on that jsonb payload's
// approval_id field.

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"

	kevents "github.com/gradionhq/margince/backend/internal/shared/kernel/events"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// HandleApprovalDecided is the engine-side approval.decided consumer: a
// REJECTED decision on a workflow staging lands as the parked run's
// terminal 'blocked' outcome. An approval keeps the run parked in
// requires_approval — the effect lands through redemption, not through
// this consumer — and a decision on a non-workflow approval matches no
// run row and is a normal no-op, so the consumer never needs to know
// which approvals are workflow stagings up front.
func (e *WorkflowEngine) HandleApprovalDecided(ctx context.Context, env kevents.Envelope) error {
	if env.Type != "approval.decided" {
		return nil
	}
	var payload struct {
		Verdict string `json:"verdict"`
	}
	if len(env.Payload) > 0 {
		if err := json.Unmarshal(env.Payload, &payload); err != nil {
			return fmt.Errorf("crmagents: approval.decided payload: %w", err)
		}
	}
	if payload.Verdict != "rejected" {
		return nil
	}
	approvalID := ids.From[ids.ApprovalKind](env.Entity.ID)
	// This consumer's workspace is its handle's; the envelope carries none.
	ws, err := e.db.Workspace(ctx)
	if err != nil {
		return err
	}
	wsCtx := principal.WithWorkspaceID(ctx, ws.UUID)
	return e.MarkRunBlocked(wsCtx, approvalID,
		"approval "+approvalID.String()+" was rejected by the deciding human")
}

// MarkRunBlocked lands the terminal 'blocked' outcome (with its reason)
// on the run parked behind one staged approval, matching on the
// approval_id field the Apply path stamped into detail — never on the
// whole reason string, so a wording change can never break the match.
// Approval expiry has no bus signal today (expiry is computed lazily at
// read time, never swept) — an expiry sweeper, when one exists, records
// its outcome through this same entry point with an "expired" reason.
// Idempotent: only a still-parked run flips, so a redelivered decision
// changes nothing.
func (e *WorkflowEngine) MarkRunBlocked(ctx context.Context, approvalID ids.ApprovalID, reason string) error {
	detail, err := reasonDetail(reason)
	if err != nil {
		return fmt.Errorf("automation: encoding the blocked reason: %w", err)
	}
	return e.db.Tx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			UPDATE workflow_run SET status = 'blocked', detail = $2
			WHERE status = 'requires_approval' AND detail->>'approval_id' = $1`,
			approvalID.String(), detail)
		return err
	})
}

// CompleteApprovedRunTx lands the terminal 'applied' outcome on the run parked
// behind one released approval — the mirror of MarkRunBlocked's rejection arm,
// and the half that until now did not exist.
//
// Without it an APPROVED staging left its run reading requires_approval
// forever: the rejection consumer terminated one verdict and nothing terminated
// the other, so run history showed a firing still waiting for a decision a
// human had already given, and the effect it authorized had already run.
//
// It takes the CALLER's transaction, and that is the whole point. The release
// redeems the approval and performs its effect in one transaction; the run
// transition belongs in that same commit, or a crash between them recreates the
// permanently-parked run this exists to prevent — with the message already
// sent. There is no reconciler to lean on, so the commit boundary is the
// guarantee.
//
// Idempotent by predicate, exactly like MarkRunBlocked: only a still-parked run
// flips, so a redelivered or re-driven release changes nothing. Matching on the
// approval_id field rather than a status string means a run that was blocked or
// completed by another path is simply not found, which is the correct no-op.
// A package function rather than a method: it reads no engine state and needs
// no handle, and the release that drives it runs on the approvals decision path
// where no engine exists. Constructing one purely to reach a transition would
// be a dependency that exists to satisfy a receiver.
func CompleteApprovedRunTx(ctx context.Context, tx pgx.Tx, approvalID ids.ApprovalID) error {
	if _, err := tx.Exec(ctx, `
		UPDATE workflow_run SET status = 'applied'
		WHERE status = 'requires_approval' AND detail->>'approval_id' = $1`,
		approvalID.String()); err != nil {
		return fmt.Errorf("automation: completing the run a released approval unparked: %w", err)
	}
	return nil
}
