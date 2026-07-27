// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package aicert_test

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose/aicert"
)

func sampleRecord() aicert.Record {
	return aicert.Record{
		Task:                 "summarize",
		Provider:             "anthropic",
		ServedModel:          "claude-sonnet-5",
		EnvClass:             "cloud",
		PromptVersion:        "v1",
		CorpusVersion:        "v1",
		Verdict:              aicert.VerdictCertified,
		Runs:                 3,
		Reliability:          1,
		ScoreP50:             85,
		ScoreMin:             80,
		LatencyP50:           1200,
		LatencyP95:           1500,
		MeanTokens:           300,
		MeanTokensIn:         250,
		MeanTokensOut:        50,
		MeanCachedTokens:     20,
		MeanCacheWriteTokens: 5,
		EstCostMicroUSD:      4200,
		JudgeServedModel:     "claude-opus-4",
		SelfJudged:           false,
		ServedIdentitySource: "response",
		RanAt:                "2026-07-18T00:00:00Z",
	}
}

func TestWriteRecordThenLoadRecordsRoundTrips(t *testing.T) {
	dir := t.TempDir()
	want := sampleRecord()
	if err := aicert.WriteRecord(dir, want); err != nil {
		t.Fatalf("WriteRecord: %v", err)
	}
	got, err := aicert.LoadRecords(dir)
	if err != nil {
		t.Fatalf("LoadRecords: %v", err)
	}
	if len(got) != 1 || !reflect.DeepEqual(got[0], want) {
		t.Fatalf("got %+v, want [%+v]", got, want)
	}
}

func TestWriteRecordPathSanitizesFilesystemHostileCharacters(t *testing.T) {
	dir := t.TempDir()
	r := sampleRecord()
	r.Provider = "fireworks"
	r.ServedModel = "accounts/fireworks/models/llama-v3-70b-instruct"
	r.EnvClass = "cloud:eu"
	if err := aicert.WriteRecord(dir, r); err != nil {
		t.Fatalf("WriteRecord: %v", err)
	}
	taskDir := filepath.Join(dir, "summarize")
	entries, err := os.ReadDir(taskDir)
	if err != nil {
		t.Fatalf("read %s: %v", taskDir, err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d files under %s, want 1: %v", len(entries), taskDir, entries)
	}
	name := entries[0].Name()
	if name != "fireworks_accounts_fireworks_models_llama-v3-70b-instruct_cloud_eu.json" {
		t.Fatalf("sanitized filename = %q", name)
	}
}

func TestWriteRecordIsByteForByteStableAcrossRepeatedWrites(t *testing.T) {
	dir := t.TempDir()
	r := sampleRecord()
	if err := aicert.WriteRecord(dir, r); err != nil {
		t.Fatalf("WriteRecord (1st): %v", err)
	}
	path := filepath.Join(dir, "summarize", "anthropic_claude-sonnet-5_cloud.json")
	first, err := os.ReadFile(path) // #nosec G304 -- t.TempDir() + a literal filename, test-only
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if err := aicert.WriteRecord(dir, r); err != nil {
		t.Fatalf("WriteRecord (2nd): %v", err)
	}
	second, err := os.ReadFile(path) // #nosec G304 -- t.TempDir() + a literal filename, test-only
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(first) != string(second) {
		t.Fatalf("re-writing an identical Record changed the file:\n--- first ---\n%s\n--- second ---\n%s", first, second)
	}
	if len(first) == 0 || first[len(first)-1] != '\n' {
		t.Fatalf("record file does not end with a trailing newline: %q", first)
	}
}

func TestLoadRecordsOnAMissingDirectoryIsEmptyNotAnError(t *testing.T) {
	got, err := aicert.LoadRecords(filepath.Join(t.TempDir(), "does-not-exist"))
	if err != nil {
		t.Fatalf("LoadRecords on a missing dir: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d records, want 0", len(got))
	}
}

func TestLoadRecordsSortsDeterministicallyAcrossTasksAndModels(t *testing.T) {
	dir := t.TempDir()
	b := sampleRecord()
	b.Task, b.Provider, b.ServedModel = "summarize", "zzz-provider", "m1"
	a := sampleRecord()
	a.Task, a.Provider, a.ServedModel = "enrich", "aaa-provider", "m1"
	if err := aicert.WriteRecord(dir, b); err != nil {
		t.Fatalf("WriteRecord: %v", err)
	}
	if err := aicert.WriteRecord(dir, a); err != nil {
		t.Fatalf("WriteRecord: %v", err)
	}
	got, err := aicert.LoadRecords(dir)
	if err != nil {
		t.Fatalf("LoadRecords: %v", err)
	}
	if len(got) != 2 || got[0].Task != "enrich" || got[1].Task != "summarize" {
		t.Fatalf("got %+v, want enrich then summarize", got)
	}
}

// A record covers a task, and a reader asks about a site. Folding the record's
// scenario rows per site is what keeps a four-site task from printing one set
// of numbers four times under four labels.
func TestRecordForSiteFoldsOnlyThatSitesScenarios(t *testing.T) {
	rec := aicert.Record{
		Task: "cold_start", Runs: 4, Passed: 3,
		Scenarios: []aicert.ScenarioRecord{
			{Scenario: "acts_01", Site: "acts", Verdict: aicert.VerdictCertified, Runs: 1, Passed: 1, ReportedAccepted: 1},
			{Scenario: "acts_02", Site: "acts", Verdict: aicert.VerdictSupportedDegraded, Runs: 1, Passed: 1, ReportedAbstained: 1},
			{Scenario: "extract_01", Site: "field_extract", Verdict: aicert.VerdictNotSupported, Runs: 2, Passed: 1, ReportedAccepted: 1, ReportedInvalid: 1},
		},
	}

	acts, ok := rec.ForSite("acts")
	if !ok {
		t.Fatal("the record ran two scenarios on acts and reports covering none")
	}
	if acts.Runs != 2 || acts.Passed != 2 || acts.ReportedAbstained != 1 {
		t.Fatalf("acts tally = %+v, want its own two runs", acts)
	}
	if acts.Verdict != aicert.VerdictSupportedDegraded {
		t.Fatalf("acts verdict = %q, want the worse of its two scenarios", acts.Verdict)
	}
	if acts.Reliability() != 1 {
		t.Fatalf("acts reliability = %v, want 1 — both of ITS runs passed, while the task's did not", acts.Reliability())
	}

	extract, ok := rec.ForSite("field_extract")
	if !ok {
		t.Fatal("the record ran a scenario on field_extract and reports covering none")
	}
	if extract.Runs != 2 || extract.Passed != 1 || extract.ReportedInvalid != 1 {
		t.Fatalf("field_extract tally = %+v, want its own two runs", extract)
	}

	// A site the record never ran is not a zeroed measurement: it is no
	// measurement, and a row built from a zero tally would claim otherwise.
	if _, ok := rec.ForSite("company_message"); ok {
		t.Fatal("a site this record never measured reports a tally")
	}
}
