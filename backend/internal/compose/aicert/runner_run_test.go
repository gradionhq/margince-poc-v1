// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package aicert_test

// Black-box tests over the public Run() entry point: a real corpus on
// disk, a real routing file, records written to and read back from
// disk. certifyTask's own (white-box, same-package) tests already pin
// the scored verdict math against a scripted fake; these tests instead
// pin Run's OWN plumbing — corpus loading, task filtering, repeats
// validation, and record I/O — against the offline fake provider's
// UNSCRIPTED fallback, which is itself fully deterministic (a stable
// hash of the request payload, always prefixed "fake-completion:"), so
// a `contains: fake-completion` structural check is a reliable,
// script-free HardPass signal. The judge side of that same unscripted
// fallback is never valid JSON, so every judge score here lands at 0
// (the "parsed twice, still failed, score 0" path) and every verdict is
// not_supported — Run() has no seam to script the judge, unlike
// certifyTask's own tests, so this is the honest ceiling of a pure
// black-box run.

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/compose/aicert"
)

const fakeRoutingYAML = `
profile: eu_hosted
tiers:
  local_small: {provider: fake}
  cheap_cloud: {provider: fake}
  premium: {provider: fake}
embeddings: {provider: fake}
`

func writeRoutingFile(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "routing.yaml")
	if err := os.WriteFile(path, []byte(fakeRoutingYAML), 0o600); err != nil {
		t.Fatalf("writing routing file: %v", err)
	}
	return path
}

// scenarioYAML builds one minimal, always-"basic"-named scenario for
// task — every call site in this file names a different task, never a
// different scenario name.
func scenarioYAML(task string) string {
	return `
name: basic
task: ` + task + `
source: hand_authored
sanitized_by: tester
input: Describe the widget.
expect:
  structural:
    - kind: contains
      arg: fake-completion
  rubric: Score higher for a longer, on-topic answer.
  bands:
    certified_min: 70
    degraded_min: 50
    floor: 40
`
}

func quietTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestRunWritesOneRecordPerTaskAndItLoadsBackIdentically(t *testing.T) {
	dir := t.TempDir()
	corpusDir := filepath.Join(dir, "corpus")
	recordDir := filepath.Join(dir, "records")
	writeCorpusFile(t, corpusDir, "summarize/basic_01.yaml", scenarioYAML("summarize"))
	routingPath := writeRoutingFile(t, dir)

	records, err := aicert.Run(context.Background(), aicert.RunnerConfig{
		RoutingPath: routingPath,
		CorpusDir:   corpusDir,
		RecordDir:   recordDir,
		Repeats:     3,
	}, quietTestLogger())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("got %d records, want 1", len(records))
	}
	rec := records[0]
	if rec.Task != "summarize" || rec.Provider != "fake" || rec.ServedModel != "fake" {
		t.Fatalf("record identity wrong: %+v", rec)
	}
	if rec.Runs != 3 || rec.Reliability != 1 {
		t.Fatalf("every run's output contains the required substring — want runs=3 reliability=1, got %+v", rec)
	}
	if rec.PromptVersion == "" || rec.CorpusVersion == "" {
		t.Fatalf("prompt/corpus version must be stamped, got %+v", rec)
	}
	if _, err := time.Parse(time.RFC3339, rec.RanAt); err != nil {
		t.Fatalf("ran_at %q is not RFC3339: %v", rec.RanAt, err)
	}

	loaded, err := aicert.LoadRecords(recordDir)
	if err != nil {
		t.Fatalf("LoadRecords: %v", err)
	}
	if len(loaded) != 1 || loaded[0] != rec {
		t.Fatalf("LoadRecords round-trip mismatch: wrote %+v, loaded %+v", rec, loaded)
	}
}

func TestRunTaskFilterRestrictsCertificationToOneTask(t *testing.T) {
	dir := t.TempDir()
	corpusDir := filepath.Join(dir, "corpus")
	writeCorpusFile(t, corpusDir, "summarize/basic_01.yaml", scenarioYAML("summarize"))
	writeCorpusFile(t, corpusDir, "cold_start/basic_01.yaml", scenarioYAML("cold_start"))
	routingPath := writeRoutingFile(t, dir)

	records, err := aicert.Run(context.Background(), aicert.RunnerConfig{
		RoutingPath: routingPath,
		CorpusDir:   corpusDir,
		RecordDir:   filepath.Join(dir, "records"),
		TaskFilter:  "cold_start",
		Repeats:     1,
	}, quietTestLogger())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(records) != 1 || records[0].Task != "cold_start" {
		t.Fatalf("TaskFilter must restrict to exactly cold_start, got %+v", records)
	}
}

func TestRunUnknownTaskFilterFailsLoudly(t *testing.T) {
	dir := t.TempDir()
	corpusDir := filepath.Join(dir, "corpus")
	writeCorpusFile(t, corpusDir, "summarize/basic_01.yaml", scenarioYAML("summarize"))
	routingPath := writeRoutingFile(t, dir)

	_, err := aicert.Run(context.Background(), aicert.RunnerConfig{
		RoutingPath: routingPath,
		CorpusDir:   corpusDir,
		RecordDir:   filepath.Join(dir, "records"),
		TaskFilter:  "offer_draft",
		Repeats:     1,
	}, quietTestLogger())
	if err == nil || !strings.Contains(err.Error(), "offer_draft") {
		t.Fatalf("want an error naming the unmatched task filter, got %v", err)
	}
}

func TestRunRejectsAnEvenRepeatsBeforeTouchingAnything(t *testing.T) {
	dir := t.TempDir()
	corpusDir := filepath.Join(dir, "corpus")
	writeCorpusFile(t, corpusDir, "summarize/basic_01.yaml", scenarioYAML("summarize"))
	routingPath := writeRoutingFile(t, dir)

	_, err := aicert.Run(context.Background(), aicert.RunnerConfig{
		RoutingPath: routingPath,
		CorpusDir:   corpusDir,
		RecordDir:   filepath.Join(dir, "records"),
		Repeats:     4,
	}, quietTestLogger())
	if err == nil || !strings.Contains(err.Error(), "odd") {
		t.Fatalf("want an odd-repeats complaint, got %v", err)
	}
}

// TestRunWritesTaskARecordAndSurfacesTaskBsWriteErrorInTheSameCall proves
// the "one task fails, its sibling still gets recorded, in the same
// Run() call" property that TestRunMalformedOverrideJoinsAnErrorPerTaskAndAbortsNone
// cannot: a malformed override fails every task identically, so it can
// never show one task succeeding alongside another failing in one Run.
// Here both tasks certify cleanly, but a plain FILE pre-created at the
// exact path WriteRecord needs as a directory for "summarize" makes its
// own os.MkdirAll fail — for that task only. "cold_start" sorts first
// (sortedTasks is alphabetical) and its own records/cold_start directory
// is untouched, so this proves both halves of the contract in one call:
// cold_start's record is written, and summarize's write error is heard
// (errors.Join), not swallowed.
func TestRunWritesTaskARecordAndSurfacesTaskBsWriteErrorInTheSameCall(t *testing.T) {
	dir := t.TempDir()
	corpusDir := filepath.Join(dir, "corpus")
	recordDir := filepath.Join(dir, "records")
	writeCorpusFile(t, corpusDir, "cold_start/basic_01.yaml", scenarioYAML("cold_start"))
	writeCorpusFile(t, corpusDir, "summarize/basic_01.yaml", scenarioYAML("summarize"))
	routingPath := writeRoutingFile(t, dir)

	if err := os.MkdirAll(recordDir, 0o750); err != nil {
		t.Fatalf("pre-creating the records dir: %v", err)
	}
	// WriteRecord's own recordPath for "summarize" is
	// records/summarize/<file>.json; MkdirAll needs records/summarize to
	// be a directory (or absent) — a plain file occupying that exact
	// path makes MkdirAll fail for summarize only.
	if err := os.WriteFile(filepath.Join(recordDir, "summarize"), []byte("occupied"), 0o600); err != nil {
		t.Fatalf("pre-creating the blocking file: %v", err)
	}

	records, err := aicert.Run(context.Background(), aicert.RunnerConfig{
		RoutingPath: routingPath,
		CorpusDir:   corpusDir,
		RecordDir:   recordDir,
		Repeats:     1,
	}, quietTestLogger())

	if len(records) != 1 || records[0].Task != "cold_start" {
		t.Fatalf("want exactly cold_start's record written despite summarize's write failure, got %+v", records)
	}
	if err == nil || !strings.Contains(err.Error(), "summarize") {
		t.Fatalf("want an error naming summarize's write failure, got %v", err)
	}
}

// TestRunMalformedOverrideJoinsAnErrorPerTaskAndAbortsNone proves the
// "heard, never swallowed" contract on the error path every task
// actually reaches: a malformed MODEL= override fails identically for
// every task in the corpus (each task's own certifyTask call refuses
// it independently), and Run reports every one of them — via
// errors.Join, not just the first — rather than stopping at the first
// failure.
func TestRunMalformedOverrideJoinsAnErrorPerTaskAndAbortsNone(t *testing.T) {
	dir := t.TempDir()
	corpusDir := filepath.Join(dir, "corpus")
	writeCorpusFile(t, corpusDir, "summarize/basic_01.yaml", scenarioYAML("summarize"))
	writeCorpusFile(t, corpusDir, "cold_start/basic_01.yaml", scenarioYAML("cold_start"))
	routingPath := writeRoutingFile(t, dir)

	records, err := aicert.Run(context.Background(), aicert.RunnerConfig{
		RoutingPath: routingPath,
		CorpusDir:   corpusDir,
		RecordDir:   filepath.Join(dir, "records"),
		Override:    "not-a-valid-override",
		Repeats:     1,
	}, quietTestLogger())
	if len(records) != 0 {
		t.Fatalf("a malformed override must certify nothing, got %+v", records)
	}
	if err == nil {
		t.Fatal("want a non-nil error")
	}
	for _, task := range []string{"summarize", "cold_start"} {
		if !strings.Contains(err.Error(), task) {
			t.Errorf("joined error must name task %s, got %v", task, err)
		}
	}
}
