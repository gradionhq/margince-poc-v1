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
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
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

// keyedPersonRouter mounts a stub on the person-update route, which
// replayScope probes, wired per-route so the chi RoutePattern binds.
func keyedPersonRouter(e *integration.Env, handler http.HandlerFunc) chi.Router {
	r := chi.NewRouter()
	r.With(idempotency(e.Pool)).Patch("/v1/people/{id}", handler)
	return r
}

func keyedPersonCall(ctx context.Context, r chi.Router, person ids.UUID, key string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPatch, "/v1/people/"+person.String(), strings.NewReader(`{"full_name":"Renamed"}`)).WithContext(ctx)
	req.Header.Set("Idempotency-Key", key)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

// API-CC-8: the recorded body is a retransmission, not a receipt that
// outlives the authority it was produced under. A caller who could see the
// record when they wrote it, and cannot see it when they retry, gets the
// read path's 404 — not the bytes. The same key still replays while they
// CAN see it, so the gate refuses on visibility rather than on retrying.
func TestReplayRefusesOnceTheCallerHasLostSightOfTheRecord(t *testing.T) {
	e := integration.Setup(t)
	rep1 := e.As(e.Rep1, []ids.UUID{e.Team1}, integration.RepPerms)
	person := e.SeedPerson(t, "Visible then not", &e.Rep1)

	calls := 0
	r := keyedPersonRouter(e, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if _, err := io.WriteString(w, `{"id":"`+person.String()+`","full_name":"Renamed"}`); err != nil {
			t.Errorf("writing the stub response: %v", err)
		}
	})

	const key = "replay-after-revocation"
	if first := keyedPersonCall(rep1, r, person, key); first.Code != http.StatusOK {
		t.Fatalf("first call = %d, want 200", first.Code)
	}

	// While the record is still visible the key replays, and does NOT
	// re-execute — without this the 404 below would prove nothing, since a
	// gate that broke replay outright would produce it too.
	replay := keyedPersonCall(rep1, r, person, key)
	if replay.Code != http.StatusOK {
		t.Fatalf("replay while visible = %d, want the recorded 200", replay.Code)
	}
	if !strings.Contains(replay.Body.String(), "Renamed") {
		t.Fatalf("replay while visible returned %q, want the recorded body", replay.Body.String())
	}
	if calls != 1 {
		t.Fatalf("handler ran %d times, want 1 — the replay re-executed instead of replaying", calls)
	}

	// The record moves to a team this caller is not in. Nothing about the
	// claim changed: same principal, same key, same path, same body.
	owner := integration.OwnerConn(t)
	if _, err := owner.Exec(context.Background(),
		`UPDATE person SET owner_id = $1 WHERE id = $2`, e.Rep3, person); err != nil {
		t.Fatalf("transferring the person to another team: %v", err)
	}

	after := keyedPersonCall(rep1, r, person, key)
	if after.Code != http.StatusNotFound {
		t.Fatalf("replay after losing sight = %d, want 404 — a stored response must not outlive the authority it was produced under (API-CC-8)", after.Code)
	}
	if strings.Contains(after.Body.String(), "Renamed") {
		t.Fatalf("replay after losing sight leaked the recorded body: %q", after.Body.String())
	}
	if calls != 1 {
		t.Fatalf("handler ran %d times, want 1 — the refused replay must not re-execute the mutation either", calls)
	}
}
