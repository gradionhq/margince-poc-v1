// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package aicert

// A certification record is a claim about specific scenarios AND about the
// requests this build builds from them. These tests keep that claim falsifiable:
// the stamp must move when any part of the claim moves — including the part that
// lives in the product's own code — and a committed record whose stamp does not
// match this build must be reported as no longer describing what ships.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/compose/aitasks"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/promptfence"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// stamp is PromptVersion with the error folded into the test's own failure —
// every call below supplies a census that binds every scenario's site, so an
// error here is the test being wrong, not the claim.
func stamp(t *testing.T, scenarios []Scenario, census *aitasks.Registry) string {
	t.Helper()
	got, err := PromptVersion(context.Background(), scenarios, census)
	if err != nil {
		t.Fatalf("PromptVersion: %v", err)
	}
	return got
}

// Every part of a scenario changes what a score MEANS: the fixture is the data
// the site is given, the expected answer decides what "right" is, and the bands
// decide what passes. A stamp that moved for only one of them would leave the
// other two able to change under a record still claiming to cover them.
func TestPromptVersionMovesWithEveryPartOfTheClaim(t *testing.T) {
	census := testCensus(t)
	base := testScenario("one", wideBands)
	base.Expect.Rubric = "Score the grounding."
	before := stamp(t, []Scenario{base}, census)

	edits := map[string]func(sc *Scenario){
		"the fixture": func(sc *Scenario) { sc.Fixture = JSONValue(`{"subject":"a different widget"}`) },
		"the expected answer": func(sc *Scenario) {
			sc.Expect.Answer = JSONValue(`"` + widgetAbstention + `"`)
		},
		"the rubric": func(sc *Scenario) { sc.Expect.Rubric = "Score the grounding leniently." },
		"the bands":  func(sc *Scenario) { sc.Expect.Bands.CertifiedMin = 60 },
		"the caps":   func(sc *Scenario) { sc.Expect.Caps.MaxTokens = 300 },
	}
	for what, edit := range edits {
		t.Run(what, func(t *testing.T) {
			edited := base
			edit(&edited)
			if stamp(t, []Scenario{edited}, census) == before {
				t.Fatalf("editing %s kept the certification stamp — a stale record could not be detected", what)
			}
		})
	}
}

// The half a fixture corpus otherwise loses: the corpus holds the data a site is
// GIVEN, and the site's own code turns it into the prompt. Two builds that send
// different prompts for the same scenario must not share a stamp, or a record
// goes on claiming to certify wording that has been deleted.
func TestPromptVersionMovesWhenTheRequestTheSiteBuildsMoves(t *testing.T) {
	sc := testScenarioOnSite("one", promptVariant, wideBands)

	before := stamp(t, []Scenario{sc}, promptCensus(t, "Describe the subject in one sentence.", 1024))
	after := stamp(t, []Scenario{sc}, promptCensus(t, "Describe the subject in two sentences.", 1024))
	if before == after {
		t.Fatal("editing the system prompt the site sends kept the stamp — a record would certify wording the build no longer sends")
	}

	tighter := stamp(t, []Scenario{sc}, promptCensus(t, "Describe the subject in one sentence.", 256))
	if tighter == before {
		t.Fatal("changing the answer ceiling the site asks for kept the stamp — the model is handed a different request under the same claim")
	}
}

// The other half a record's verdict rests on: score_p50, the band it lands in,
// and therefore the whole claim come from the GRADER, so a build that grades
// differently must not go on matching a record scored under the old grader.
//
// The grader's prompt is this build's own text and no census can hand the stamp
// a different one, so the claim is checked in its two parts: that the stamp's
// grader half is the request compose.JudgeRequest actually returns for this
// scenario, and that that digest separates two grader requests differing only in
// their system prompt. Together they say an edit to the grader's instruction
// moves the stamp.
func TestPromptVersionCoversTheRequestTheGraderIsSent(t *testing.T) {
	sc := testScenarioOnSite("one", promptVariant, wideBands)
	sc.Expect.Rubric = "Score the grounding."
	candidate := model.Request{
		System:    "Describe the subject in one sentence.",
		Messages:  []model.Message{{Role: roleUser, Content: "a widget"}},
		MaxTokens: 1024,
	}

	got, err := graderRequestDigest(sc, candidate)
	if err != nil {
		t.Fatalf("graderRequestDigest: %v", err)
	}
	shipped := compose.JudgeRequest(sc.Expect.Rubric, "a widget", stampCandidateOutput)
	want, err := canonicalRequestDigest(shipped)
	if err != nil {
		t.Fatalf("digesting the shipped grader request: %v", err)
	}
	if got != want {
		t.Fatalf("the stamp's grader half is %s and the request compose.JudgeRequest builds digests to %s — the stamp covers a grading call this build does not make", got, want)
	}

	edited := compose.JudgeRequest(sc.Expect.Rubric, "a widget", stampCandidateOutput)
	edited.System = "Grade generously.\n" + edited.System
	generous, err := canonicalRequestDigest(edited)
	if err != nil {
		t.Fatalf("digesting the edited grader request: %v", err)
	}
	if generous == want {
		t.Fatal("a grader instructed differently digests the same — editing the grader's system prompt would leave every record claiming to certify scores it can no longer produce")
	}
}

// The grader is shown the turn the candidate was given, so a case that asks the
// model nothing leaves nothing to grade — and judgeScore fails a run for exactly
// that. A stamp over such a case would be certifying a grading call that cannot
// be built, which is worse than refusing it.
func TestPromptVersionRefusesACaseTheGraderCouldNotBeBuiltFrom(t *testing.T) {
	site := aitasks.Site{Task: ai.TaskSummarize, Variant: promptVariant, Kind: ai.SiteKindOneShot}
	census := aitasks.NewRegistry()
	census.Register(site)
	census.BindCase(site, systemOnlyCases{site: site})

	_, err := PromptVersion(context.Background(), []Scenario{testScenarioOnSite("one", promptVariant, wideBands)}, census)
	if err == nil || !strings.Contains(err.Error(), "carries no user turn") {
		t.Fatalf("want a refusal naming the missing ask, got %v", err)
	}
}

// A site mints two things per call that a scenario cannot carry: the data
// boundary, and the id it tells the model to answer that data by. The grader
// mints a boundary of its own on top. All of them differ between two sends of
// the SAME prompt, so a stamp that moved with them would call every record stale
// the moment it was written.
func TestPromptVersionIgnoresWhatEachCallMintsForItself(t *testing.T) {
	sc := testScenarioOnSite("one", promptVariant, wideBands)
	census := fencedCensus(t)
	first := stamp(t, []Scenario{sc}, census)
	again := stamp(t, []Scenario{sc}, census)
	if first != again {
		t.Fatalf("two stamps over one corpus disagree (%s, %s) — a per-call fence nonce or record id is reaching the digest", first, again)
	}
}

func TestPromptVersionIsOrderIndependent(t *testing.T) {
	census := testCensus(t)
	a := testScenario("a", wideBands)
	b := testScenario("b", wideBands)
	b.Fixture = JSONValue(`{"subject":"another widget"}`)
	if stamp(t, []Scenario{a, b}, census) != stamp(t, []Scenario{b, a}, census) {
		t.Fatal("the stamp depends on scenario order, so a reordered corpus reads as a scenario change")
	}
}

// A stamp with an empty request half would match forever, which is the exact
// failure this stamp exists to remove — so a case that builds no request is
// refused rather than stamped on its scenario alone.
func TestPromptVersionRefusesACaseThatBuildsNoRequest(t *testing.T) {
	site := aitasks.Site{Task: ai.TaskSummarize, Variant: promptVariant, Kind: ai.SiteKindOneShot}
	census := aitasks.NewRegistry()
	census.Register(site)
	census.BindCase(site, silentCases{site: site})

	_, err := PromptVersion(context.Background(), []Scenario{testScenarioOnSite("one", promptVariant, wideBands)}, census)
	if err == nil || !strings.Contains(err.Error(), "without building a request") {
		t.Fatalf("want a refusal naming the missing request, got %v", err)
	}
}

func TestPromptVersionRefusesAScenarioNoCaseServes(t *testing.T) {
	_, err := PromptVersion(context.Background(), []Scenario{testScenarioOnSite("one", "a_site_nobody_built", wideBands)}, testCensus(t))
	if err == nil || !strings.Contains(err.Error(), "a_site_nobody_built") {
		t.Fatalf("want a refusal naming the unbound site, got %v", err)
	}
}

// The stamp is only a staleness signal if it is stable: a case whose request
// carries anything minted per run — an id, a timestamp — would move the stamp on
// every read, and every record would read as stale for no reason a reader could
// act on. This is the whole shipped corpus, driven through the shipped cases.
func TestTheShippedCorpusStampsTheSameTwice(t *testing.T) {
	census, err := compose.NewTaskCensus()
	if err != nil {
		t.Fatalf("building the task census: %v", err)
	}
	scenarios, err := LoadCorpus("corpus", census)
	if err != nil {
		t.Fatalf("load corpus: %v", err)
	}
	for task, ofTask := range groupCorpusByTask(scenarios) {
		first := stamp(t, ofTask, census)
		again := stamp(t, ofTask, census)
		if first != again {
			t.Errorf("task %s stamps differently on two reads of the same corpus (%s, %s) — something in the request it builds is minted per run",
				task, first, again)
		}
	}
}

// The staleness report: which committed records were scored against something
// this build no longer sends. It is a test rather than prose in a status file so
// the answer is computed from the tree, and it FAILS while any record still
// claims otherwise — the fix is a re-certification run (see records/README.md),
// not an edit here.
func TestEveryCommittedRecordNamesTheCurrentPromptVersion(t *testing.T) {
	census, err := compose.NewTaskCensus()
	if err != nil {
		t.Fatalf("building the task census: %v", err)
	}
	scenarios, err := LoadCorpus("corpus", census)
	if err != nil {
		t.Fatalf("load corpus: %v", err)
	}
	byTask := groupCorpusByTask(scenarios)
	records, err := LoadRecords("records")
	if err != nil {
		t.Fatalf("load records: %v", err)
	}
	var stale []string
	for _, rec := range records {
		current, ok := byTask[rec.Task]
		if !ok {
			// A record for a task with no scenarios cannot be checked against
			// anything; the corpus-coverage test owns that case.
			continue
		}
		if want := stamp(t, current, census); rec.PromptVersion != want {
			stale = append(stale, rec.Task+"/"+rec.Provider+"_"+rec.ServedModel+
				" was certified against "+rec.PromptVersion+", this build is "+want)
		}
	}
	if len(stale) > 0 {
		t.Errorf("these certification records were scored against scenarios or prompts that have since changed, so they no longer describe what ships — re-run certification for these tasks:\n  %s",
			strings.Join(stale, "\n  "))
	}
}

func groupCorpusByTask(scenarios []Scenario) map[string][]Scenario {
	byTask := map[string][]Scenario{}
	for _, sc := range scenarios {
		byTask[sc.Task] = append(byTask[sc.Task], sc)
	}
	return byTask
}

// promptVariant names the stand-in site the stamp tests certify. It is its own
// site because these tests change what the SITE sends, which the widget case
// (shared with the runner's tests) must not do.
const promptVariant = "prompt_widget"

// promptCases stands in for a site's own request builder: the system prompt and
// the answer ceiling are the product's, not the corpus's, so a test can change
// what this build sends without touching a scenario.
type promptCases struct {
	site      aitasks.Site
	system    string
	maxTokens int
	fenced    bool
}

func (c promptCases) Site() aitasks.Site { return c.site }

func (c promptCases) Prepare(fixture, _ json.RawMessage) (aitasks.PreparedCase, error) {
	var f struct {
		Subject string `json:"subject"`
	}
	if err := json.Unmarshal(fixture, &f); err != nil {
		return nil, err
	}
	system, subject := c.system, f.Subject
	if c.fenced {
		// One fence and one row id per call, exactly as a shipped site mints
		// them: the marker is named in this call's own system prompt, and the
		// data is identified by an id the corpus never supplied.
		fence := promptfence.New()
		system += " " + fence.Rule("subject")
		subject = fence.WrapAttr("source_id", ids.NewV7().String(), subject)
	}
	return promptCase{system: system, subject: subject, maxTokens: c.maxTokens}, nil
}

type promptCase struct {
	system    string
	subject   string
	maxTokens int
}

func (c promptCase) Run(ctx context.Context, completer aitasks.Completer) (aitasks.Trace, error) {
	req := model.Request{
		System:    c.system,
		Messages:  []model.Message{{Role: "user", Content: c.subject}},
		MaxTokens: c.maxTokens,
	}
	trace := aitasks.Trace{Requests: []model.Request{req}}
	resp, err := completer.Complete(ctx, req)
	if err != nil {
		return trace, err
	}
	trace.Output = resp.Text
	return trace, nil
}

func (promptCase) Evaluate(aitasks.Trace) aitasks.Outcome {
	return aitasks.Outcome{Result: aitasks.OutcomeAccepted}
}

// systemOnlyCases is the case that instructs the model and asks it nothing: its
// request exists, so the candidate half of the stamp is fine, and there is still
// no turn a grader could be shown as the question under grading.
type systemOnlyCases struct{ site aitasks.Site }

func (c systemOnlyCases) Site() aitasks.Site { return c.site }

func (systemOnlyCases) Prepare(json.RawMessage, json.RawMessage) (aitasks.PreparedCase, error) {
	return systemOnlyCase{}, nil
}

type systemOnlyCase struct{}

func (systemOnlyCase) Run(ctx context.Context, completer aitasks.Completer) (aitasks.Trace, error) {
	req := model.Request{System: "Describe the subject in one sentence.", MaxTokens: 1024}
	trace := aitasks.Trace{Requests: []model.Request{req}}
	resp, err := completer.Complete(ctx, req)
	if err != nil {
		return trace, err
	}
	trace.Output = resp.Text
	return trace, nil
}

func (systemOnlyCase) Evaluate(aitasks.Trace) aitasks.Outcome {
	return aitasks.Outcome{Result: aitasks.OutcomeAccepted}
}

// silentCases is the case that answers without ever asking: it has nothing this
// stamp can cover.
type silentCases struct{ site aitasks.Site }

func (c silentCases) Site() aitasks.Site { return c.site }

func (silentCases) Prepare(json.RawMessage, json.RawMessage) (aitasks.PreparedCase, error) {
	return silentCase{}, nil
}

type silentCase struct{}

func (silentCase) Run(context.Context, aitasks.Completer) (aitasks.Trace, error) {
	return aitasks.Trace{Output: "answered without asking"}, nil
}

func (silentCase) Evaluate(aitasks.Trace) aitasks.Outcome {
	return aitasks.Outcome{Result: aitasks.OutcomeAccepted}
}

// promptCensus binds promptVariant to a site that sends the given system prompt
// under the given answer ceiling.
func promptCensus(t *testing.T, system string, maxTokens int) *aitasks.Registry {
	t.Helper()
	return censusOfPromptCases(t, promptCases{
		site:      aitasks.Site{Task: ai.TaskSummarize, Variant: promptVariant, Kind: ai.SiteKindOneShot},
		system:    system,
		maxTokens: maxTokens,
	})
}

// fencedCensus binds promptVariant to a site that mints a fresh data boundary
// per call, which is what every shipped site showing a model captured text does.
func fencedCensus(t *testing.T) *aitasks.Registry {
	t.Helper()
	return censusOfPromptCases(t, promptCases{
		site:      aitasks.Site{Task: ai.TaskSummarize, Variant: promptVariant, Kind: ai.SiteKindOneShot},
		system:    "Describe the subject in one sentence.",
		maxTokens: 1024,
		fenced:    true,
	})
}

func censusOfPromptCases(t *testing.T, cases promptCases) *aitasks.Registry {
	t.Helper()
	r := aitasks.NewRegistry()
	r.Register(cases.site)
	r.BindCase(cases.site, cases)
	return r
}
