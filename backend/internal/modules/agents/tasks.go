// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// MCP Tasks (io.modelcontextprotocol/tasks) for the one operation on this
// surface that genuinely waits: a human decision.
//
// WHAT IT IS FOR. A confirm-first (🟡) call is refused and told to come back
// with an approval_id. The agent is never told when — or whether — the person
// decided, so it either gives up on an effect the human released or re-issues
// the call on a guess. A task turns that dead end into a durable handle: the
// call answers with a taskId, and the client polls until the decision lands.
//
// A TASK IS NEVER AUTHORITY, which is the whole reason this shape was chosen
// over a background executor. tasks/get is an authenticated MCP request like
// any other, so when a poll finds the approval approved the effect runs INSIDE
// that poll, under the polling request's own live passport, through the same
// Registry.Invoke every other call takes. Nothing executes between requests.
// That is what makes the two obligations structural rather than defended:
// a cancelled task cannot have writes continuing because nothing is running,
// and a revoked passport stops its own task by failing its own next bind.
//
// The id carries no permission either. Every task method requires the
// presenting passport to match the one the task was created for, and a
// mismatch answers exactly what an unknown id answers.
//
// WHERE THE ROWS LIVE. Not here. This module declares the seam and compose
// implements it over the agent_task table, the same split idempotency.go uses
// — the flat tool-surface package owns no SQL.

import (
	"context"
	"encoding/json"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// TaskStatus is the lifecycle a client sees. These are the specification's own
// values and the ONLY ones stored: an internal vocabulary mapped onto these at
// the wire would be a second answer to "what is this task doing", and the two
// would eventually disagree. Work in flight is a claim timestamp, not a status.
type TaskStatus string

const (
	// TaskWorking means the human has not decided yet.
	TaskWorking TaskStatus = "working"
	// TaskCompleted means the tool returned a result — including a result that
	// refuses. A human declining is not a protocol fault, and the specification
	// is explicit that isError belongs to a completed task.
	TaskCompleted TaskStatus = "completed"
	// TaskFailed means a JSON-RPC error was raised during execution, which on
	// this surface means the effect could not be carried out at all.
	TaskFailed TaskStatus = "failed"
	// TaskCancelled means the staging was withdrawn before anyone decided.
	TaskCancelled TaskStatus = "cancelled"
)

// terminal reports whether a status can still change. The specification makes
// these three immutable, which is what lets a second poll be answered from the
// stored row instead of by executing anything again.
func (s TaskStatus) terminal() bool {
	return s == TaskCompleted || s == TaskFailed || s == TaskCancelled
}

// Task is one durable handle: the staged decision it waits on, and whatever
// terminal answer has been recorded for it.
type Task struct {
	ID            ids.UUID
	ApprovalID    ids.ApprovalID
	Tool          string
	Status        TaskStatus
	StatusMessage string
	// Result is the tool result a completed task answers with, and Error the
	// JSON-RPC error a failed one carries. Exactly one is set, and only on the
	// status that owns it — the table asserts that with a CHECK, because a
	// completed task with no result is a task whose second poll has nothing to
	// say after its first said "completed".
	Result json.RawMessage
	Error  json.RawMessage
	// ExpiresAt is when this server stops answering for the task. It tracks the
	// approval's own window rather than a constant: a handle that outlived the
	// decision it points at would poll forever against something nobody can
	// release.
	ExpiresAt time.Time
	CreatedAt time.Time
	UpdatedAt time.Time
}

// TaskClaim is what one attempt to take a task for execution decided.
type TaskClaim struct {
	// Won is false when another poll holds the task; that caller is executing
	// and this one must report `working` rather than run a second time.
	Won bool
	// Reclaimed means this caller took a claim somebody else had already made
	// and never settled. It is the interrupted case, and it is knowledge only
	// this row has: the approval cannot distinguish "consumed by a poll that
	// died" from "consumed by a direct call", but a claim that outlived its
	// lease with no recorded outcome can only be the former.
	Reclaimed bool
}

// NewTask is what creating one needs. The passport is taken from the creating
// request's principal by the store, never passed in: it is the binding that
// makes the id useless to anyone else, and a caller-supplied one could be
// wrong.
type NewTask struct {
	ApprovalID    ids.ApprovalID
	Tool          string
	StatusMessage string
	ExpiresAt     time.Time
}

// Settlement is the terminal state one execution reached.
type Settlement struct {
	Status        TaskStatus
	StatusMessage string
	Result        json.RawMessage
	Error         json.RawMessage
}

// Tasks is the durable half of the extension, implemented by compose.
//
// Every method is scoped to the CALLING principal's passport by the
// implementation, so no method here takes one. A task belonging to another
// passport is not a permission error but an absent one — the same
// existence-hiding answer the row-scope rule gives everywhere else on this
// surface, because "that task exists but is not yours" is an oracle.
type Tasks interface {
	// Create durably records a working task and answers it. It must not return
	// until a Load for the same id would resolve: the specification forbids
	// handing back a handle that is not yet gettable, so a client never has to
	// poll speculatively for its own task to appear.
	Create(ctx context.Context, in NewTask) (Task, error)
	// Load answers this passport's task, or apperrors.ErrNotFound.
	Load(ctx context.Context, id ids.UUID) (Task, error)
	// Claim takes a working task for execution and reports whether this caller
	// won it. Two polls arriving together must not both execute; the loser is
	// told the truth, which is that the task is still working.
	//
	// A claim older than lease may be taken again, because a process that died
	// mid-execution would otherwise strand the task forever. That retry is safe
	// without being idempotent by itself: redemption is single-use, so a second
	// execution of an already-performed effect is refused by the approval
	// rather than by this lease.
	Claim(ctx context.Context, id ids.UUID, lease time.Duration) (TaskClaim, error)
	// Settle records a terminal state and answers the task as it now stands. A
	// task already terminal is left alone and answered unchanged — the
	// specification makes a terminal state immutable, and the honest place to
	// enforce that is the one write that could break it.
	Settle(ctx context.Context, id ids.UUID, s Settlement) (Task, error)
}

// TaskApprovals is what POLLING a decision needs, as distinct from staging or
// redeeming one.
//
// It is its own seam rather than three more methods on Approvals, because the
// two are used at different moments by different callers: Approvals is the
// staging/redemption dependency both doors share, and this is read by the one
// door that hands out handles. One implementation satisfies both — the compose
// adapter over the approvals service — so there is still a single answer to
// "what has this human decided"; what is separate is who has to be able to ask.
type TaskApprovals interface {
	// State answers whether a human has decided yet, for a poll that must report
	// progress without acting. It is a READ of the authority object and settles
	// nothing: the decision it reports still has to be redeemed.
	State(ctx context.Context, approvalID ids.ApprovalID) (ApprovalState, error)
	// ProposedChange answers the payload a redemption would perform — the
	// human's edit where there was one, the agent's proposal otherwise.
	//
	// A caller rebuilding a released call reads it HERE rather than remembering
	// what it staged. An edit rewrites both the payload and the hash that opens
	// it (ADR-0036 §4), so a call rebuilt from the original arguments would
	// perform what the agent asked for instead of what the person allowed — and
	// would be refused for the mismatch, which is the only reason that bug would
	// ever be noticed.
	ProposedChange(ctx context.Context, approvalID ids.ApprovalID) (json.RawMessage, error)
	// Withdraw takes a live pending proposal off the inbox, so a cancelled task
	// leaves behind no decision that could no longer take effect. Withdrawing an
	// already-decided approval changes nothing and is not an error: the caller
	// asked for the proposal to be gone, and it is.
	Withdraw(ctx context.Context, approvalID ids.ApprovalID) error
}

// ApprovalState is the live decision state of a staged approval, read through
// the TaskApprovals seam because this module never reaches a sibling's tables.
type ApprovalState struct {
	// Decided distinguishes the three outcomes a poll must tell apart. Pending
	// and expired are NOT the same answer: a pending approval is still worth
	// waiting on, and an expired one never will be.
	Decided ApprovalDecision
	// ExpiresAt is the window the task's own ttlMs is derived from.
	ExpiresAt time.Time
}

// ApprovalDecision is what the human has done so far.
type ApprovalDecision string

const (
	// ApprovalPending means nobody has decided.
	ApprovalPending ApprovalDecision = "pending"
	// ApprovalApproved means the effect may now be performed, once.
	ApprovalApproved ApprovalDecision = "approved"
	// ApprovalRejected means a person said no. It is remembered so a task does
	// not report a refusal as though the tool had merely failed.
	ApprovalRejected ApprovalDecision = "rejected"
	// ApprovalExpired means the window closed undecided — which is also what a
	// withdrawal reads as, since withdrawal is forced expiry.
	ApprovalExpired ApprovalDecision = "expired"
)

// The copy a task carries. It is the model's only instruction, so each line
// says what happened, what has NOT happened, and what to do next — an agent
// that reads "needs approval" and stops has stranded the very effect the human
// is about to release.
const (
	taskCreatedMessage = "A person must approve this before it takes effect. Nothing has changed yet. " +
		"Poll tasks/get with this taskId; it completes when they decide."
	taskRejectedMessage = "A person declined this. Nothing was changed, and repeating the call will be " +
		"declined the same way — tell the user rather than retrying."
	taskExpiredMessage = "Nobody decided this before the approval window closed, so it will never take " +
		"effect. Nothing was changed. Ask the user whether to propose it again."
	taskCancelledMessage = "This task was cancelled and its pending approval withdrawn. Nothing was changed."
	taskCompletedMessage = "A person approved this and it has now been carried out."
	// taskInterruptedMessage is the one answer this surface cannot make
	// definite, so it says exactly that. It is reached when the approval was
	// already consumed by an execution this task has no recorded result for: an
	// earlier poll that died after redeeming, or the agent redeeming the same
	// approval through a direct call. Guessing either way would be a lie, and
	// re-running would risk a second effect from one human yes.
	taskInterruptedMessage = "The approval for this call was consumed by an attempt that recorded no result, " +
		"so whether the effect was carried out is not known here. Do not repeat the call — read the record, " +
		"or the workspace audit log, to see what committed."
)

// taskPollIntervalMs is what a client is asked to wait between polls. A human
// approval takes minutes, so this is already generous; one poll costs a
// passport re-authentication and a single indexed row read.
const taskPollIntervalMs = 5000

// taskClaimLease is how long one execution may hold a task before another poll
// may take it. It is well past any tool's own wall clock, so it is reached only
// by a process that died rather than by one still working.
const taskClaimLease = 2 * time.Minute
