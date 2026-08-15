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
	"slices"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	kevents "github.com/gradionhq/margince/backend/internal/shared/kernel/events"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/workflow"
)

// HandleApprovalDecided is the engine-side approval.decided consumer: it turns a
// decision into the parked run's terminal outcome. A decision on a non-workflow
// approval matches no run row and is a normal no-op, so the consumer never needs
// to know which approvals are workflow stagings up front.
//
// Which outcome depends on the verdict AND the kind, because approving is not
// one thing. For a kind whose whole effect is the asking (AskingOnlyKinds —
// request_approval), the human's yes is the last thing that had to happen, so
// the run completes here. For a kind that proposes a write, the run completes
// inside the transaction its release executor performs the write in, and this
// consumer leaves it alone: completing it here would race that executor and
// report as done a write that might still fail.
//
// TWO verdicts refuse, and the second one is why the run stops waiting at all.
// A human rejecting says no; a closed window says no on nobody's behalf
// ("unactioned means rejected", APPR-PARAM-1). The expiry sweep writes that
// verdict in the same transaction as the approval and emits this event through
// the outbox, so the run is ended by the same delivery guarantee a rejection
// has — rather than by a second loop somewhere that has to be kept in step.
// Without the expired arm a staging nobody answered left its run in
// requires_approval permanently, which AUTO-AC-10 expects to see as blocked.
func (e *WorkflowEngine) HandleApprovalDecided(ctx context.Context, env kevents.Envelope) error {
	if env.Type != "approval.decided" {
		return nil
	}
	var payload struct {
		Verdict string `json:"verdict"`
		Kind    string `json:"kind"`
	}
	if len(env.Payload) > 0 {
		if err := json.Unmarshal(env.Payload, &payload); err != nil {
			return fmt.Errorf("crmagents: approval.decided payload: %w", err)
		}
	}
	reason, refused := runBlockedReasonFor(payload.Verdict)
	completes := payload.Verdict == "approved" && slices.Contains(AskingOnlyKinds(), payload.Kind)
	if !refused && !completes {
		return nil
	}
	approvalID := ids.From[ids.ApprovalKind](env.Entity.ID)
	// This consumer's workspace is its handle's; the envelope carries none.
	ws, err := e.db.Workspace(ctx)
	if err != nil {
		return err
	}
	wsCtx := principal.WithWorkspaceID(ctx, ws.UUID)
	if completes {
		return CompleteApprovedRun(wsCtx, e.db, approvalID)
	}
	return e.MarkRunBlocked(wsCtx, approvalID, "approval "+approvalID.String()+" "+reason)
}

// CompleteApprovedRun lands the terminal 'applied' outcome on a parked run in a
// transaction of its own — CompleteApprovedRunTx for callers with no transaction
// to lend it.
//
// Two callers, and the difference between them is what each is allowed to
// promise. The decision consumer above uses it for an asking-only kind, where
// the human's yes IS the outcome and there is no write to join. A release
// executor uses it after its write has returned, because the record port it
// writes through owns its own transaction and cannot be joined — so the
// transition follows the write rather than sharing its commit, and a write that
// failed leaves the run parked instead of claiming success.
//
// Idempotent by predicate either way: only a still-parked run flips, so
// redelivery, retry, and a second executor pass all change nothing.
//
// A package function taking the handle, for the same reason CompleteApprovedRunTx
// is one: a release executor reaching it runs on the decision path, where no
// engine exists, and constructing one purely to reach a transition would be a
// dependency that exists to satisfy a receiver.
func CompleteApprovedRun(ctx context.Context, db *database.DB, approvalID ids.ApprovalID) error {
	return db.Tx(ctx, func(tx pgx.Tx) error {
		return CompleteApprovedRunTx(ctx, tx, approvalID)
	})
}

// runBlockedReasonFor maps a verdict onto the run outcome it produces, and says
// whether it produces one at all.
//
// A table rather than a condition, because the set is the decision: approved is
// absent on purpose (the effect lands through redemption, not here), and a
// verdict this build has not met yet blocks nothing rather than being guessed
// at. The reason is the human-facing half of the run record, so it says which
// kind of no it was — somebody reading run history needs to tell "a colleague
// declined this" from "nobody ever looked".
func runBlockedReasonFor(verdict string) (string, bool) {
	switch verdict {
	case "rejected":
		return "was rejected by the deciding human", true
	case "expired":
		return "expired with nobody deciding it", true
	default:
		return "", false
	}
}

// MarkRunBlocked lands the terminal 'blocked' outcome (with its reason)
// on the run parked behind one staged approval, matching on the
// approval_id field the Apply path stamped into detail — never on the
// whole reason string, so a wording change can never break the match.
// Both refusals arrive the same way: the expiry sweep writes an 'expired'
// verdict and emits approval.decided from the same transaction, so a window
// that closed reaches this entry point exactly as a rejection does.
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

// StageableKinds are the approval kinds a firing can put in front of a human.
//
// Exported so the composition layer can hold every one of them to a
// decision-grant mapping. That gate has existed for kinds with a registered
// effect, and these have none — an automation stages them and no compose
// executor runs on approval — so they were invisible to it. A kind missing from
// approvals' grant map is not merely inert: requireDecisionGrants fails closed,
// so its stagings are hidden from every inbox and cannot be decided by anyone,
// while the run that raised them waits forever.
//
// Derived from the switch that stages them (applyOne) rather than restated: a
// kind that starts staging and is not added here would reintroduce exactly the
// blind spot this exists to close, so the list and the switch are read
// together by TestEveryKindAnAutomationCanStageIsDecidable.
func StageableKinds() []string {
	return []string{
		string(workflow.ActionEmitFlowEvent),
		string(workflow.ActionAssignOwner),
		HeldDraftKind,
	}
}

// AskingOnlyKinds are the stageable kinds whose whole effect is the asking.
//
// There is exactly one, and it is not a category waiting to be filled:
// request_approval stages under emit_flow_event and its action IS the
// confirm-first act, so a human answering it leaves nothing further to run.
// Every OTHER stageable kind proposes a write, and approving one with no
// release executor would complete a run that did nothing — which is why the two
// sets are stated separately and checked against each other in composition
// (TestEveryStageableKindEitherAsksOnlyOrHasAReleaseEffect) rather than left for
// whoever adds the next kind to remember.
func AskingOnlyKinds() []string {
	return []string{string(workflow.ActionEmitFlowEvent)}
}
