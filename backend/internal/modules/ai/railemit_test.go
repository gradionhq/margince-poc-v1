// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import (
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// A settled occurrence's state is read off the terminal attempt, and the one
// pairing worth pinning is error-with-degraded: degraded means partial state
// was KEPT, and a call that ended on a sentinel kept nothing. Reporting that as
// degraded would put a run on the rail claiming to have saved something.
func TestRailStateReadsTheTerminalOutcome(t *testing.T) {
	for _, tc := range []struct {
		name string
		call Call
		want string
	}{
		{"a clean finish", Call{}, railStateDone},
		{"partial state kept", Call{Degraded: true}, railStateDegraded},
		{"ended on a sentinel", Call{ErrorSentinel: "budget_exceeded"}, railStateFailed},
		{"degraded AND errored", Call{Degraded: true, ErrorSentinel: "provider_unavailable"}, railStateFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := railState(tc.call); got != tc.want {
				t.Errorf("railState = %q, want %q", got, tc.want)
			}
		})
	}
}

// degrade_reason reaches an ordinary rep, so it carries the closed sentinel and
// never a provider's own message. A clean call carries none at all rather than
// an empty string, which the projection would store as a reason that exists.
func TestRailDegradeReasonIsTheSentinelOrNothing(t *testing.T) {
	if got := railDegradeReason(Call{}); got != nil {
		t.Errorf("a clean call carries degrade reason %q, want none", *got)
	}
	got := railDegradeReason(Call{ErrorSentinel: "budget_exceeded"})
	if got == nil {
		t.Fatal("an errored call carries no degrade reason, so the rail cannot say why it stopped")
	}
	if *got != "budget_exceeded" {
		t.Errorf("degrade reason = %q, want the sentinel", *got)
	}
}

// The key is what collapses a job's many calls for one task into one line. Two
// calls of the same task under one correlation id are ONE occurrence; the same
// task under a different correlation is a different piece of work, and two
// different tasks under one correlation are two.
func TestTheUnitOfWorkKeyGroupsOneRequestsCallsForOneTask(t *testing.T) {
	corr := ids.NewV7()
	other := ids.NewV7()
	first := Call{CorrelationID: &corr, Task: TaskSummarize, LogicalCallID: ids.NewV7()}
	second := Call{CorrelationID: &corr, Task: TaskSummarize, LogicalCallID: ids.NewV7()}
	if unitOfWorkKey(first) != unitOfWorkKey(second) {
		t.Error("two calls of one task under one correlation id key different occurrences, so one piece of work would draw two lines")
	}
	elsewhere := Call{CorrelationID: &other, Task: TaskSummarize, LogicalCallID: ids.NewV7()}
	if unitOfWorkKey(first) == unitOfWorkKey(elsewhere) {
		t.Error("two requests key one occurrence, so the second would be refused as a redelivery of the first")
	}
	sibling := Call{CorrelationID: &corr, Task: TaskGrowthFit, LogicalCallID: ids.NewV7()}
	if unitOfWorkKey(first) == unitOfWorkKey(sibling) {
		t.Error("two tasks under one correlation id key one occurrence, so one would overwrite the other")
	}
}

// A degraded call says WHY, and the sentinel is not the only source.
//
// The degraded-without-a-sentinel case is the common one: the budget guardrail
// demotes the ladder and the call then succeeds, so there is no error to name.
// A rail that said "degraded" and could never say why would be reporting a
// worse outcome than "done" with nothing a reader could act on.
func TestADegradedCallSaysWhyEvenWithNoSentinel(t *testing.T) {
	got := railDegradeReason(Call{Degraded: true, AttemptReason: attemptReasonBudgetDegrade})
	if got == nil {
		t.Fatal("a budget-degraded call carries no reason, so the rail can only say that it went badly")
	}
	if *got != attemptReasonBudgetDegrade {
		t.Errorf("reason = %q, want the attempt's own reason", *got)
	}
	// The sentinel still wins where there is one: a call that ended on an error
	// kept nothing, and its sentinel is the more specific fact.
	both := railDegradeReason(Call{Degraded: true, ErrorSentinel: "budget_exceeded", AttemptReason: attemptReasonBudgetDegrade})
	if both == nil || *both != "budget_exceeded" {
		t.Errorf("reason = %v, want the sentinel to win over the attempt reason", both)
	}
	// And a clean call still carries none rather than an empty string, which the
	// projection would store as a reason that exists.
	if clean := railDegradeReason(Call{AttemptReason: attemptReasonBudgetDegrade}); clean != nil {
		t.Errorf("a clean call carries reason %q", *clean)
	}
}

// The registry is the router's instruction, not a list beside it. A task a
// carrier owns must leave the router silent, or the occurrence is written twice
// by two writers that disagree about its lifecycle.
func TestTheRouterStaysSilentForACarrierOwnedTask(t *testing.T) {
	if RouterReports(TaskDocumentExtract) {
		t.Error("the router reports document_extract, which attachment extraction owns — the two would both write its occurrence")
	}
	if RouterReports(TaskAgentLoop) {
		t.Error("the router reports agent_loop, which the scheduled runner owns")
	}
	if !RouterReports(TaskSummarize) {
		t.Error("the router reports nothing for summarize, and no carrier owns it, so the task is silent")
	}
}

// A task that is a STEP inside another piece of work announces nothing of its
// own. Announcing it would report one piece of work twice at two grains — and
// for embeddings specifically, the reindex pass mints no correlation id, so
// every vector would key its own occurrence.
func TestTheRouterStaysSilentForAStepThatIsNotAnOccurrence(t *testing.T) {
	if RailOwner(TaskEmbeddings) != SourceNoOccurrence {
		t.Fatalf("embeddings is owned by %q, want %q", RailOwner(TaskEmbeddings), SourceNoOccurrence)
	}
	if RouterReports(TaskEmbeddings) {
		t.Error("the router announces every embedding call, so a search and the embedding it made are two lines for one piece of work")
	}
	if NoOccurrenceReason(TaskEmbeddings) == "" {
		t.Error("embeddings reports nothing and says nothing about why")
	}
}

// An unanswered task leaves the router silent rather than guessing. The gate at
// the root is what stops one existing; this pins the behaviour it relies on.
func TestAnUnansweredTaskIsNobodysToReport(t *testing.T) {
	if got := RailOwner(Task("a_task_nobody_declared")); got != "" {
		t.Errorf("RailOwner invented owner %q for an undeclared task", got)
	}
	if RouterReports(Task("a_task_nobody_declared")) {
		t.Error("the router would announce a task nobody answered for, inventing a grain and an attribution no gate has read")
	}
}

// A call outside any correlation scope is NOT announced, and the router says so
// rather than trying.
//
// storekit.Emit refuses an envelope with no correlation id, and Call.CorrelationID
// is read from that same context value — so when it is absent the announcement
// cannot succeed, it can only fail late. An earlier version of this file
// documented a fallback to the logical call id, and the test for it asserted the
// KEY that fallback would have used: the key was reachable, the occurrence never
// was, and every such call paid a lock, a count and a system_log row before
// erroring out.
func TestACallOutsideACorrelationScopeIsNotAnnounced(t *testing.T) {
	logical := ids.NewV7()
	// The key function still answers, because it is asked for other reasons —
	// what changed is that nothing asks it on this path.
	if key := unitOfWorkKey(Call{Task: TaskSummarize, LogicalCallID: logical}); key == "" {
		t.Error("the key function answers nothing for a call with no correlation id")
	}
	// The behaviour that matters is the skip, and it is asserted where the
	// decision is made rather than through the key it would have used.
	for _, c := range []Call{
		{Task: TaskSummarize, LogicalCallID: logical},
		{Task: TaskSummarize, LogicalCallID: logical, CorrelationID: new(ids.UUID)},
	} {
		if announceable(c) {
			t.Errorf("a call with correlation %v would be announced, and the bus would refuse it", c.CorrelationID)
		}
	}
	corr := ids.NewV7()
	if !announceable(Call{Task: TaskSummarize, LogicalCallID: logical, CorrelationID: &corr}) {
		t.Error("a call inside a correlation scope is not announceable, so nothing would ever reach the rail")
	}
}
