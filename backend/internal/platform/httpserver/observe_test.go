// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package httpserver

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestWriteOverlayMetricsRendersEveryCounter pins the overlay sync-health
// section /metrics emits: the per-object-class source lag gauge and all
// three mirror counters (synced, conflict, deleted). A counter that is
// wired into OverlayMetrics but not rendered here would be invisible to
// operators, so each family's line is asserted explicitly.
func TestWriteOverlayMetricsRendersEveryCounter(t *testing.T) {
	rec := httptest.NewRecorder()
	writeOverlayMetrics(context.Background(), rec, &OverlayMetrics{
		SourceLag: func(context.Context) (map[string]time.Duration, error) {
			return map[string]time.Duration{"person": 90 * time.Second}, nil
		},
		SyncedTotal:   func() uint64 { return 7 },
		ConflictTotal: func() uint64 { return 3 },
		DeletedTotal:  func() uint64 { return 5 },
	})
	body := rec.Body.String()
	for _, want := range []string{
		`margince_overlay_source_lag_seconds{object_class="person"} 90`,
		"margince_overlay_mirror_synced_total 7",
		"margince_overlay_mirror_conflict_total 3",
		"margince_overlay_mirror_deleted_total 5",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("overlay metrics body missing %q\n---\n%s", want, body)
		}
	}
}

// Readyz reports the AI runtime's binding posture on the 200 body but
// never lets it gate readiness: an AI-unconfigured deployment is still a
// ready deployment (ai-operational-spec §2), so "ai: unconfigured" must
// ride the success body with no other dependency check present.
func TestReadyzReportsAIStateOnSuccessNeverAsAGate(t *testing.T) {
	for _, state := range []string{"configured", "unconfigured", "fake"} {
		t.Run(state, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/readyz", nil)
			rec := httptest.NewRecorder()
			Readyz(state, nil)(rec, req)

			if rec.Code != 200 {
				t.Fatalf("AI state %q must never turn /readyz unready, got status %d", state, rec.Code)
			}
			body := rec.Body.String()
			if !strings.Contains(body, "ai: "+state) {
				t.Fatalf("body %q does not report ai: %s", body, state)
			}
		})
	}
}

// A failing dependency check still wins over AI state: readiness is
// about the checks, and the AI line is informational only.
func TestReadyzDependencyFailureStillReturns503RegardlessOfAIState(t *testing.T) {
	req := httptest.NewRequest("GET", "/readyz", nil)
	rec := httptest.NewRecorder()
	failing := ReadyCheck{Name: "postgres", Check: func(context.Context) error { return errors.New("down") }}

	Readyz("configured", nil, failing)(rec, req)

	if rec.Code != 503 {
		t.Fatalf("want 503 on a failed dependency check, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "postgres") {
		t.Fatalf("body %q does not name the unready dependency", rec.Body.String())
	}
}

// Readyz reports the embed store's binding posture on the 200 body the
// same way it reports AI state (Task 17): a visibility line, never a
// gate. A nil embedState (a role that never wires an embed lane) and an
// embedState that has already turned its own marker-read failure into
// "unknown" both render "embed: unknown" — Readyz never inspects why,
// it only ever renders what the seam hands back.
func TestReadyzReportsEmbedStateOnSuccessNeverAsAGate(t *testing.T) {
	for _, tc := range []struct {
		name string
		fn   func(context.Context) string
		want string
	}{
		{name: "active", fn: func(context.Context) string { return "active" }, want: "active"},
		{name: "needs_reindex", fn: func(context.Context) string { return "needs_reindex" }, want: "needs_reindex"},
		{name: "reembedding", fn: func(context.Context) string { return "reembedding" }, want: "reembedding"},
		{name: "marker read error derives unknown", fn: func(context.Context) string { return "unknown" }, want: "unknown"},
		{name: "nil embedState defaults to unknown", fn: nil, want: "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/readyz", nil)
			rec := httptest.NewRecorder()
			Readyz("configured", tc.fn)(rec, req)

			if rec.Code != 200 {
				t.Fatalf("embed state must never turn /readyz unready, got status %d", rec.Code)
			}
			body := rec.Body.String()
			if !strings.Contains(body, "embed: "+tc.want) {
				t.Fatalf("body %q does not report embed: %s", body, tc.want)
			}
		})
	}
}

// A failing dependency check still wins over embed state too: the same
// invariant TestReadyzDependencyFailureStillReturns503RegardlessOfAIState
// pins for the AI line applies to the embed line — it never turns a
// failed dependency check into a 200.
func TestReadyzDependencyFailureStillReturns503RegardlessOfEmbedState(t *testing.T) {
	req := httptest.NewRequest("GET", "/readyz", nil)
	rec := httptest.NewRecorder()
	failing := ReadyCheck{Name: "postgres", Check: func(context.Context) error { return errors.New("down") }}

	Readyz("configured", func(context.Context) string { return "active" }, failing)(rec, req)

	if rec.Code != 503 {
		t.Fatalf("want 503 on a failed dependency check regardless of embed state, got %d", rec.Code)
	}
}
