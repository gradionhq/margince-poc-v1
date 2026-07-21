// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package aicert

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/ai"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// testScenario builds a Scenario for TaskSummarize — the one task every
// certifyTask test in this file certifies (the task under test is
// always passed to certifyTask directly; sc.Task here is just the
// corpus record's own descriptive field, never consulted for routing).
func testScenario(name string, bands Bands, checks []Check) Scenario {
	return Scenario{
		Name:        name,
		Task:        string(ai.TaskSummarize),
		Source:      sourceHandAuthored,
		SanitizedBy: "tester",
		Input:       "Describe the widget in one sentence.",
		Expect: Expectations{
			Structural: checks,
			Rubric:     "Score higher for a longer, on-topic, concrete answer; lower for a vague or off-topic one.",
			Bands:      bands,
		},
	}
}

func scoreJSON(score int) string {
	return `{"score": ` + strconv.Itoa(score) + `, "reason": "test-scripted"}`
}

var wideBands = Bands{CertifiedMin: 70, DegradedMin: 50, Floor: 40}

// --- pure helpers ---

func TestRepeatsOrDefault(t *testing.T) {
	cases := []struct {
		name    string
		in      int
		want    int
		wantErr bool
	}{
		{"zero defaults to three", 0, 3, false},
		{"valid odd", 5, 5, false},
		{"one is valid", 1, 1, false},
		{"even is refused", 4, 0, true},
		{"negative is refused", -1, 0, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := repeatsOrDefault(c.in)
			if c.wantErr {
				if err == nil {
					t.Fatalf("repeatsOrDefault(%d): want an error, got %d", c.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("repeatsOrDefault(%d): unexpected error: %v", c.in, err)
			}
			if got != c.want {
				t.Fatalf("repeatsOrDefault(%d) = %d, want %d", c.in, got, c.want)
			}
		})
	}
}

func TestOverrideForTaskRebindsOnlyTheTaskLadderAndNeverMutatesBase(t *testing.T) {
	base := ai.FakeRoutingConfig()
	before := len(base.Tiers)

	overridden, err := overrideForTask(base, ai.TaskColdStart, "anthropic:claude-cert-test")
	if err != nil {
		t.Fatalf("valid override rejected: %v", err)
	}
	for _, tier := range ai.TaskLadder(ai.TaskColdStart) {
		binding := overridden.Tiers[tier]
		if binding.Provider != "anthropic" || binding.Model != "claude-cert-test" {
			t.Errorf("tier %s = %+v, want the override binding", tier, binding)
		}
	}
	if binding := overridden.Tiers[ai.TierLocalSmall]; binding.Provider != ai.ProviderFake {
		t.Errorf("a tier off TaskColdStart's ladder must be untouched, got %+v", binding)
	}
	if len(base.Tiers) != before || base.Tiers[ai.TierCheapCloud].Provider != ai.ProviderFake {
		t.Fatalf("overrideForTask mutated the base config's own tier map: %+v", base.Tiers)
	}
}

func TestOverrideForTaskNoOpOnEmptyOverride(t *testing.T) {
	base := ai.FakeRoutingConfig()
	got, err := overrideForTask(base, ai.TaskColdStart, "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Tiers[ai.TierCheapCloud].Provider != ai.ProviderFake {
		t.Fatalf("empty override must leave the base binding untouched, got %+v", got.Tiers[ai.TierCheapCloud])
	}
}

func TestOverrideForTaskRefusesAMalformedOverride(t *testing.T) {
	_, err := overrideForTask(ai.FakeRoutingConfig(), ai.TaskColdStart, "no-colon-here")
	if err == nil || !strings.Contains(err.Error(), "provider:model") {
		t.Fatalf("want a provider:model complaint, got %v", err)
	}
}

func TestOverrideForTaskRefusesATaskWithNoLadder(t *testing.T) {
	_, err := overrideForTask(ai.FakeRoutingConfig(), ai.Task("not_a_real_task"), "anthropic:claude-x")
	if err == nil || !strings.Contains(err.Error(), "no routing ladder") {
		t.Fatalf("want a no-routing-ladder complaint, got %v", err)
	}
}

func TestGroupByTaskFiltersAndSortedTasksOrdersDeterministically(t *testing.T) {
	scenarios := []Scenario{
		{Name: "a", Task: string(ai.TaskSummarize)},
		{Name: "b", Task: string(ai.TaskColdStart)},
		{Name: "c", Task: string(ai.TaskSummarize)},
	}
	all := groupByTask(scenarios, "")
	if len(all[ai.TaskSummarize]) != 2 || len(all[ai.TaskColdStart]) != 1 {
		t.Fatalf("unfiltered grouping = %+v", all)
	}
	filtered := groupByTask(scenarios, string(ai.TaskColdStart))
	if len(filtered) != 1 || len(filtered[ai.TaskColdStart]) != 1 {
		t.Fatalf("filtered grouping = %+v", filtered)
	}
	order := sortedTasks(all)
	if len(order) != 2 || order[0] != ai.TaskColdStart || order[1] != ai.TaskSummarize {
		t.Fatalf("sortedTasks = %v, want [cold_start summarize]", order)
	}
}

func TestWorstVerdictRanksNotSupportedBelowDegradedBelowCertified(t *testing.T) {
	cases := []struct{ a, b, want string }{
		{VerdictCertified, VerdictNotSupported, VerdictNotSupported},
		{VerdictCertified, VerdictSupportedDegraded, VerdictSupportedDegraded},
		{VerdictSupportedDegraded, VerdictNotSupported, VerdictNotSupported},
		{VerdictCertified, VerdictCertified, VerdictCertified},
	}
	for _, c := range cases {
		if got := worstVerdict(c.a, c.b); got != c.want {
			t.Errorf("worstVerdict(%s, %s) = %s, want %s", c.a, c.b, got, c.want)
		}
	}
}

func TestPercentileNearestRank(t *testing.T) {
	sorted := []int64{10, 20, 30}
	if got := percentile(sorted, 0.50); got != 20 {
		t.Errorf("p50 of %v = %d, want 20", sorted, got)
	}
	if got := percentile(sorted, 0.95); got != 30 {
		t.Errorf("p95 of %v = %d, want 30", sorted, got)
	}
	if got := percentile(nil, 0.50); got != 0 {
		t.Errorf("percentile of an empty slice = %d, want 0", got)
	}
}

// --- certifyTask: the real router pipeline over the offline fake ---

const containsWidget = "widget"

func widgetChecks() []Check {
	return []Check{{Kind: checkKindContains, Arg: containsWidget}}
}

func TestCertifyTaskCertifiesWhenEveryRunPassesAndScoresHigh(t *testing.T) {
	candidateFake := ai.NewFakeClient().Script("the widget is blue and durable", "the widget is blue and durable", "the widget is blue and durable")
	judgeFake := ai.NewFakeClient().Script(scoreJSON(90), scoreJSON(90), scoreJSON(90))

	sc := testScenario("basic", wideBands, widgetChecks())
	rec, err := certifyTask(wsContext(t), ai.TaskSummarize, []Scenario{sc}, ai.FakeRoutingConfig(), "", 3, quietLogger(), &certifyHooks{
		candidateOpts: []ai.LocalOption{ai.WithFakeClient(candidateFake)},
		judgeOpts:     []ai.LocalOption{ai.WithFakeClient(judgeFake)},
	})
	if err != nil {
		t.Fatalf("certifyTask: %v", err)
	}
	if rec.Verdict != VerdictCertified {
		t.Fatalf("verdict = %q, want %q (record: %+v)", rec.Verdict, VerdictCertified, rec)
	}
	if rec.Runs != 3 || rec.Reliability != 1 {
		t.Fatalf("runs=%d reliability=%v, want 3 and 1", rec.Runs, rec.Reliability)
	}
	if rec.ScoreP50 != 90 || rec.ScoreMin != 90 {
		t.Fatalf("score_p50=%d score_min=%d, want 90 and 90", rec.ScoreP50, rec.ScoreMin)
	}
	if !rec.SelfJudged {
		t.Fatalf("both candidate and judge served through the fake provider — want self_judged true, record: %+v", rec)
	}
	if rec.Provider != ai.ProviderFake || rec.ServedModel != ai.ProviderFake {
		t.Fatalf("provider/served_model = %q/%q, want %q/%q", rec.Provider, rec.ServedModel, ai.ProviderFake, ai.ProviderFake)
	}
}

func TestCertifyTaskSupportedDegradedOnPartialReliability(t *testing.T) {
	candidateFake := ai.NewFakeClient().Script(
		"the widget is blue and durable",
		"the widget is blue and durable",
		"off topic, no keyword here",
	)
	judgeFake := ai.NewFakeClient().Script(scoreJSON(70), scoreJSON(70), scoreJSON(70))

	sc := testScenario("basic", wideBands, widgetChecks())
	rec, err := certifyTask(wsContext(t), ai.TaskSummarize, []Scenario{sc}, ai.FakeRoutingConfig(), "", 3, quietLogger(), &certifyHooks{
		candidateOpts: []ai.LocalOption{ai.WithFakeClient(candidateFake)},
		judgeOpts:     []ai.LocalOption{ai.WithFakeClient(judgeFake)},
	})
	if err != nil {
		t.Fatalf("certifyTask: %v", err)
	}
	if rec.Verdict != VerdictSupportedDegraded {
		t.Fatalf("verdict = %q, want %q (record: %+v)", rec.Verdict, VerdictSupportedDegraded, rec)
	}
	if got := rec.Reliability; got < 0.66 || got > 0.67 {
		t.Fatalf("reliability = %v, want ~2/3", got)
	}
}

func TestCertifyTaskNotSupportedOnLowScores(t *testing.T) {
	candidateFake := ai.NewFakeClient().Script("the widget is blue", "the widget is blue", "the widget is blue")
	judgeFake := ai.NewFakeClient().Script(scoreJSON(10), scoreJSON(10), scoreJSON(10))

	sc := testScenario("basic", wideBands, widgetChecks())
	rec, err := certifyTask(wsContext(t), ai.TaskSummarize, []Scenario{sc}, ai.FakeRoutingConfig(), "", 3, quietLogger(), &certifyHooks{
		candidateOpts: []ai.LocalOption{ai.WithFakeClient(candidateFake)},
		judgeOpts:     []ai.LocalOption{ai.WithFakeClient(judgeFake)},
	})
	if err != nil {
		t.Fatalf("certifyTask: %v", err)
	}
	if rec.Verdict != VerdictNotSupported {
		t.Fatalf("verdict = %q, want %q — every run passed structurally but the score never clears the floor", rec.Verdict, VerdictNotSupported)
	}
}

// TestCertifyTaskDegradedCandidateAttemptYieldsNoRecord covers the
// spec's hard rule: a budget-forced degrade on ANY run refuses the whole
// task's certification rather than certifying a demoted answer.
// WithMonthlyBudget(1) guarantees the second call already sees a spent
// balance many multiples of the ceiling, so TaskSummarize's ladder
// (interactive, so it pins rather than queues) demotes to local_small —
// still bound and servable under ai.FakeRoutingConfig(), so this is a
// genuine soft-degrade, never a hard failure.
func TestCertifyTaskDegradedCandidateAttemptYieldsNoRecord(t *testing.T) {
	sc := testScenario("basic", wideBands, nil)
	_, err := certifyTask(wsContext(t), ai.TaskSummarize, []Scenario{sc}, ai.FakeRoutingConfig(), "", 3, quietLogger(), &certifyHooks{
		candidateOpts: []ai.LocalOption{ai.WithMonthlyBudget(1)},
	})
	if err == nil {
		t.Fatal("want an error — no record for a task with a degraded candidate attempt")
	}
	if !strings.Contains(err.Error(), "degraded") {
		t.Fatalf("error should name the degrade, got %v", err)
	}
}

// TestCertifyTaskDegradedJudgeAttemptYieldsNoRecord covers the judge-side
// half of the spec's hard rule (§5: "any Degraded attempt ⇒ no record for
// that task"), which historically was only checked on the candidate: a
// budget-forced demotion on the JUDGE's own trace must also void the
// task's record, because a demoted judge silently grades every run with
// a weaker model and nothing in the Record would ever show it.
//
// The judge's task (cert_judge) queues rather than degrades once its
// budget is fully exhausted for background work, so a naively tiny budget
// would surface a hard ErrBudgetDeferred, not the soft in-band
// [80%,100%) demotion this test needs. Instead: probe the exact token
// cost of the judge's first call (request and response text are fixed,
// so the fake client's deterministic "4 bytes per token" arithmetic
// makes this exact, not approximate), then size the budget so the
// SECOND call — the parse-failure retry, still against the same judge
// router/meter as the first — lands at ~90% utilization: squarely
// inside the soft-degrade band regardless of small estimation error.
func TestCertifyTaskDegradedJudgeAttemptYieldsNoRecord(t *testing.T) {
	const candidateOutput = "the widget is blue and durable"
	sc := testScenario("basic", wideBands, widgetChecks())

	probeReq := judgeRequest(sc, candidateOutput)
	probeResp, err := ai.NewFakeClient().Script("not valid json at all").Complete(context.Background(), probeReq)
	if err != nil {
		t.Fatalf("probing the judge's first-call token cost: %v", err)
	}
	call1Tokens := int64(probeResp.InputTokens + probeResp.OutputTokens)
	budget := call1Tokens * 10 / 9 // ~90% utilization after call 1 — inside [80%,100%)

	candidateFake := ai.NewFakeClient().Script(candidateOutput)
	judgeFake := ai.NewFakeClient().Script("not valid json at all", scoreJSON(90))

	_, err = certifyTask(wsContext(t), ai.TaskSummarize, []Scenario{sc}, ai.FakeRoutingConfig(), "", 1, quietLogger(), &certifyHooks{
		candidateOpts: []ai.LocalOption{ai.WithFakeClient(candidateFake)},
		judgeOpts:     []ai.LocalOption{ai.WithFakeClient(judgeFake), ai.WithMonthlyBudget(budget)},
	})
	if err == nil {
		t.Fatal("want an error — no record for a task with a degraded judge attempt")
	}
	if !strings.Contains(err.Error(), "judge") || !strings.Contains(err.Error(), "degraded") {
		t.Fatalf("error should name the judge degrade, got %v", err)
	}
}

// TestCertifyTaskJudgeRetriesOnceOnAParseFailureThenScores proves the
// judge's one-retry contract: a first reply that fails strict JSON
// parsing is retried once, and the retry's score is what the run keeps.
func TestCertifyTaskJudgeRetriesOnceOnAParseFailureThenScores(t *testing.T) {
	candidateFake := ai.NewFakeClient().Script("the widget is blue and durable")
	judgeFake := ai.NewFakeClient().Script("not valid json at all", scoreJSON(80))

	sc := testScenario("basic", wideBands, widgetChecks())
	rec, err := certifyTask(wsContext(t), ai.TaskSummarize, []Scenario{sc}, ai.FakeRoutingConfig(), "", 1, quietLogger(), &certifyHooks{
		candidateOpts: []ai.LocalOption{ai.WithFakeClient(candidateFake)},
		judgeOpts:     []ai.LocalOption{ai.WithFakeClient(judgeFake)},
	})
	if err != nil {
		t.Fatalf("certifyTask: %v", err)
	}
	if rec.ScoreP50 != 80 {
		t.Fatalf("score_p50 = %d, want 80 (the retry's score)", rec.ScoreP50)
	}
	if rec.Verdict != VerdictCertified {
		t.Fatalf("verdict = %q, want %q", rec.Verdict, VerdictCertified)
	}
}

// TestCertifyTaskJudgeScoresZeroWhenBothAttemptsFailToParse proves the
// "then that run scores 0" half of the spec: two consecutive
// unparseable judge replies never abort the run — they just cost it the
// score.
func TestCertifyTaskJudgeScoresZeroWhenBothAttemptsFailToParse(t *testing.T) {
	candidateFake := ai.NewFakeClient().Script("the widget is blue and durable")
	judgeFake := ai.NewFakeClient().Script("still not json", "nope, also not json")

	sc := testScenario("basic", wideBands, widgetChecks())
	rec, err := certifyTask(wsContext(t), ai.TaskSummarize, []Scenario{sc}, ai.FakeRoutingConfig(), "", 1, quietLogger(), &certifyHooks{
		candidateOpts: []ai.LocalOption{ai.WithFakeClient(candidateFake)},
		judgeOpts:     []ai.LocalOption{ai.WithFakeClient(judgeFake)},
	})
	if err != nil {
		t.Fatalf("certifyTask: %v", err)
	}
	if rec.ScoreP50 != 0 || rec.ScoreMin != 0 {
		t.Fatalf("score should be 0 after two failed parses, got p50=%d min=%d", rec.ScoreP50, rec.ScoreMin)
	}
	if rec.Verdict != VerdictNotSupported {
		t.Fatalf("verdict = %q, want %q", rec.Verdict, VerdictNotSupported)
	}
}

// TestCertifyTaskFoldsMultipleScenariosToTheirWorstVerdict pins the
// multi-scenario rollup: Verdict itself is scoped to ONE scenario's odd
// run count (score.go panics on an even N), so a task with 2 scenarios ×
// 3 repeats pools 6 runs total — this proves that pooling never reaches
// Verdict with an even count, while the task's own verdict still folds
// to the worse of its two scenarios.
func TestCertifyTaskFoldsMultipleScenariosToTheirWorstVerdict(t *testing.T) {
	candidateFake := ai.NewFakeClient().Script(
		"the widget is blue", "the widget is blue", "the widget is blue", // scenario 1
		"the widget is blue", "the widget is blue", "the widget is blue", // scenario 2
	)
	judgeFake := ai.NewFakeClient().Script(
		scoreJSON(90), scoreJSON(90), scoreJSON(90), // scenario 1: certified-quality
		scoreJSON(10), scoreJSON(10), scoreJSON(10), // scenario 2: not-supported-quality
	)

	scenarios := []Scenario{
		testScenario("good", wideBands, widgetChecks()),
		testScenario("bad", wideBands, widgetChecks()),
	}
	rec, err := certifyTask(wsContext(t), ai.TaskSummarize, scenarios, ai.FakeRoutingConfig(), "", 3, quietLogger(), &certifyHooks{
		candidateOpts: []ai.LocalOption{ai.WithFakeClient(candidateFake)},
		judgeOpts:     []ai.LocalOption{ai.WithFakeClient(judgeFake)},
	})
	if err != nil {
		t.Fatalf("certifyTask: %v", err)
	}
	if rec.Runs != 6 {
		t.Fatalf("runs = %d, want 6 (2 scenarios x 3 repeats, pooled)", rec.Runs)
	}
	if rec.Verdict != VerdictNotSupported {
		t.Fatalf("verdict = %q, want %q — the task must fold to its worst scenario", rec.Verdict, VerdictNotSupported)
	}
}

// TestCertifyTaskVoidsARecordWhenALaterRunIsServedByADifferentModel
// covers I2: TaskSummarize's ladder is [cheap_cloud, premium]; cheap_cloud
// serves run 1 as "model-a", then fails transiently on run 2 so premium
// serves it instead as "model-b" — a genuine mid-set ladder fallback
// (mirroring the ai package's own TestLadderFallbackBuffersOneLogicalCall-
// WithTwoAttempts at the router level, replayed here through certifyTask's
// pooled accounting). The task must void its record rather than certify
// scores partly produced by one model and partly by another.
func TestCertifyTaskVoidsARecordWhenALaterRunIsServedByADifferentModel(t *testing.T) {
	candidateFake := ai.NewFakeClient().ScriptSteps(
		ai.FakeStep{Text: "the widget is blue and durable", ServedModel: "model-a"}, // run 1: cheap_cloud serves
		ai.FakeStep{Err: errors.New("cheap_cloud: transient provider error")},       // run 2: cheap_cloud fails
		ai.FakeStep{Text: "the widget is blue and durable", ServedModel: "model-b"}, // run 2: premium falls back and serves
	)
	judgeFake := ai.NewFakeClient().Script(scoreJSON(90), scoreJSON(90))

	sc := testScenario("basic", wideBands, widgetChecks())
	_, err := certifyTask(wsContext(t), ai.TaskSummarize, []Scenario{sc}, ai.FakeRoutingConfig(), "", 2, quietLogger(), &certifyHooks{
		candidateOpts: []ai.LocalOption{ai.WithFakeClient(candidateFake)},
		judgeOpts:     []ai.LocalOption{ai.WithFakeClient(judgeFake)},
	})
	if err == nil {
		t.Fatal("want an error — no record for a task whose runs were served by more than one model")
	}
	if !strings.Contains(err.Error(), "model-a") || !strings.Contains(err.Error(), "model-b") {
		t.Fatalf("error should name both identities, got %v", err)
	}
}

// wsContext mints the fixed DB-less workspace principal every router
// call in this package's tests needs, mirroring ensureWorkspace's own
// production behavior so a direct certifyTask call (bypassing Run,
// which calls ensureWorkspace itself) still has one.
func wsContext(t *testing.T) context.Context {
	t.Helper()
	return ensureWorkspace(context.Background())
}
