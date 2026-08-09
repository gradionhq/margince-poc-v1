// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// The three task methods, and how a task is rendered.
//
// They exist in the MODERN framing only, and that is a statement about the
// protocol rather than a restriction: the capability admitting them is a
// per-request `_meta` member, and the handshake era has no place to carry one.
// A legacy client asking for tasks/get is asking for a method that, in its era,
// does not exist — which is what -32601 says.
//
// Execution lives next door in taskexecute.go. The boundary is the same one
// dispatch.go and explain.go keep: this file decides what a client is TOLD, and
// that one decides what actually happens when a poll finds a released approval.

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/workflow"
)

// The extension's methods. notifications/tasks and subscriptions/listen are
// deliberately absent: the specification makes notifications optional and names
// polling the default, and a server that pushed status would owe a subscription
// stream this surface does not serve.
const (
	methodTasksGet    = "tasks/get"
	methodTasksUpdate = "tasks/update"
	methodTasksCancel = "tasks/cancel"
)

// resultTypeTask is the discriminator a CreateTaskResult carries. The
// specification reserves it to this extension and forbids it on any other
// result shape, which createTaskResult below is what enforces: it is the one
// type that answers it.
const resultTypeTask = "task"

// The members of the wire Task. taskWire is the one place that writes them;
// they are named rather than inlined so a reader can see the whole shape at
// once, and so the test that holds each to the specification's own spelling has
// something to hold.
const (
	fieldTaskID        = "taskId"
	fieldStatus        = "status"
	fieldStatusMessage = "statusMessage"
	fieldCreatedAt     = "createdAt"
	fieldLastUpdatedAt = "lastUpdatedAt"
	fieldPollInterval  = "pollIntervalMs"
	fieldResult        = "result"
	fieldError         = "error"
)

// createTaskResult is the answer a staged 🟡 call gives a task-capable client.
// It is a distinct type ONLY so it can claim its own resultType — which is also
// how the specification's "MUST NOT set resultType to task on other result
// types" is kept: no other result can answer it.
type createTaskResult map[string]any

func (createTaskResult) resultType() string { return resultTypeTask }

// taskParams is the sole parameter all three methods take.
type taskParams struct {
	//nolint:tagliatelle // taskId is the Tasks extension's own member name
	TaskID string `json:"taskId"`
}

// taskIDName reads the task a body names, for the Mcp-Name mirror. It decodes
// through the SAME shape the handlers do, which is the property that makes the
// mirror meaningful rather than decorative.
func taskIDName(params json.RawMessage) string {
	var p taskParams
	if err := json.Unmarshal(params, &p); err != nil {
		return ""
	}
	return p.TaskID
}

// taskMethod answers one of the three, after the two checks every one of them
// shares: the client declared the extension, and it named a task this passport
// may see.
func (s *Dispatcher) taskMethod(ctx context.Context, req rpcRequest, fr framing) rpcResponse {
	resp := rpcResponse{JSONRPC: jsonRPCVersion, ID: req.ID}
	if !fr.tasks {
		resp.Error = missingClientCapability(req.Method)
		return resp
	}
	if !s.tasksServed() {
		// A composition with no task store never hands out a handle, so there is
		// no id a client could be holding. Saying the method does not exist is
		// true of this server, and truer than an empty not-found.
		return methodNotFound(req)
	}
	callCtx, rpcErr := s.bindTaskCall(ctx, req.Method)
	if rpcErr != nil {
		resp.Error = rpcErr
		return resp
	}
	id, rpcErr := taskIDParam(req.Params)
	if rpcErr != nil {
		resp.Error = rpcErr
		return resp
	}
	task, err := s.tasks.Load(callCtx, id)
	if err != nil {
		resp.Error = s.taskLookupError(req.Method, err)
		return resp
	}
	return s.answerTaskMethod(callCtx, resp, req.Method, task)
}

// answerTaskMethod runs the method's own half, once the task is in hand.
func (s *Dispatcher) answerTaskMethod(ctx context.Context, resp rpcResponse, method string, task Task) rpcResponse {
	switch method {
	case methodTasksGet:
		resp.Result = s.advance(ctx, task)
	case methodTasksCancel:
		if result, rpcErr := s.cancelTask(ctx, task); rpcErr != nil {
			resp.Error = rpcErr
		} else {
			resp.Result = result
		}
	case methodTasksUpdate:
		// An empty acknowledgement, which is the whole of it. This server never
		// raises an inputRequest — a 🟡 decision is a person visiting Margince,
		// not a round trip back through the agent's client — so every key a
		// client could send is one the specification tells servers to ignore.
		resp.Result = map[string]any{}
	}
	return resp
}

// bindTaskCall authenticates the polling request, exactly as a tools/call is
// authenticated, so a task method is never a quieter door than the tool it
// completes.
//
// On the hosted transport this cannot fail: the handler answers 401 with the
// RFC 9728 challenge before dispatch is reached, and the binder there is a
// pass-through. So a failure here means a transport handed the dispatcher a
// context it never authenticated, which is this server's defect rather than the
// caller's — reported as one, with the cause kept server-side.
func (s *Dispatcher) bindTaskCall(ctx context.Context, method string) (context.Context, *rpcError) {
	bound, err := s.bind(ctx)
	if err != nil {
		s.log.Error("mcp: task method reached dispatch on an unauthenticated context",
			"method", method, "err", err)
		return nil, &rpcError{Code: codeInternalError, Message: "this request could not be authenticated"}
	}
	return bound, nil
}

// taskIDParam reads and parses the one parameter, refusing anything that is not
// a task id THIS server could have minted.
//
// A malformed id is refused with the same code and the same shape of message an
// unknown one gets, because they are the same fact from the client's side: no
// task is being named. Answering them differently would let a caller tell a
// well-formed id it does not own from a typo, which is the beginning of an
// enumeration oracle.
func taskIDParam(params json.RawMessage) (ids.UUID, *rpcError) {
	var p taskParams
	if err := json.Unmarshal(params, &p); err != nil {
		return ids.Nil, &rpcError{Code: codeInvalidParams, Message: "malformed task params: taskId must be a string"}
	}
	id, err := ids.Parse(p.TaskID)
	if err != nil {
		return ids.Nil, unknownTaskError()
	}
	return id, nil
}

// taskLookupError renders a failed Load. A task that is absent, expired, or
// another passport's is ONE answer — the specification's -32602 — and that is
// deliberate: distinguishing "not yours" from "not there" tells a caller which
// ids exist.
func (s *Dispatcher) taskLookupError(method string, err error) *rpcError {
	if errors.Is(err, apperrors.ErrNotFound) {
		return unknownTaskError()
	}
	s.log.Error("mcp: task lookup failed", "method", method, "err", err)
	return &rpcError{Code: codeInternalError, Message: "the task could not be read"}
}

func unknownTaskError() *rpcError {
	return &rpcError{
		Code: codeInvalidParams,
		Message: "no such task: it was never created by this passport, or it has expired and been " +
			"discarded. Do not poll it again.",
	}
}

// cancelTask retracts the proposal behind a task and settles the handle.
//
// Withdrawing is the point rather than a side effect. Cancelling the handle and
// leaving the staging in a person's inbox would leave a decision that can no
// longer take effect and that nobody can retract — the same zombie authority
// object refuseStagingElsewhere declines to mint.
//
// THE ORDER IS LOAD-BEARING, and it is safe for a reason worth stating: a
// withdrawal only ever touches a PENDING approval, and an executing poll only
// ever holds an APPROVED one. So retracting first cannot disturb an execution
// in flight — against an approved proposal it is a no-op that reports exactly
// that.
//
// Cancel then competes with those executors for the same row, and losing is not
// a failure. The specification makes cancellation cooperative and permits a
// task to reach a terminal state other than cancelled; a poll already inside
// the released call will finish it, and the client learns that by polling. What
// this must never do is mark a task cancelled while its effect is committing,
// which is why the store's Cancel refuses a claimed row instead of this
// function reading the status and hoping.
//
// A task already terminal is answered untouched, for the same reason.
func (s *Dispatcher) cancelTask(ctx context.Context, task Task) (map[string]any, *rpcError) {
	if task.Status.terminal() {
		return map[string]any{}, nil
	}
	retracted, err := s.taskApprovals.Withdraw(ctx, task.ApprovalID)
	if err != nil {
		// Answered as an error rather than acked. An empty ack would tell the
		// client its cancellation succeeded while the proposal stayed live in a
		// person's inbox — and the client would stop polling the very task that
		// could still act.
		s.log.Error("mcp: withdrawing a cancelled task's approval failed",
			"task", task.ID, "approval", task.ApprovalID, "err", err)
		return nil, &rpcError{
			Code: codeInternalError,
			Message: "the approval behind this task could not be withdrawn, so it was not cancelled: " +
				"try again, or ask the user to decide it in Margince",
		}
	}
	message := taskCancelledMessage
	if !retracted {
		message = taskCancelledLateMessage
	}
	cancelled, err := s.tasks.Cancel(ctx, task.ID, taskClaimLease, message)
	if err != nil {
		// Answered as an error for the same reason the withdrawal above is: an
		// empty ack tells the client its cancellation succeeded, and a client
		// that believes that stops polling — leaving a task nothing will ever
		// settle. Losing the RACE to an executor is different and returns nil:
		// there the task does reach a terminal state, just not this one.
		s.log.Error("mcp: recording a cancelled task failed", "task", task.ID, "err", err)
		return nil, &rpcError{
			Code: codeInternalError,
			Message: "the cancellation could not be recorded, so this task was not cancelled: " +
				"poll it again, or try cancelling once more",
		}
	}
	if !cancelled {
		// A poll holds the claim and is inside the released call, or the task
		// has already settled. The ack stays empty and claims nothing — the
		// client learns the real terminal state by polling, which is what the
		// specification means by cooperative.
		s.log.Info("mcp: a cancelled task was already executing or settled",
			"task", task.ID, "status", task.Status)
	}
	return map[string]any{}, nil
}

// mintTask turns a staged 🟡 refusal into a handle the caller can poll, and
// reports whether it did.
//
// It mints for exactly one refusal — a call the gate sent to a human, which is
// the only thing on this surface that WAITS. A step-up on a volume counter is
// deliberately not one of them: it stages a question too, but releasing it
// widens a counter rather than performing the call, so a task that completed on
// release would report an effect that never happened. That exclusion is
// structural rather than a check here — StepUpStagedError is its own type and
// does not unwrap to the confirm-first sentinel.
//
// Every failure below falls back to the plain refusal, which is the answer the
// client would have got anyway. A handle this server could not create is worth
// nothing; the sentence telling the agent a person must approve is worth
// something.
func (s *Dispatcher) mintTask(ctx context.Context, fr framing, tool string, refusal error) (createTaskResult, bool) {
	var staged *workflow.StagedApprovalError
	if !fr.tasks || !s.tasksServed() || !errors.As(refusal, &staged) {
		return nil, false
	}
	state, err := s.taskApprovals.State(ctx, staged.ApprovalID)
	if err != nil {
		s.log.Error("mcp: reading a freshly staged approval failed",
			"tool", tool, "approval", staged.ApprovalID, "err", err)
		return nil, false
	}
	task, err := s.tasks.Create(ctx, NewTask{
		ApprovalID:    staged.ApprovalID,
		Tool:          tool,
		StatusMessage: taskCreatedMessage,
		ExpiresAt:     state.ExpiresAt,
	})
	if err != nil {
		s.log.Error("mcp: creating a task for a staged call failed",
			"tool", tool, "approval", staged.ApprovalID, "err", err)
		return nil, false
	}
	return createTaskResult(taskWire(task, s.now())), true
}

// taskWire renders a task in the shape the specification's Task carries.
//
// ttlMs is derived from the row's own expiry rather than fixed, and is never
// null: an unlimited task is one this server would have to retain forever. It
// floors at zero rather than going negative, because a negative freshness is
// not a thing a client can act on.
func taskWire(t Task, now time.Time) map[string]any {
	wire := map[string]any{
		fieldTaskID:        t.ID.String(),
		fieldStatus:        string(t.Status),
		fieldCreatedAt:     t.CreatedAt.UTC().Format(time.RFC3339),
		fieldLastUpdatedAt: t.UpdatedAt.UTC().Format(time.RFC3339),
		fieldTTLMs:         max(int64(0), t.ExpiresAt.Sub(now).Milliseconds()),
	}
	if t.StatusMessage != "" {
		wire[fieldStatusMessage] = t.StatusMessage
	}
	if !t.Status.terminal() {
		// Only while there is something left to poll for. A terminal task
		// carrying a polling interval invites the one poll that can never
		// change anything.
		wire[fieldPollInterval] = taskPollIntervalMs
	}
	if len(t.Result) > 0 {
		wire[fieldResult] = json.RawMessage(t.Result)
	}
	if len(t.Error) > 0 {
		wire[fieldError] = json.RawMessage(t.Error)
	}
	return wire
}
