// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

import (
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose/aicert"
)

func TestRenderMatrixEmptyRecordsPrintsAHint(t *testing.T) {
	out := renderMatrix(nil)
	if !strings.Contains(out, "make e2e-ai") {
		t.Fatalf("renderMatrix(nil) = %q, want a hint pointing at `make e2e-ai`", out)
	}
	if strings.Contains(out, "SCORE_P50") {
		t.Fatalf("renderMatrix(nil) = %q, want no table header when there is nothing to show", out)
	}
}

func TestRenderMatrixFormatsEachRecordAsARow(t *testing.T) {
	records := []aicert.Record{
		{
			Task:        "extract_lead",
			Provider:    "anthropic",
			ServedModel: "claude-sonnet-4-6",
			EnvClass:    "byok",
			Verdict:     aicert.VerdictCertified,
			Runs:        3,
			Reliability: 1.0,
			ScoreP50:    92,
			LatencyP50:  1450,
		},
		{
			Task:        "extract_lead",
			Provider:    "ollama",
			ServedModel: "llama3.1:8b",
			EnvClass:    "local",
			Verdict:     aicert.VerdictNotSupported,
			Runs:        3,
			Reliability: 0.33,
			ScoreP50:    40,
			LatencyP50:  2100,
		},
	}

	out := renderMatrix(records)

	for _, want := range []string{
		"TASK", "PROVIDER", "MODEL", "VERDICT", "RELIABILITY", "SCORE_P50", "LATENCY_P50_MS",
		"extract_lead", "anthropic", "claude-sonnet-4-6", aicert.VerdictCertified, "92", "1450",
		"ollama", "llama3.1:8b", aicert.VerdictNotSupported, "40", "2100",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("renderMatrix output missing %q; got:\n%s", want, out)
		}
	}

	// Two data rows plus a header: the row order LoadRecords already
	// guarantees (Task/Provider/ServedModel/EnvClass) must survive
	// straight through to the rendered table.
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("renderMatrix produced %d lines, want 3 (header + 2 records):\n%s", len(lines), out)
	}
	if !strings.Contains(lines[1], "anthropic") || !strings.Contains(lines[2], "ollama") {
		t.Fatalf("renderMatrix rows out of order, want anthropic before ollama:\n%s", out)
	}
}
