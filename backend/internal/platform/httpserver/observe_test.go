// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package httpserver

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
)

// Readyz reports the AI runtime's binding posture on the 200 body but
// never lets it gate readiness: an AI-unconfigured deployment is still a
// ready deployment (ai-operational-spec §2), so "ai: unconfigured" must
// ride the success body with no other dependency check present.
func TestReadyzReportsAIStateOnSuccessNeverAsAGate(t *testing.T) {
	for _, state := range []string{"configured", "unconfigured", "fake"} {
		t.Run(state, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/readyz", nil)
			rec := httptest.NewRecorder()
			Readyz(state)(rec, req)

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

	Readyz("configured", failing)(rec, req)

	if rec.Code != 503 {
		t.Fatalf("want 503 on a failed dependency check, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "postgres") {
		t.Fatalf("body %q does not name the unready dependency", rec.Body.String())
	}
}
