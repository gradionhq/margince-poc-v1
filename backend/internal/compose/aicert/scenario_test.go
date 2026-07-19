// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package aicert_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose/aicert"
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
source: hand_authored
sanitized_by: jane
system: You are a helpful CRM assistant.
history:
  - role: user
    text: hello
  - role: assistant
    text: hi there
input: Summarize this deal.
expect:
  structural:
    - kind: contains
      arg: summary
    - kind: not_contains
      arg: TODO
    - kind: min_facts
      arg: "2"
    - kind: json_schema
      schema:
        type: object
        properties:
          summary:
            type: string
        required: [summary]
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

	scenarios, err := aicert.LoadCorpus(dir)
	if err != nil {
		t.Fatalf("LoadCorpus: %v", err)
	}
	if len(scenarios) != 1 {
		t.Fatalf("got %d scenarios, want 1", len(scenarios))
	}
	sc := scenarios[0]
	if sc.Name != "basic_summary" || sc.Task != "summarize" || sc.Source != "hand_authored" || sc.SanitizedBy != "jane" {
		t.Fatalf("scenario fields wrong: %+v", sc)
	}
	if len(sc.History) != 2 || sc.History[0].Role != "user" || sc.History[1].Text != "hi there" {
		t.Fatalf("history wrong: %+v", sc.History)
	}
	if sc.Expect.Bands != (aicert.Bands{CertifiedMin: 70, DegradedMin: 50, Floor: 40}) {
		t.Fatalf("bands wrong: %+v", sc.Expect.Bands)
	}
	if sc.Expect.Caps.P95LatencyMS != 5000 || sc.Expect.Caps.MaxTokens != 500 {
		t.Fatalf("caps wrong: %+v", sc.Expect.Caps)
	}
	if len(sc.Expect.Structural) != 4 {
		t.Fatalf("got %d structural checks, want 4", len(sc.Expect.Structural))
	}
	schemaCheck := sc.Expect.Structural[3]
	if schemaCheck.Kind != "json_schema" || len(schemaCheck.Schema) == 0 {
		t.Fatalf("json_schema check not decoded: %+v", schemaCheck)
	}
	if !strings.Contains(string(schemaCheck.Schema), `"summary"`) {
		t.Fatalf("schema json missing expected content: %s", schemaCheck.Schema)
	}
}

func TestLoadCorpusRefusesAnUnknownTask(t *testing.T) {
	dir := t.TempDir()
	writeCorpusFile(t, dir, "bogus/one.yaml", `
name: x
task: not_a_real_task
source: hand_authored
sanitized_by: jane
input: hi
expect:
  bands: {certified_min: 70, degraded_min: 50, floor: 40}
`)
	_, err := aicert.LoadCorpus(dir)
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

func TestLoadCorpusRefusesAnExtractedSource(t *testing.T) {
	dir := t.TempDir()
	writeCorpusFile(t, dir, "summarize/one.yaml", `
name: x
task: summarize
source: "extracted:0198c1c2-0000-7000-8000-000000000000"
sanitized_by: jane
input: hi
expect:
  bands: {certified_min: 70, degraded_min: 50, floor: 40}
`)
	_, err := aicert.LoadCorpus(dir)
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
source: made_up
sanitized_by: jane
input: hi
expect:
  bands: {certified_min: 70, degraded_min: 50, floor: 40}
`)
	_, err := aicert.LoadCorpus(dir)
	if err == nil {
		t.Fatal("want an error for an unrecognized source, got nil")
	}
}

func TestLoadCorpusRefusesAMissingSignOff(t *testing.T) {
	dir := t.TempDir()
	writeCorpusFile(t, dir, "summarize/one.yaml", `
name: x
task: summarize
source: hand_authored
sanitized_by: ""
input: hi
expect:
  bands: {certified_min: 70, degraded_min: 50, floor: 40}
`)
	_, err := aicert.LoadCorpus(dir)
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
source: hand_authored
sanitized_by: jane
input: hi
bogus_field: oops
expect:
  bands: {certified_min: 70, degraded_min: 50, floor: 40}
`)
	if _, err := aicert.LoadCorpus(dir); err == nil {
		t.Fatal("want an error for an unknown top-level field, got nil")
	}
}

func TestLoadCorpusRefusesAnUnknownCheckField(t *testing.T) {
	dir := t.TempDir()
	writeCorpusFile(t, dir, "summarize/one.yaml", `
name: x
task: summarize
source: hand_authored
sanitized_by: jane
input: hi
expect:
  structural:
    - kind: contains
      arg: hi
      typo_field: oops
  bands: {certified_min: 70, degraded_min: 50, floor: 40}
`)
	if _, err := aicert.LoadCorpus(dir); err == nil {
		t.Fatal("want an error for an unknown check field, got nil")
	}
}

func TestLoadCorpusRefusesOmittedBands(t *testing.T) {
	dir := t.TempDir()
	writeCorpusFile(t, dir, "summarize/one.yaml", `
name: x
task: summarize
source: hand_authored
sanitized_by: jane
input: hi
expect: {}
`)
	_, err := aicert.LoadCorpus(dir)
	if err == nil {
		t.Fatal("want an error for omitted bands, got nil")
	}
	if !strings.Contains(err.Error(), "certified_min") {
		t.Fatalf("error %q does not name the missing bands field", err)
	}
	if !strings.Contains(err.Error(), "one.yaml") {
		t.Fatalf("error %q does not name the offending file", err)
	}
}

func TestLoadCorpusRefusesADegradedMinAboveCertifiedMin(t *testing.T) {
	dir := t.TempDir()
	writeCorpusFile(t, dir, "summarize/one.yaml", `
name: x
task: summarize
source: hand_authored
sanitized_by: jane
input: hi
expect:
  bands: {certified_min: 50, degraded_min: 70, floor: 40}
`)
	_, err := aicert.LoadCorpus(dir)
	if err == nil {
		t.Fatal("want an error for degraded_min above certified_min, got nil")
	}
	if !strings.Contains(err.Error(), "degraded_min") {
		t.Fatalf("error %q does not name the offending field", err)
	}
}

func TestLoadCorpusRefusesAFloorAboveDegradedMin(t *testing.T) {
	dir := t.TempDir()
	writeCorpusFile(t, dir, "summarize/one.yaml", `
name: x
task: summarize
source: hand_authored
sanitized_by: jane
input: hi
expect:
  bands: {certified_min: 70, degraded_min: 50, floor: 60}
`)
	_, err := aicert.LoadCorpus(dir)
	if err == nil {
		t.Fatal("want an error for floor above degraded_min, got nil")
	}
	if !strings.Contains(err.Error(), "floor") {
		t.Fatalf("error %q does not name the offending field", err)
	}
}

func TestLoadCorpusAcceptsAValidOrderedBandsTriple(t *testing.T) {
	dir := t.TempDir()
	writeCorpusFile(t, dir, "summarize/one.yaml", `
name: x
task: summarize
source: hand_authored
sanitized_by: jane
input: hi
expect:
  bands: {certified_min: 70, degraded_min: 50, floor: 40}
`)
	scenarios, err := aicert.LoadCorpus(dir)
	if err != nil {
		t.Fatalf("LoadCorpus: %v", err)
	}
	if len(scenarios) != 1 {
		t.Fatalf("got %d scenarios, want 1", len(scenarios))
	}
	if scenarios[0].Expect.Bands != (aicert.Bands{CertifiedMin: 70, DegradedMin: 50, Floor: 40}) {
		t.Fatalf("bands wrong: %+v", scenarios[0].Expect.Bands)
	}
}

func TestLoadCorpusOnAnEmptyDirectoryReturnsNoScenariosAndNoError(t *testing.T) {
	dir := t.TempDir()
	scenarios, err := aicert.LoadCorpus(dir)
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
source: hand_authored
sanitized_by: jane
input: hi
expect:
  bands: {certified_min: 70, degraded_min: 50, floor: 40}
`)
	scenarios, err := aicert.LoadCorpus(dir)
	if err != nil {
		t.Fatalf("LoadCorpus: %v", err)
	}
	if len(scenarios) != 1 {
		t.Fatalf("got %d scenarios, want 1 (the fixture .html must not be parsed as a scenario)", len(scenarios))
	}
}
