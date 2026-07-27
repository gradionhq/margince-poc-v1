// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package aicert_test

// The score bands' own load-time gate. A scenario whose bands are omitted or
// out of order would not measure a weaker model differently from a stronger
// one — it would certify whatever it was given — so each of these is refused
// at load rather than discovered as a suspiciously green record.

import (
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose/aicert"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
)

func TestLoadCorpusRefusesOmittedBands(t *testing.T) {
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
`)
	_, err := aicert.LoadCorpus(dir, censusFor(t, ai.TaskSummarize))
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
site: widget
source: hand_authored
sanitized_by: jane
fixture: {subject: hi}
expect:
  outcome: accepted
  answer: hi
  bands: {certified_min: 50, degraded_min: 70, floor: 40}
`)
	_, err := aicert.LoadCorpus(dir, censusFor(t, ai.TaskSummarize))
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
site: widget
source: hand_authored
sanitized_by: jane
fixture: {subject: hi}
expect:
  outcome: accepted
  answer: hi
  bands: {certified_min: 70, degraded_min: 50, floor: 60}
`)
	_, err := aicert.LoadCorpus(dir, censusFor(t, ai.TaskSummarize))
	if err == nil {
		t.Fatal("want an error for floor above degraded_min, got nil")
	}
	if !strings.Contains(err.Error(), "floor") {
		t.Fatalf("error %q does not name the offending field", err)
	}
}
