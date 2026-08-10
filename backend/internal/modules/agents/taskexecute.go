// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// What a poll actually does, which is where this extension earns its keep.
//
// A tasks/get is an authenticated request, so when it finds the approval
// released it performs the effect THERE — under the polling passport, through
// the same Registry.Invoke the agent's own retry would take. Admission, object
// RBAC, row scope, the version pin, the volume counters and the retry claim all
// apply because it is the same path, not because this file re-checks them.
//
// Nothing runs between requests, and that is what makes the two obligations
// this track owes structural rather than defended: a cancelled task cannot have
// writes still landing, because no writer exists outside a request; and a
// passport that has been revoked never reaches here, because the transport
// answers its poll with a 401 before dispatch is entered.
//
// WHAT IS EXECUTED IS THE APPROVAL'S PAYLOAD, NOT THE AGENT'S. A human may edit
// a staged proposal before releasing it (ADR-0036 §4), and doing so rewrites
// both proposed_change and diff_hash — "the original hash no longer opens
// anything", in the approvals module's own words. Replaying the arguments the
// agent sent would therefore perform what the agent asked for rather than what
// the person allowed, and would fail redemption for saying so.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// advance answers a tasks/get: it reports the task, and moves it if the
// decision it waits on has landed.
func (s *Dispatcher) advance(ctx context.Context, task Task) map[string]any {
	if task.Status.terminal() {
		return s.serveRecorded(ctx, task)
	}
	state, err := s.taskApprovals.State(ctx, task.ApprovalID)
	if err != nil {
		// The decision could not be read, which is not the same as there being
		// no decision — so the task keeps saying `working`, which is the answer
		// that costs a client one more poll rather than a wrong terminal state.
		s.log.Error("mcp: reading a task's approval state failed",
			"task", task.ID, "approval", task.ApprovalID, "err", err)
		return taskWire(task, s.now())
	}
	switch state.Decided {
	case ApprovalPending:
		return taskWire(task, s.now())
	case ApprovalApproved:
		return s.runReleased(ctx, task)
	case ApprovalRejected:
		// A person said no. That is a RESULT — the surface answered the call —
		// so it completes with an isError result rather than failing, which the
		// specification reserves for a protocol fault.
		return s.settle(ctx, task, Settlement{
			Status:        TaskCompleted,
			StatusMessage: taskRejectedMessage,
			Result:        mustMarshalToolError(taskRejectedMessage),
		})
	case ApprovalExpired:
		return s.settle(ctx, task, Settlement{Status: TaskCancelled, StatusMessage: taskExpiredMessage})
	default:
		s.log.Error("mcp: unknown approval decision on a task", "task", task.ID, "decision", state.Decided)
		return taskWire(task, s.now())
	}
}

// runReleased performs the approved call, once.
//
// The claim decides which of three things this poll is. A poll that loses the
// claim executes nothing and says `working`, which is true. A poll that WINS a
// claim somebody already held is the interrupted case: a previous execution
// took the task and never recorded an outcome, so its effect may or may not
// have committed — and since the approval is single-use, running again would
// either be refused or, worse, perform a second act on one human yes.
func (s *Dispatcher) runReleased(ctx context.Context, task Task) map[string]any {
	claim, err := s.tasks.Claim(ctx, task.ID, taskClaimLease)
	if err != nil {
		s.log.Error("mcp: claiming a task for execution failed", "task", task.ID, "err", err)
		return taskWire(task, s.now())
	}
	if !claim.Won {
		return taskWire(task, s.now())
	}
	if claim.Reclaimed {
		s.log.Error("mcp: a task was re-claimed after an execution that recorded no outcome",
			"task", task.ID, "approval", task.ApprovalID)
		return s.settle(ctx, task, s.interrupted())
	}
	// Only now is "already consumed" meaningful. Asked BEFORE the claim it
	// cannot tell an approval spent elsewhere from one being spent by the poll
	// executing this very task — and a losing poll would then declare the task
	// interrupted while the winner was still committing it.
	if s.approvalConsumed(ctx, task) {
		s.log.Warn("mcp: a task's approval was consumed outside it",
			"task", task.ID, "approval", task.ApprovalID)
		return s.settle(ctx, task, s.interrupted())
	}
	return s.settle(ctx, task, s.invokeReleased(ctx, task))
}

// interrupted is the settlement for a task whose approval was spent by
// something this poll cannot see the outcome of. It says so rather than
// guessing, because the two guesses are "nothing happened" (which would invite
// a repeat of an irreversible act) and "it worked" (which would report a result
// nobody has).
func (s *Dispatcher) interrupted() Settlement {
	return Settlement{
		Status:        TaskFailed,
		StatusMessage: taskInterruptedMessage,
		Error:         mustMarshalRPCError(taskInterruptedMessage),
	}
}

// invokeReleased runs the released call and turns its outcome into a
// settlement.
//
// EVERY outcome of the call itself is `completed`, refusals included: the tool
// surface answered, and the specification is explicit that a result carrying
// isError belongs to a completed task. That covers the two refusals a released
// call most often meets — the fifteen-minute redemption window having closed,
// and the target row having changed since the person saw it — and both of them
// reach the agent as the same actionable sentence a direct retry would get.
//
// `failed` is kept for the one case where no call was made at all.
func (s *Dispatcher) invokeReleased(ctx context.Context, task Task) Settlement {
	args, err := releasedArgs(ctx, s.taskApprovals, task.ApprovalID)
	if err != nil {
		// The staged payload could not be rebuilt into a call. Nothing ran, and
		// nothing the agent can do changes that, so it is a fault rather than a
		// refusal.
		s.log.Error("mcp: rebuilding a released call failed",
			"task", task.ID, "approval", task.ApprovalID, "err", err)
		const message = "The approved change could not be turned back into a call, so nothing was done. " +
			"Report this to the workspace admin rather than retrying."
		return Settlement{
			Status:        TaskFailed,
			StatusMessage: message,
			Error:         mustMarshalRPCError(message),
		}
	}
	out, records, err := s.registry.InvokeServing(ctx, task.Tool, args)
	if err != nil {
		if errors.Is(err, apperrors.ErrApprovalTokenInvalid) && s.approvalConsumed(ctx, task) {
			// The approval was consumed between this poll's state read and its
			// redemption: a direct retry carrying the same approval_id got there
			// first. Reporting the redemption refusal would tell the agent its
			// call was rejected when the effect may well have landed.
			return s.interrupted()
		}
		// A refusal hands over no records, so it carries no ServedRecords and
		// every later poll may serve it as it stands.
		explained := s.explain(task.Tool, err)
		return Settlement{Status: TaskCompleted, StatusMessage: explained, Result: mustMarshalToolError(explained)}
	}
	// The ENVELOPE is what is stored, not the rendered result. It is the document
	// that names the records this answer handed over, and naming them is what
	// lets a later poll re-prove the caller may still see them — the same
	// evidence an idempotency replay walks. Rendering happens at serve time.
	return Settlement{
		Status:        TaskCompleted,
		StatusMessage: taskCompletedMessage,
		Result:        out,
		ServedRecords: &records,
	}
}

// serveRecorded answers a task that has already settled.
//
// A RECORDED ANSWER IS A RECEIPT THAT OUTLIVES THE AUTHORITY IT WAS PRODUCED
// UNDER, and this is the second door onto one — the idempotency claim was the
// first. Handing the bytes back unchecked would keep paying out records to a
// caller whose grant, seat or row scope has since been pulled, and would keep
// serving pre-erasure names out of a snapshot every live read now refuses. It
// would also be the cheapest bulk read on the surface: free, and repeatable for
// the life of the handle.
//
// So a stored ENVELOPE is re-proven and re-charged through the registry's own
// replay gate before it is rendered. A stored refusal is not: it names no
// records, so there is nothing to re-prove and nothing to bill.
func (s *Dispatcher) serveRecorded(ctx context.Context, task Task) map[string]any {
	if task.ServedRecords == nil {
		return taskWire(task, s.now())
	}
	// An answer that handed over NO records is served as it stands, and this is
	// the one place the task door parts from the idempotency door on purpose.
	//
	// That door refuses an evidence-free envelope because it cannot tell "this
	// answer carried no records" from "this answer's evidence is missing", and
	// serving on a parse-shaped doubt is what its gate exists to stop. Here the
	// count is a POSITIVE fact recorded at execution time by the same pass that
	// collects the evidence: zero means nothing left through the seam, so there
	// is nothing to re-prove and nothing to re-charge. It is reachable —
	// send_email answers an activity id and a status, naming no record — and
	// refusing it would tell an agent its receipt was withdrawn for lost access
	// that never existed.
	if *task.ServedRecords == 0 {
		return taskWire(s.rendered(task), s.now())
	}
	proven, err := s.registry.ServeRecorded(ctx, task.Tool, task.Result, *task.ServedRecords)
	if err != nil {
		// WHY it could not be served decides what the agent should do, and the
		// three causes differ: a record it may no longer read is permanent and
		// says so, while a read bound it has exhausted clears with the window
		// and a store that hiccuped clears on the next poll. Telling the last
		// two "do not repeat the call" would strand a document they could have
		// had — so only the existence-hiding answer keeps the withheld wording.
		s.log.Warn("mcp: a completed task's recorded answer could not be served",
			"task", task.ID, "tool", task.Tool, "err", err)
		message := s.explain(task.Tool, err)
		if errors.Is(err, apperrors.ErrNotFound) {
			message = taskWithheldMessage
		}
		withheld := task
		withheld.ServedRecords = nil
		withheld.Result = mustMarshalToolError(message)
		return taskWire(withheld, s.now())
	}
	task.Result = proven
	return taskWire(s.rendered(task), s.now())
}

// approvalConsumed reports whether this task's single-use authority has already
// been spent.
//
// It is asked TWICE, and the two moments are different questions. After winning
// the claim it means "somebody else spent it" — a direct retry, or a poll that
// died after redeeming. After a redemption REFUSAL it closes the window between
// those two points, which is real: a direct retry can consume the approval
// inside it, and reporting the refusal would tell the agent its call was
// rejected when the effect may have landed.
//
// A caller must filter its own refusals before asking: only a token refusal is
// worth re-asking about, because row scope, version skew and the fifteen-minute
// window closing are all the honest answer to a call that genuinely did not
// happen.
func (s *Dispatcher) approvalConsumed(ctx context.Context, task Task) bool {
	state, err := s.taskApprovals.State(ctx, task.ApprovalID)
	if err != nil {
		s.log.Error("mcp: re-reading an approval to see whether it was already spent failed",
			"task", task.ID, "approval", task.ApprovalID, "err", err)
		return false
	}
	return state.Consumed
}

// settle records a terminal state and answers the task as settled.
//
// A settlement that cannot be WRITTEN is still answered, and the asymmetry is
// deliberate: the effect has already happened, so this poll is the one chance
// to tell its caller the truth. What the unrecorded row costs is a later poll,
// which finds the claim held and no outcome and reports the interrupted answer
// — honest about not knowing rather than confidently wrong.
func (s *Dispatcher) settle(ctx context.Context, task Task, settlement Settlement) map[string]any {
	settled, err := s.tasks.Settle(ctx, task.ID, settlement)
	if err != nil {
		s.log.Error("mcp: settling a task failed", "task", task.ID, "status", settlement.Status, "err", err)
		settled = task
		settled.Status = settlement.Status
		settled.StatusMessage = settlement.StatusMessage
		settled.Result, settled.Error = settlement.Result, settlement.Error
		settled.ServedRecords = settlement.ServedRecords
		settled.UpdatedAt = s.now()
	}
	// A settlement can LOSE: the store answers an already-terminal task with the
	// stored row rather than this caller's settlement. That row is somebody
	// else's answer, so it is a read like any other — rendered() would hand its
	// records over on the strength of a call this request never made, unproven
	// and uncharged. Only a settlement that carries its own envelope is this
	// request's own work.
	if settled.ServedRecords != nil && settlement.ServedRecords == nil {
		return s.serveRecorded(ctx, settled)
	}
	return taskWire(s.rendered(settled), s.now())
}

// rendered turns a task whose Result is a stored ENVELOPE into one whose Result
// is the tool result a client reads.
//
// It is the fresh half of the pair serveRecorded completes. This one neither
// re-proves nor re-charges, and both omissions are the point: the call has just
// run under this very request, so its records were proven by the handler's own
// row scope and charged by runAndSeal. Charging again here would bill a caller
// twice for one answer.
func (s *Dispatcher) rendered(task Task) Task {
	if task.ServedRecords == nil {
		return task
	}
	out, err := json.Marshal(s.result(task.Tool, task.Result))
	if err != nil {
		// The effect COMMITTED and its answer cannot be rendered. Saying that
		// beats both alternatives: reporting a failure would tell the agent
		// nothing happened, and re-running is impossible on a spent approval.
		s.log.Error("mcp: rendering a task's result failed", "task", task.ID, "err", err)
		task.Result = mustMarshalToolError(taskWithheldMessage)
		return task
	}
	task.Result = out
	return task
}

// releasedArgs rebuilds the call a human released: the approval's CURRENT
// proposed change, plus the approval id that redeems it.
//
// The payload is read fresh rather than remembered on the task, because an
// edited approval carries a different one under a different hash — and a call
// built from what the agent originally sent would both perform the wrong change
// and be refused for it.
//
// approval_id is SET, never merged: it is a member the surface owns, so a
// payload that somehow carried one has no say over which approval this call
// redeems.
func releasedArgs(ctx context.Context, approvals TaskApprovals, approvalID ids.ApprovalID) (json.RawMessage, error) {
	change, err := approvals.ProposedChange(ctx, approvalID)
	if err != nil {
		return nil, fmt.Errorf("reading the approved change: %w", err)
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(change, &members); err != nil {
		return nil, fmt.Errorf("the approved change is not a JSON object: %w", err)
	}
	if members == nil {
		// A literal JSON null decodes into a nil map without error, and would
		// reach Invoke as an argument-less call.
		return nil, fmt.Errorf("the approved change is a JSON null, not an object")
	}
	id, err := json.Marshal(approvalID.String())
	if err != nil {
		return nil, fmt.Errorf("encoding the approval id: %w", err)
	}
	members[approvalIDArg] = id
	return json.Marshal(members)
}

// mustMarshalToolError renders a refusal as the tool result a completed task
// carries. The input is this package's own prose, so the encode cannot fail on
// any value reachable here; if it somehow did, an empty result would be a
// completed task that says nothing, and this says something.
func mustMarshalToolError(message string) json.RawMessage {
	raw, err := json.Marshal(toolError(message))
	if err != nil {
		return json.RawMessage(`{"isError":true,"content":[]}`)
	}
	return raw
}

// mustMarshalRPCError renders the JSON-RPC error a failed task carries, in the
// wire shape a client already parses errors in.
//
// The code is always the internal one, and takes no parameter for that reason:
// every way a task can FAIL is this server being unable to carry out something
// it accepted. A refusal the caller could act on is not a failed task at all —
// the specification puts it in a completed one, as an isError result.
func mustMarshalRPCError(message string) json.RawMessage {
	raw, err := json.Marshal(rpcError{Code: codeInternalError, Message: message})
	if err != nil {
		return json.RawMessage(`{"code":-32603,"message":"internal error"}`)
	}
	return raw
}

// now is this dispatcher's reading of the wall clock, used only for the
// freshness a rendered task reports.
//
// It is NOT injectable, and deliberately so: the computation a test needs to
// pin is taskWire's, which already takes `now` as a parameter and is asserted
// against a fixed one. A second seam here would be a field nothing sets.
func (s *Dispatcher) now() time.Time { return time.Now() }
