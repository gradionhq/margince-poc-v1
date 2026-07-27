// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package aicert_test

// The corpus format's own tests. A scenario carries the DATA a site is given
// and the answer the corpus expects back; the site's own code builds the
// request. Every refusal below is a scenario that would otherwise cost a paid
// run to discover and report nothing when it arrived.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose/aicert"
	"github.com/gradionhq/margince/backend/internal/compose/aitasks"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
)

func writeCorpusFile(t *testing.T, dir, name, contents string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

const validScenarioYAML = `
name: basic_summary
task: summarize
site: widget
source: hand_authored
sanitized_by: jane
fixture:
  subject: Summarize this deal.
  history:
    - role: user
      text: hello
    - role: assistant
      text: hi there
expect:
  outcome: accepted
  answer: summary
  rubric: Score how well the summary captures the deal's status.
  bands:
    certified_min: 70
    degraded_min: 50
    floor: 40
  caps:
    p95_latency_ms: 5000
    max_tokens: 500
`

func TestLoadCorpusParsesAScenarioWithAllFields(t *testing.T) {
	dir := t.TempDir()
	writeCorpusFile(t, dir, "summarize/basic_01.yaml", validScenarioYAML)

	scenarios, err := aicert.LoadCorpus(dir, censusFor(t, ai.TaskSummarize))
	if err != nil {
		t.Fatalf("LoadCorpus: %v", err)
	}
	if len(scenarios) != 1 {
		t.Fatalf("got %d scenarios, want 1", len(scenarios))
	}
	sc := scenarios[0]
	if sc.Name != "basic_summary" || sc.Task != "summarize" || sc.Site != stubVariant ||
		sc.Source != "hand_authored" || sc.SanitizedBy != "jane" {
		t.Fatalf("scenario fields wrong: %+v", sc)
	}
	if sc.Expect.Bands != (aicert.Bands{CertifiedMin: 70, DegradedMin: 50, Floor: 40}) {
		t.Fatalf("bands wrong: %+v", sc.Expect.Bands)
	}
	if sc.Expect.Caps.P95LatencyMS != 5000 || sc.Expect.Caps.MaxTokens != 500 {
		t.Fatalf("caps wrong: %+v", sc.Expect.Caps)
	}
	if sc.Expect.Outcome != "accepted" {
		t.Fatalf("outcome = %q, want accepted", sc.Expect.Outcome)
	}
	// The expected answer reaches the site in the site's own vocabulary, which
	// here is a bare JSON string — not a wrapper the harness invented.
	if got := string(sc.Expect.Answer); got != `"summary"` {
		t.Fatalf("answer = %s, want a bare JSON string", got)
	}
	// The fixture is handed to the site as JSON, nesting intact: a site whose
	// Prepare takes a list of turns must receive the list, not a rendering of it.
	var fixture struct {
		Subject string `json:"subject"`
		History []struct {
			Role string `json:"role"`
			Text string `json:"text"`
		} `json:"history"`
	}
	if err := json.Unmarshal(sc.Fixture, &fixture); err != nil {
		t.Fatalf("the fixture is not JSON a site could unmarshal: %v (%s)", err, sc.Fixture)
	}
	if fixture.Subject != "Summarize this deal." {
		t.Fatalf("fixture subject = %q", fixture.Subject)
	}
	if len(fixture.History) != 2 || fixture.History[0].Role != "user" || fixture.History[1].Text != "hi there" {
		t.Fatalf("fixture history wrong: %+v", fixture.History)
	}
}

func TestLoadCorpusRefusesAnUnknownTask(t *testing.T) {
	dir := t.TempDir()
	writeCorpusFile(t, dir, "bogus/one.yaml", `
name: x
task: not_a_real_task
site: widget
source: hand_authored
sanitized_by: jane
fixture: {subject: hi}
expect:
  outcome: accepted
  answer: hi
  bands: {certified_min: 70, degraded_min: 50, floor: 40}
`)
	_, err := aicert.LoadCorpus(dir, censusFor(t, ai.TaskSummarize))
	if err == nil {
		t.Fatal("want an error for an unknown task, got nil")
	}
	if !strings.Contains(err.Error(), "not_a_real_task") {
		t.Fatalf("error %q does not name the offending task", err)
	}
	if !strings.Contains(err.Error(), "one.yaml") {
		t.Fatalf("error %q does not name the offending file", err)
	}
}

// A scenario whose site nobody registered names code this build does not have,
// so nothing could certify it. Refusing it at load is the difference between a
// typo caught in a parse and a typo caught by a paid run that measured nothing.
func TestLoadCorpusRefusesASiteTheCensusDoesNotRegister(t *testing.T) {
	dir := t.TempDir()
	writeCorpusFile(t, dir, "summarize/one.yaml", `
name: x
task: summarize
site: invented
source: hand_authored
sanitized_by: jane
fixture: {subject: hi}
expect:
  outcome: accepted
  answer: hi
  bands: {certified_min: 70, degraded_min: 50, floor: 40}
`)
	_, err := aicert.LoadCorpus(dir, censusFor(t, ai.TaskSummarize))
	if err == nil {
		t.Fatal("want an error for a site the census does not register, got nil")
	}
	if !strings.Contains(err.Error(), "invented") {
		t.Fatalf("error %q does not name the offending site", err)
	}
}

func TestLoadCorpusRefusesAScenarioThatNamesNoSite(t *testing.T) {
	dir := t.TempDir()
	writeCorpusFile(t, dir, "summarize/one.yaml", `
name: x
task: summarize
source: hand_authored
sanitized_by: jane
fixture: {subject: hi}
expect:
  outcome: accepted
  answer: hi
  bands: {certified_min: 70, degraded_min: 50, floor: 40}
`)
	_, err := aicert.LoadCorpus(dir, censusFor(t, ai.TaskSummarize))
	if err == nil {
		t.Fatal("want an error for a scenario naming no site, got nil")
	}
	if !strings.Contains(err.Error(), "site") {
		t.Fatalf("error %q does not name the missing field", err)
	}
}

// The corpus holds fixtures, not prompts. A scenario still carrying a prompt
// field is one nobody converted, and loading it would certify a request the
// product never sends.
func TestLoadCorpusRefusesAScenarioCarryingAPrompt(t *testing.T) {
	dir := t.TempDir()
	writeCorpusFile(t, dir, "summarize/one.yaml", `
name: x
task: summarize
site: widget
source: hand_authored
sanitized_by: jane
system: You are a helpful CRM assistant.
fixture: {subject: hi}
expect:
  outcome: accepted
  answer: hi
  bands: {certified_min: 70, degraded_min: 50, floor: 40}
`)
	_, err := aicert.LoadCorpus(dir, censusFor(t, ai.TaskSummarize))
	if err == nil {
		t.Fatal("want an error for a scenario carrying a system prompt, got nil")
	}
	if !strings.Contains(err.Error(), "system") {
		t.Fatalf("error %q does not name the offending field", err)
	}
}

func TestLoadCorpusRefusesAScenarioWithNoFixture(t *testing.T) {
	dir := t.TempDir()
	writeCorpusFile(t, dir, "summarize/one.yaml", `
name: x
task: summarize
site: widget
source: hand_authored
sanitized_by: jane
fixture:
expect:
  outcome: accepted
  answer: hi
  bands: {certified_min: 70, degraded_min: 50, floor: 40}
`)
	_, err := aicert.LoadCorpus(dir, censusFor(t, ai.TaskSummarize))
	if err == nil {
		t.Fatal("want an error for a scenario with no fixture, got nil")
	}
	if !strings.Contains(err.Error(), "fixture") {
		t.Fatalf("error %q does not name the missing field", err)
	}
}

// An outcome outside the seam's four words asserts something no Evaluate can
// ever report.
func TestLoadCorpusRefusesAnUnknownExpectedOutcome(t *testing.T) {
	dir := t.TempDir()
	writeCorpusFile(t, dir, "summarize/one.yaml", `
name: x
task: summarize
site: widget
source: hand_authored
sanitized_by: jane
fixture: {subject: hi}
expect:
  outcome: pretty_good
  answer: hi
  bands: {certified_min: 70, degraded_min: 50, floor: 40}
`)
	_, err := aicert.LoadCorpus(dir, censusFor(t, ai.TaskSummarize))
	if err == nil {
		t.Fatal("want an error for an unknown expect.outcome, got nil")
	}
	if !strings.Contains(err.Error(), "pretty_good") {
		t.Fatalf("error %q does not name the offending value", err)
	}
	for _, want := range []string{
		aitasks.OutcomeAccepted,
		aitasks.OutcomeWrongAnswer,
		aitasks.OutcomeInvalid,
		aitasks.OutcomeAbstained,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not offer the %q vocabulary", err, want)
		}
	}
}

// A scenario whose right answer is silence must load: expect.outcome carries the
// seam's abstention word, and the runner passes a run whose reported outcome
// equals it. Without this the corpus could hold every claim about what a model
// must SAY and none about what it must not.
func TestLoadCorpusAcceptsAnAbstentionOutcome(t *testing.T) {
	dir := t.TempDir()
	writeCorpusFile(t, dir, "summarize/abstain.yaml", `
name: x
task: summarize
site: widget
source: hand_authored
sanitized_by: jane
fixture: {subject: hi}
expect:
  outcome: abstained
  answer: {}
  bands: {certified_min: 70, degraded_min: 50, floor: 40}
`)
	scenarios, err := aicert.LoadCorpus(dir, censusFor(t, ai.TaskSummarize))
	if err != nil {
		t.Fatalf("LoadCorpus refused an abstention scenario: %v", err)
	}
	if len(scenarios) != 1 {
		t.Fatalf("loaded %d scenarios, want 1", len(scenarios))
	}
	if scenarios[0].Expect.Outcome != aitasks.OutcomeAbstained {
		t.Fatalf("outcome = %q, want %q", scenarios[0].Expect.Outcome, aitasks.OutcomeAbstained)
	}
}

// A corpus is only meaningful against the build that certifies it, so a caller
// with no census is refused rather than served a corpus nobody could run.
func TestLoadCorpusRefusesAMissingCensus(t *testing.T) {
	dir := t.TempDir()
	writeCorpusFile(t, dir, "summarize/basic_01.yaml", validScenarioYAML)

	_, err := aicert.LoadCorpus(dir, nil)
	if err == nil {
		t.Fatal("want an error when no census is supplied, got nil")
	}
	if !strings.Contains(err.Error(), "census") {
		t.Fatalf("error %q does not say what is missing", err)
	}
}

func TestLoadCorpusRefusesAnExtractedSource(t *testing.T) {
	dir := t.TempDir()
	writeCorpusFile(t, dir, "summarize/one.yaml", `
name: x
task: summarize
site: widget
source: "extracted:0198c1c2-0000-7000-8000-000000000000"
sanitized_by: jane
fixture: {subject: hi}
expect:
  outcome: accepted
  answer: hi
  bands: {certified_min: 70, degraded_min: 50, floor: 40}
`)
	_, err := aicert.LoadCorpus(dir, censusFor(t, ai.TaskSummarize))
	if err == nil {
		t.Fatal("want an error for an extracted: source, got nil")
	}
	if !strings.Contains(err.Error(), "extracted:") {
		t.Fatalf("error %q does not name the refused source", err)
	}
}

func TestLoadCorpusRefusesAnUnrecognizedSource(t *testing.T) {
	dir := t.TempDir()
	writeCorpusFile(t, dir, "summarize/one.yaml", `
name: x
task: summarize
site: widget
source: made_up
sanitized_by: jane
fixture: {subject: hi}
expect:
  outcome: accepted
  answer: hi
  bands: {certified_min: 70, degraded_min: 50, floor: 40}
`)
	_, err := aicert.LoadCorpus(dir, censusFor(t, ai.TaskSummarize))
	if err == nil {
		t.Fatal("want an error for an unrecognized source, got nil")
	}
}

func TestLoadCorpusRefusesAMissingSignOff(t *testing.T) {
	dir := t.TempDir()
	writeCorpusFile(t, dir, "summarize/one.yaml", `
name: x
task: summarize
site: widget
source: hand_authored
sanitized_by: ""
fixture: {subject: hi}
expect:
  outcome: accepted
  answer: hi
  bands: {certified_min: 70, degraded_min: 50, floor: 40}
`)
	_, err := aicert.LoadCorpus(dir, censusFor(t, ai.TaskSummarize))
	if err == nil {
		t.Fatal("want an error for a missing sanitized_by, got nil")
	}
	if !strings.Contains(err.Error(), "sanitized_by") {
		t.Fatalf("error %q does not name the missing field", err)
	}
}

func TestLoadCorpusRefusesAnUnknownTopLevelField(t *testing.T) {
	dir := t.TempDir()
	writeCorpusFile(t, dir, "summarize/one.yaml", `
name: x
task: summarize
site: widget
source: hand_authored
sanitized_by: jane
fixture: {subject: hi}
bogus_field: oops
expect:
  outcome: accepted
  answer: hi
  bands: {certified_min: 70, degraded_min: 50, floor: 40}
`)
	if _, err := aicert.LoadCorpus(dir, censusFor(t, ai.TaskSummarize)); err == nil {
		t.Fatal("want an error for an unknown top-level field, got nil")
	}
}

// The unknown-field gate reaches every level BELOW the fixture too: a typo'd
// band is a band that silently stops gating anything.
func TestLoadCorpusRefusesAnUnknownFieldInsideExpect(t *testing.T) {
	dir := t.TempDir()
	writeCorpusFile(t, dir, "summarize/one.yaml", `
name: x
task: summarize
site: widget
source: hand_authored
sanitized_by: jane
fixture: {subject: hi}
expect:
  outcome: accepted
  answer: hi
  bands: {certified_min: 70, degraded_min: 50, floor: 40, typo_min: 10}
`)
	if _, err := aicert.LoadCorpus(dir, censusFor(t, ai.TaskSummarize)); err == nil {
		t.Fatal("want an error for an unknown field inside expect.bands, got nil")
	}
}

// A fixture's own keys are the SITE's business, so the unknown-field gate stops
// at the fixture boundary: every site takes a different shape, and this package
// knows none of them.
func TestLoadCorpusAcceptsAnyShapeInsideTheFixture(t *testing.T) {
	dir := t.TempDir()
	writeCorpusFile(t, dir, "summarize/one.yaml", `
name: x
task: summarize
site: widget
source: hand_authored
sanitized_by: jane
fixture:
  subject: hi
  pages:
    - url: https://example.test/pricing
      kind: pricing
      lines: [a, b]
expect:
  outcome: accepted
  answer: hi
  bands: {certified_min: 70, degraded_min: 50, floor: 40}
`)
	scenarios, err := aicert.LoadCorpus(dir, censusFor(t, ai.TaskSummarize))
	if err != nil {
		t.Fatalf("LoadCorpus: %v", err)
	}
	if len(scenarios) != 1 {
		t.Fatalf("got %d scenarios, want 1", len(scenarios))
	}
	if !strings.Contains(string(scenarios[0].Fixture), `"url":"https://example.test/pricing"`) {
		t.Fatalf("the fixture lost its nested shape: %s", scenarios[0].Fixture)
	}
}

func TestLoadCorpusOnAnEmptyDirectoryReturnsNoScenariosAndNoError(t *testing.T) {
	dir := t.TempDir()
	scenarios, err := aicert.LoadCorpus(dir, censusFor(t, ai.TaskSummarize))
	if err != nil {
		t.Fatalf("LoadCorpus on an empty dir: %v", err)
	}
	if len(scenarios) != 0 {
		t.Fatalf("got %d scenarios from an empty dir, want 0", len(scenarios))
	}
}

func TestLoadCorpusSkipsNonYAMLFilesLikeFixtureAssets(t *testing.T) {
	dir := t.TempDir()
	writeCorpusFile(t, dir, "site_extract/fixtures/page.html", "<html></html>")
	writeCorpusFile(t, dir, "site_extract/basic_01.yaml", `
name: x
task: site_extract
site: widget
source: hand_authored
sanitized_by: jane
fixture: {subject: hi}
expect:
  outcome: accepted
  answer: hi
  bands: {certified_min: 70, degraded_min: 50, floor: 40}
`)
	scenarios, err := aicert.LoadCorpus(dir, censusFor(t, ai.TaskSiteExtract))
	if err != nil {
		t.Fatalf("LoadCorpus: %v", err)
	}
	if len(scenarios) != 1 {
		t.Fatalf("got %d scenarios, want 1 (the fixture .html must not be parsed as a scenario)", len(scenarios))
	}
}
