// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package aicert_test

import (
	"os"
	"path/filepath"
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
	if len(got) != 1 || got[0] != want {
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
