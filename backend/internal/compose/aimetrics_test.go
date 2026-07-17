// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"io"
	"strings"
	"testing"
)

// TestWithAIMetricsRendersOnceAcrossSurfaces guards the Task-6 review fix:
// coldStartOptions and offerDraftOptions both call WithAIMetrics for their
// own ModelPath, and the field they set must render exactly once —
// last-wins over a single func(io.Writer), not an accumulating slice that
// duplicates the AI metric families on every scrape.
func TestWithAIMetricsRendersOnceAcrossSurfaces(t *testing.T) {
	var s Server
	calls := 0
	first := func(w io.Writer) { calls++; _, _ = io.WriteString(w, "first\n") }
	second := func(w io.Writer) { calls++; _, _ = io.WriteString(w, "second\n") }

	WithAIMetrics(first)(&s, nil)
	WithAIMetrics(second)(&s, nil)

	var b strings.Builder
	s.writeAIMetrics(&b)

	if calls != 1 {
		t.Fatalf("writeAIMetrics invoked the registered renderer %d times, want 1", calls)
	}
	out := b.String()
	if out != "second\n" {
		t.Fatalf("writeAIMetrics output = %q, want the last-registered renderer's output only", out)
	}
}

// TestWithAIMetricsUnsetWritesNothing keeps an AI-less role's /metrics
// honest: no renderer registered means no AI counter family at all.
func TestWithAIMetricsUnsetWritesNothing(t *testing.T) {
	var s Server
	var b strings.Builder
	s.writeAIMetrics(&b)
	if b.Len() != 0 {
		t.Fatalf("writeAIMetrics with no renderer wrote %q, want empty", b.String())
	}
}
