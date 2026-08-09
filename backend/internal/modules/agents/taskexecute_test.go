// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// What a POLL does, as opposed to what a client is told: the decision it
// observes, the effect it performs at most once, and the answer it records.
//
// The protocol half — negotiation, the three methods, the wire shape — is
// tasks_test.go, which also holds the fakes both files drive. The split is the
// same one taskexecute.go and tasksdispatch.go keep.

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// The status machine: each thing a human can do to the staged proposal maps to
// exactly one task state, and `rejected` is the one that reads backwards —
// a person declining is a RESULT the surface returned, not a protocol fault.
func TestEachApprovalOutcomeMapsToItsOwnTaskState(t *testing.T) {
	for _, tc := range []struct {
		decision   ApprovalDecision
		wantStatus TaskStatus
		wantError  bool
	}{
		{ApprovalPending, TaskWorking, false},
		{ApprovalRejected, TaskCompleted, true},
		{ApprovalExpired, TaskCancelled, false},
		{ApprovalApproved, TaskCompleted, false},
	} {
		t.Run(string(tc.decision), func(t *testing.T) {
			s, store := stagingDispatcher(t)
			task := mintOne(t, s, store)
			store.approvals.decision = tc.decision

			wire := pollTask(t, s, task.ID)
			if wire[fieldStatus] != string(tc.wantStatus) {
				t.Fatalf("status = %v, want %v", wire[fieldStatus], tc.wantStatus)
			}
			if tc.wantStatus == TaskCompleted {
				result, ok := wire[fieldResult].(json.RawMessage)
				if !ok {
					t.Fatalf("a completed task carried no result: %v", wire)
				}
				if isToolError(t, result) != tc.wantError {
					t.Errorf("result isError = %v, want %v", !tc.wantError, tc.wantError)
				}
			}
			if tc.wantStatus.terminal() && wire[fieldPollInterval] != nil {
				t.Errorf("a terminal task invited another poll: %v", wire)
			}
		})
	}
}

// The effect happens ONCE, and the second poll is answered from what the first
// one recorded rather than by running anything again. This is the property a
// stored result exists for: redemption is single-use, so a re-run could not
// succeed even if it were attempted.
func TestPollingACompletedTaskTwiceRunsTheCallOnce(t *testing.T) {
	s, store := stagingDispatcher(t)
	task := mintOne(t, s, store)
	store.approvals.decision = ApprovalApproved

	first := pollTask(t, s, task.ID)
	second := pollTask(t, s, task.ID)

	if store.tool.calls != 1 {
		t.Fatalf("the released call ran %d times, want exactly 1", store.tool.calls)
	}
	if first[fieldStatus] != string(TaskCompleted) || second[fieldStatus] != string(TaskCompleted) {
		t.Fatalf("statuses = %v / %v, want completed twice", first[fieldStatus], second[fieldStatus])
	}
	if string(first[fieldResult].(json.RawMessage)) != string(second[fieldResult].(json.RawMessage)) {
		t.Errorf("the second poll answered a different result:\n%s\n%s", first[fieldResult], second[fieldResult])
	}
}

// A recorded answer is a receipt that outlives the authority it was produced
// under. When the caller can no longer read the records it names, the document
// is withheld — the same rule the idempotency replay keeps, and the reason both
// doors share one gate.
func TestACompletedTasksResultIsWithheldOnceItsRecordsAreNoLongerReadable(t *testing.T) {
	s, store := stagingDispatcher(t)
	task := mintOne(t, s, store)
	store.approvals.decision = ApprovalApproved

	first := pollTask(t, s, task.ID)
	if isToolError(t, first[fieldResult].(json.RawMessage)) {
		t.Fatalf("the first poll refused its own fresh answer: %v", first)
	}

	// The record moves out of this caller's reach — reassigned, archived, or
	// erased. Nothing about the task row changes.
	store.records.hide(store.tool.target)

	again := pollTask(t, s, task.ID)
	if again[fieldStatus] != string(TaskCompleted) {
		t.Fatalf("status = %v, want the task to stay completed — the effect did happen", again[fieldStatus])
	}
	if !isToolError(t, again[fieldResult].(json.RawMessage)) {
		t.Errorf("the recorded record was served again after the caller lost access to it:\n%s",
			again[fieldResult])
	}
	if store.tool.calls != 1 {
		t.Errorf("the released call ran %d times, want 1", store.tool.calls)
	}
}

// What is executed is what the PERSON released, not what the agent proposed. A
// human may edit a staged proposal before approving it, which rewrites both the
// payload and the hash that opens it — so a task replaying its original
// arguments would perform the wrong change and be refused for saying so.
func TestTheReleasedCallCarriesTheHumansEditedPayload(t *testing.T) {
	s, store := stagingDispatcher(t)
	task := mintOne(t, s, store)
	store.approvals.decision = ApprovalApproved
	store.approvals.change = json.RawMessage(`{"body":"what the human wrote"}`)

	pollTask(t, s, task.ID)

	var seen map[string]any
	if err := json.Unmarshal(store.tool.lastArgs, &seen); err != nil {
		t.Fatalf("the released call's arguments did not decode: %v", err)
	}
	if seen["body"] != "what the human wrote" {
		t.Errorf("the call carried %v, want the human's edited payload", seen["body"])
	}
}

// The approval id is the surface's own argument, so a payload that carried one
// has no say over which approval the released call redeems.
func TestAReleasedCallRedeemsTheTasksOwnApproval(t *testing.T) {
	s, store := stagingDispatcher(t)
	task := mintOne(t, s, store)
	store.approvals.decision = ApprovalApproved
	store.approvals.change = json.RawMessage(`{"approval_id":"` + ids.From[ids.ApprovalKind](ids.NewV7()).String() + `"}`)

	pollTask(t, s, task.ID)

	if len(store.approvals.redeemed) != 1 {
		t.Fatalf("redeemed %d approvals, want exactly 1", len(store.approvals.redeemed))
	}
	if store.approvals.redeemed[0] != task.ApprovalID {
		t.Errorf("redeemed %s, want the task's own approval %s — a payload naming another "+
			"approval must not choose which one the released call spends",
			store.approvals.redeemed[0], task.ApprovalID)
	}
}

// An approval spent OUTSIDE this task — an ordinary retry carrying the same
// approval_id, or a poll that died after redeeming — is not an unexecuted one.
// Reading only the task's own claim would call it unexecuted, invoke, be
// refused as already-redeemed, and record a refusal for an effect that in fact
// succeeded.
func TestAnApprovalSpentOutsideTheTaskIsNotReExecuted(t *testing.T) {
	s, store := stagingDispatcher(t)
	task := mintOne(t, s, store)
	store.approvals.decision = ApprovalApproved
	store.approvals.consumed = true

	wire := pollTask(t, s, task.ID)

	if wire[fieldStatus] != string(TaskFailed) {
		t.Fatalf("status = %v (%v), want failed — the outcome is unknown, not a refusal",
			wire[fieldStatus], wire[fieldStatusMessage])
	}
	if store.tool.calls != 0 {
		t.Errorf("the released call ran %d times against a spent approval, want 0", store.tool.calls)
	}
	if len(store.approvals.redeemed) != 0 {
		t.Errorf("the task tried to redeem an approval already consumed elsewhere")
	}
}

// The claim lease must outlast the longest exchange the transport allows, or a
// poll still inside its own handler is declared dead by the next one — which
// then settles the task `failed` while the first execution goes on to commit.
func TestTheClaimLeaseOutlastsTheLongestExchange(t *testing.T) {
	// Read through the exported accessor, which is what the retention sweep
	// uses to decide whether an expired row may still have an executor inside
	// it. Two readings of one bound would let the sweep delete a live one.
	if ClaimLease() != taskClaimLease {
		t.Fatalf("ClaimLease() = %s but the executor holds %s", ClaimLease(), taskClaimLease)
	}
	if taskClaimLease <= mcpCallDeadline {
		t.Fatalf("claim lease %s does not outlast the %s exchange deadline: a live executor "+
			"would be reclaimed and its task settled failed while it was still running",
			taskClaimLease, mcpCallDeadline)
	}
}

// A poll that loses the claim runs nothing and says `working`, which is true.
func TestOnlyOneOfTwoSimultaneousPollsRunsTheCall(t *testing.T) {
	s, store := stagingDispatcher(t)
	task := mintOne(t, s, store)
	store.approvals.decision = ApprovalApproved

	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.dispatch(agentCtx(), rpcRequest{
				JSONRPC: jsonRPCVersion, ID: json.RawMessage(`1`), Method: methodTasksGet,
				Params: json.RawMessage(`{"taskId":"` + task.ID.String() + `"}`),
			}, taskCapableFraming())
		}()
	}
	wg.Wait()

	if store.tool.calls != 1 {
		t.Errorf("the released call ran %d times under two concurrent polls, want 1", store.tool.calls)
	}
}

// A claim taken from somebody who never settled is the interrupted case: an
// earlier execution died and its effect may or may not have committed. Running
// again would risk a second act on one human yes, so the task fails and says it
// does not know.
func TestATaskReclaimedAfterAnUnsettledExecutionFailsWithoutRunning(t *testing.T) {
	s, store := stagingDispatcher(t)
	task := mintOne(t, s, store)
	store.approvals.decision = ApprovalApproved
	store.reclaimed = true

	wire := pollTask(t, s, task.ID)

	if wire[fieldStatus] != string(TaskFailed) {
		t.Fatalf("status = %v, want failed", wire[fieldStatus])
	}
	if store.tool.calls != 0 {
		t.Errorf("the released call ran %d times after an interrupted attempt, want 0", store.tool.calls)
	}
	if wire[fieldError] == nil {
		t.Error("a failed task carried no error, so a client learns nothing about why")
	}
}

// A receipt is a verb. A passport past its volume ceiling is refused for every
// verb, so it must not be able to keep drawing record documents out of answers
// it produced before the ceiling closed — the poll door takes the same ceiling
// the call door does.
func TestAPassportPastItsCeilingCannotKeepDrawingARecordedAnswer(t *testing.T) {
	s, store := stagingDispatcher(t)
	task := mintOne(t, s, store)
	store.approvals.decision = ApprovalApproved

	first := pollTask(t, s, task.ID)
	if isToolError(t, first[fieldResult].(json.RawMessage)) {
		t.Fatalf("the first poll refused its own fresh answer: %v", first)
	}

	// The window closes underneath the handle.
	store.quota.exceed()

	again := pollTask(t, s, task.ID)

	if again[fieldStatus] != string(TaskCompleted) {
		t.Fatalf("status = %v, want the task to stay completed — the effect did happen", again[fieldStatus])
	}
	if !isToolError(t, again[fieldResult].(json.RawMessage)) {
		t.Errorf("a suspended passport was served the recorded records anyway:\n%s", again[fieldResult])
	}
	// And the refusal says the window ends it, not that the records are gone:
	// one clears by waiting and the other never does.
	if body := string(again[fieldResult].(json.RawMessage)); strings.Contains(body, "no longer readable") {
		t.Errorf("a volume refusal was reported as lost access, which tells the agent not to retry:\n%s", body)
	}
}

// A settlement can LOSE, and the row it loses to is somebody else's answer. It
// is a read like any other: rendered() would hand its records over on the
// strength of a call this request never made.
func TestASettlementThatLosesReProvesTheAnswerItFindsInstead(t *testing.T) {
	s, store := stagingDispatcher(t)
	task := mintOne(t, s, store)
	store.approvals.decision = ApprovalApproved

	// The winner records a completed answer carrying records.
	pollTask(t, s, task.ID)
	// The loser arrives with its own terminal settlement and finds that row.
	store.records.hide(store.tool.target)
	lost := s.settle(agentCtx(), store.tasks[task.ID], Settlement{
		Status:        TaskFailed,
		StatusMessage: taskInterruptedMessage,
		Error:         mustMarshalRPCError(taskInterruptedMessage),
	})

	if lost[fieldStatus] != string(TaskCompleted) {
		t.Fatalf("status = %v, want the stored completed answer", lost[fieldStatus])
	}
	if !isToolError(t, lost[fieldResult].(json.RawMessage)) {
		t.Errorf("the losing settlement was handed records it never proved:\n%s", lost[fieldResult])
	}
}

// What every one of these has in common: a dependency this surface does not
// control has failed, and the answer must not be a confident lie. A task that
// claimed to be cancelled, or a handle nothing can poll, costs more than the
// refusal it replaced.
func TestADependencyFailureNeverBecomesAConfidentAnswer(t *testing.T) {
	failing := errors.New("the store is unreachable")

	t.Run("a decision that cannot be read leaves the task working", func(t *testing.T) {
		s, store := stagingDispatcher(t)
		task := mintOne(t, s, store)
		store.approvals.decision = ApprovalApproved
		store.approvals.stateErr = failing

		wire := pollTask(t, s, task.ID)

		if wire[fieldStatus] != string(TaskWorking) {
			t.Errorf("status = %v, want working — an unreadable decision is not a decision", wire[fieldStatus])
		}
		if store.tool.calls != 0 {
			t.Errorf("the released call ran %d times without a readable approval", store.tool.calls)
		}
	})

	t.Run("a settlement that cannot be written still tells its caller the truth", func(t *testing.T) {
		s, store := stagingDispatcher(t)
		task := mintOne(t, s, store)
		store.approvals.decision = ApprovalApproved
		store.settleErr = failing

		wire := pollTask(t, s, task.ID)

		// The effect COMMITTED. This poll is the one chance to say so.
		if store.tool.calls != 1 {
			t.Fatalf("the released call ran %d times, want 1", store.tool.calls)
		}
		if wire[fieldStatus] != string(TaskCompleted) {
			t.Errorf("status = %v, want completed — the effect happened", wire[fieldStatus])
		}
	})

	t.Run("a payload that cannot be rebuilt fails without running", func(t *testing.T) {
		s, store := stagingDispatcher(t)
		task := mintOne(t, s, store)
		store.approvals.decision = ApprovalApproved
		store.approvals.changeErr = failing

		wire := pollTask(t, s, task.ID)

		if wire[fieldStatus] != string(TaskFailed) {
			t.Errorf("status = %v, want failed", wire[fieldStatus])
		}
		if store.tool.calls != 0 {
			t.Errorf("a call was made from a payload that could not be read")
		}
	})

	t.Run("a payload that is not an object fails without running", func(t *testing.T) {
		s, store := stagingDispatcher(t)
		task := mintOne(t, s, store)
		store.approvals.decision = ApprovalApproved
		store.approvals.change = json.RawMessage(`null`)

		wire := pollTask(t, s, task.ID)

		if wire[fieldStatus] != string(TaskFailed) {
			t.Errorf("status = %v, want failed — a JSON null is not a call", wire[fieldStatus])
		}
		if store.tool.calls != 0 {
			t.Errorf("a null payload reached the tool")
		}
	})

	t.Run("a withdrawal that fails refuses the cancel rather than acking it", func(t *testing.T) {
		s, store := stagingDispatcher(t)
		task := mintOne(t, s, store)
		store.approvals.withdrawErr = failing

		resp := cancelTaskCall(t, s, task.ID)

		if resp.Error == nil || resp.Error.Code != codeInternalError {
			t.Fatalf("cancel answered %+v, want an internal error", resp.Error)
		}
		if store.tasks[task.ID].Status != TaskWorking {
			t.Errorf("the task was settled %v despite a live proposal", store.tasks[task.ID].Status)
		}
	})

	t.Run("a cancellation that cannot be recorded refuses too", func(t *testing.T) {
		s, store := stagingDispatcher(t)
		task := mintOne(t, s, store)
		store.cancelErr = failing

		resp := cancelTaskCall(t, s, task.ID)

		if resp.Error == nil || resp.Error.Code != codeInternalError {
			t.Fatalf("cancel answered %+v, want an internal error — an empty ack would stop the client polling",
				resp.Error)
		}
	})

	t.Run("a handle that cannot be created falls back to the plain refusal", func(t *testing.T) {
		s, store := stagingDispatcher(t)
		store.createErr = failing

		out := s.call(agentCtx(), json.RawMessage(`{"name":"send_it","arguments":{}}`), taskCapableFraming())

		refusal, ok := out.(map[string]any)
		if !ok || refusal["isError"] != true {
			t.Fatalf("answered %v, want the refusal the client would have got anyway", out)
		}
	})

	t.Run("a task that cannot be read is an internal error, not an absent one", func(t *testing.T) {
		s, store := stagingDispatcher(t)
		task := mintOne(t, s, store)
		store.loadErr = failing

		resp := s.dispatch(agentCtx(), rpcRequest{
			JSONRPC: jsonRPCVersion, ID: json.RawMessage(`1`), Method: methodTasksGet,
			Params: json.RawMessage(`{"taskId":"` + task.ID.String() + `"}`),
		}, taskCapableFraming())

		if resp.Error == nil || resp.Error.Code != codeInternalError {
			t.Errorf("answered %+v, want an internal error — saying no such task would be a guess", resp.Error)
		}
	})
}
