// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"io"
	"strings"
	"testing"
)

// TestWithAIMetricsRendersOnceAcrossSurfaces: when several wired surfaces
// each register an AI metrics renderer, exactly one — the last — may run
// per scrape. The registration is last-wins over a single func(io.Writer),
// never an accumulating slice: a second renderer would repeat the AI
// metric families, which a strict Prometheus scraper rejects wholesale.
func TestWithAIMetricsRendersOnceAcrossSurfaces(t *testing.T) {
	var s Server
	calls := 0
	first := func(w io.Writer) {
		calls++
		if _, err := io.WriteString(w, "first\n"); err != nil {
			t.Errorf("write first: %v", err)
		}
	}
	second := func(w io.Writer) {
		calls++
		if _, err := io.WriteString(w, "second\n"); err != nil {
			t.Errorf("write second: %v", err)
		}
	}

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
