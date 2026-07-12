// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// The replay half of the Idempotency-Key promise, against the real claim
// table: a replay repeats the ORIGINAL response verbatim — status, body,
// AND media type (0069 response_content_type). The middleware records
// whichever 2xx the handler produced; nothing about the claim may assume
// application/json. A non-2xx outcome is deliberately never recorded
// (see idempotency.go's package comment: a failed attempt releases the
// claim), so the media-type invariant is proven on a recorded 2xx, and
// the failure path is proven as a re-execution that keeps its own
// problem+json.

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
)

// keyedQuotaRouter mounts a stub handler on an idempotency-mapped route,
// wired exactly as the generated router wires the middleware (per-route,
// so the chi RoutePattern the map is keyed by is bound).
func keyedQuotaRouter(e *integration.Env, handler http.HandlerFunc) chi.Router {
	r := chi.NewRouter()
	r.With(idempotency(e.Pool)).Post("/v1/quotas", handler)
	return r
}

func keyedQuotaCall(ctx context.Context, r chi.Router, key string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/v1/quotas", strings.NewReader(`{"target_minor":1}`)).WithContext(ctx)
	req.Header.Set("Idempotency-Key", key)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func TestIdempotencyReplayRepeatsTheRecordedContentType(t *testing.T) {
	e := integration.Setup(t)
	ctx := e.Admin() // ONE principal: the claim is scoped per (workspace, principal, key, path)

	calls := 0
	r := keyedQuotaRouter(e, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusCreated)
		if _, err := io.WriteString(w, `{"recorded":true}`); err != nil {
			t.Errorf("writing the stub response: %v", err)
		}
	})

	first := keyedQuotaCall(ctx, r, "content-type-replay")
	if first.Code != http.StatusCreated {
		t.Fatalf("first keyed call = %d, want 201", first.Code)
	}

	replay := keyedQuotaCall(ctx, r, "content-type-replay")
	if calls != 1 {
		t.Fatalf("handler ran %d times, want 1 — the second call must be a replay", calls)
	}
	if replay.Code != http.StatusCreated || replay.Body.String() != first.Body.String() {
		t.Fatalf("replay = %d %q, want the recorded 201 %q", replay.Code, replay.Body.String(), first.Body.String())
	}
	if ct := replay.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("replayed Content-Type = %q, want the recorded application/problem+json — a replay repeats the original response verbatim, media type included", ct)
	}
}

func TestIdempotencyFailedAttemptRetryIsAFreshExecution(t *testing.T) {
	e := integration.Setup(t)
	ctx := e.Admin()

	calls := 0
	r := keyedQuotaRouter(e, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		if _, err := io.WriteString(w, `{"code":"validation_error"}`); err != nil {
			t.Errorf("writing the stub response: %v", err)
		}
	})

	// A non-2xx outcome releases the claim, so the keyed retry
	// re-executes — the 422 the client sees is always the handler's own
	// problem+json, never a stored copy that could go stale or mistyped.
	first := keyedQuotaCall(ctx, r, "failure-retry")
	retry := keyedQuotaCall(ctx, r, "failure-retry")
	if calls != 2 {
		t.Fatalf("handler ran %d times, want 2 — a failed attempt must release the claim for the retry", calls)
	}
	for name, rec := range map[string]*httptest.ResponseRecorder{"first": first, "retry": retry} {
		if rec.Code != http.StatusUnprocessableEntity || rec.Header().Get("Content-Type") != "application/problem+json" {
			t.Errorf("%s = %d %q, want 422 application/problem+json", name, rec.Code, rec.Header().Get("Content-Type"))
		}
	}
}
