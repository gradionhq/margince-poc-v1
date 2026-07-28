// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package aicert

// What the record's numbers MEAN, driven end to end through certifyTask. The
// counts a record carries answer two different questions — did the run do what
// the scenario asked, and what came back when it did not — and a reader who
// takes one for the other reads a failed task as a passing one.

import (
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose/aitasks"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
)

// A run that came back accepted where the scenario demanded an abstention is a
// FAILED run. The record has to say so on its own: a reader who takes ACCEPTED
// as a pass count reads three successes here, and there are none.
func TestCertifyTaskCountsAnAcceptedRunThatFailedItsScenarioAsFailed(t *testing.T) {
	candidateFake := ai.NewFakeClient().Script(
		"the widget is blue and durable", "the widget is blue and durable", "the widget is blue and durable")
	judgeFake := ai.NewFakeClient().Script(scoreJSON(90), scoreJSON(90), scoreJSON(90))

	sc := testScenario("expects silence", wideBands)
	sc.Expect.Outcome = aitasks.OutcomeAbstained

	rec, err := certifyTask(wsContext(t), ai.TaskSummarize, []Scenario{sc}, testCensus(t),
		ai.FakeRoutingConfig(), "", 3, quietLogger(), &certifyHooks{
			candidateOpts: []ai.LocalOption{ai.WithFakeClient(candidateFake)},
			judgeOpts:     []ai.LocalOption{ai.WithFakeClient(judgeFake)},
		})
	if err != nil {
		t.Fatalf("certifyTask: %v", err)
	}
	if rec.ReportedAccepted != 3 {
		t.Fatalf("reported_accepted = %d, want 3 — the validator did accept all three replies", rec.ReportedAccepted)
	}
	if rec.Passed != 0 || rec.Reliability != 0 {
		t.Fatalf("passed=%d reliability=%v, want 0 and 0 — none of the three answered what the scenario asked",
			rec.Passed, rec.Reliability)
	}
}

// A task is not one scenario, and a pooled record cannot say which one failed.
// The two scenarios below reach one task reliability that describes neither.
func TestCertifyTaskRecordsEachScenariosOwnCounts(t *testing.T) {
	candidateFake := ai.NewFakeClient().Script(
		"the widget is blue", "the widget is blue", "the widget is blue", // scenario 1: every run answers
		"off topic, no keyword here", "off topic, no keyword here", "off topic, no keyword here", // scenario 2: none does
	)
	judgeFake := ai.NewFakeClient().Script(
		scoreJSON(90), scoreJSON(90), scoreJSON(90),
		scoreJSON(90), scoreJSON(90), scoreJSON(90),
	)

	rec, err := certifyTask(wsContext(t), ai.TaskSummarize,
		[]Scenario{testScenario("answers", wideBands), testScenario("wanders", wideBands)},
		testCensus(t), ai.FakeRoutingConfig(), "", 3, quietLogger(), &certifyHooks{
			candidateOpts: []ai.LocalOption{ai.WithFakeClient(candidateFake)},
			judgeOpts:     []ai.LocalOption{ai.WithFakeClient(judgeFake)},
		})
	if err != nil {
		t.Fatalf("certifyTask: %v", err)
	}
	if len(rec.Scenarios) != 2 {
		t.Fatalf("the record carries %d scenario rows, want one per scenario the task ran", len(rec.Scenarios))
	}
	byName := map[string]ScenarioRecord{}
	for _, row := range rec.Scenarios {
		byName[row.Scenario] = row
	}
	answers, wanders := byName["answers"], byName["wanders"]
	if answers.Runs != 3 || answers.Passed != 3 || answers.Verdict != VerdictCertified {
		t.Errorf("the passing scenario reads %+v, want 3 of 3 passed and %s", answers, VerdictCertified)
	}
	if wanders.Runs != 3 || wanders.Passed != 0 || wanders.Verdict != VerdictNotSupported {
		t.Errorf("the failing scenario reads %+v, want 0 of 3 passed and %s", wanders, VerdictNotSupported)
	}
	if wanders.ReportedWrongAnswer != 3 {
		t.Errorf("the failing scenario's replies were well-formed and wrong: %+v", wanders)
	}
	if answers.Site != widgetVariant || wanders.Site != widgetVariant {
		t.Errorf("a scenario row that does not name its site cannot be attributed to one: %+v, %+v", answers, wanders)
	}
	// The pooled numbers still describe the task — and describe neither scenario.
	if rec.Runs != 6 || rec.Passed != 3 {
		t.Errorf("task totals = %d runs / %d passed, want 6 and 3", rec.Runs, rec.Passed)
	}
}
