// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package automation

// The 'blocked' terminal outcome (A72/ADR-0035 Am.1, migration 0061): a
// workflow run that staged a 🟡 approval and then saw it rejected is a
// finished run whose effect never happened — the history must say so,
// with which approval and why. The linkage rides the run row's detail
// column (workflow_run gained no approval_id column): the Apply path
// stamps stagedApprovalDetail(id) when it parks the run, and blocking
// matches on exactly that stamp.

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	kevents "github.com/gradionhq/margince/backend/internal/shared/kernel/events"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// stagedApprovalDetail is the ONE spelling of the run-row staging
// pointer: writer (runOne) and matcher (MarkRunBlocked) both call it, so
// the linkage can never drift apart.
func stagedApprovalDetail(id ids.ApprovalID) string {
	return "staged as approval " + id.String() + "; awaiting the human decision"
}

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
	wsCtx := principal.WithWorkspaceID(ctx, env.WorkspaceID)
	return e.MarkRunBlocked(wsCtx, approvalID,
		"approval "+approvalID.String()+" was rejected by the deciding human")
}

// MarkRunBlocked lands the terminal 'blocked' outcome (with its reason)
// on the run parked behind one staged approval. Approval expiry has no
// bus signal today (expiry is computed lazily at read time, never swept)
// — an expiry sweeper, when one exists, records its outcome through this
// same entry point with an "expired" reason. Idempotent: only a
// still-parked run flips, so a redelivered decision changes nothing.
func (e *WorkflowEngine) MarkRunBlocked(ctx context.Context, approvalID ids.ApprovalID, reason string) error {
	return database.WithWorkspaceTx(ctx, e.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			UPDATE workflow_run SET status = 'blocked', error = $2
			WHERE status = 'requires_approval' AND error = $1`,
			stagedApprovalDetail(approvalID), reason)
		return err
	})
}
