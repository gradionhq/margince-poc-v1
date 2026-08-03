// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package httpserver

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestBearerTokenReadsOneSchemeForEveryTransport pins the reading every
// credentialed surface in this process shares. The lowercase row is the reason
// it is shared: RFC 7235 §2.1 makes the scheme a case-insensitive token, and
// while two spellings existed the SAME passport authenticated on /v1 and 401'd
// on /mcp — which a client reads as "re-authorize", forever, against a
// credential that was valid all along.
func TestBearerTokenReadsOneSchemeForEveryTransport(t *testing.T) {
	for header, want := range map[string]string{
		"Bearer abc":  "abc",
		"bearer abc":  "abc", // RFC 7235: the scheme is case-insensitive
		"BEARER abc":  "abc",
		"Bearer  abc": "abc", // surrounding whitespace is not part of the credential
		// A prefix that is not there must not be invented: reading past a
		// scheme this is not turns another credential into a token lookup.
		"Basic dXNlcjpwYXNz": "",
		"abc":                "",
		// Present scheme, absent credential: a caller must never look up "".
		"Bearer ":  "",
		"Bearer":   "",
		"Bearer  ": "",
		"":         "",
	} {
		if got := BearerToken(header); got != want {
			t.Errorf("BearerToken(%q) = %q, want %q", header, got, want)
		}
	}
}

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

// The preference centre's capability token travels in a path segment, and
// the access log writes the path on every request. These cases pin the
// redaction from both sides: the credential never reaches the line, and
// everything an operator reads the log FOR — method, route, trailing verb,
// ordinary record ids — still does.
func TestAccessLogRedactsCapabilityPathSegments(t *testing.T) {
	const prefix = "/v1/public/preferences/"
	// Deliberately a sentence rather than a realistic token: the assertion
	// is that this segment does not reach the log line, and a fixture that
	// LOOKS like a credential is one every secret scanner has to be told
	// about forever after.
	const token = "this-stands-in-for-a-preference-capability-token"

	for _, tc := range []struct {
		name, path, want string
	}{
		{"the token segment itself", prefix + token, prefix + "[redacted]"},
		{"a trailing verb survives", prefix + token + "/unsubscribe", prefix + "[redacted]/unsubscribe"},
		{"an unrelated path is untouched", "/v1/deals/018f2a10-0000-7000-8000-000000000001", "/v1/deals/018f2a10-0000-7000-8000-000000000001"},
		{"the prefix with nothing after it has nothing to hide", prefix, prefix},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := RedactCapabilitySegment(tc.path, prefix); got != tc.want {
				t.Errorf("RedactCapabilitySegment(%q) = %q, want %q", tc.path, got, tc.want)
			}
		})
	}

	// End to end through the middleware: the emitted line carries the
	// redacted path and no trace of the token.
	var buf strings.Builder
	log := slog.New(slog.NewTextHandler(&buf, nil))
	handler := AccessLog(log, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}), prefix)
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPut, prefix+token, nil))

	line := buf.String()
	if strings.Contains(line, token) {
		t.Errorf("the access log line carries the capability token: %s", line)
	}
	if !strings.Contains(line, prefix+"[redacted]") {
		t.Errorf("the access log line lost the route it was asked for: %s", line)
	}
}

// TestChassisWrappersPreserveResponseControllerCapabilities is a fitness
// function: SSE and long tool calls need SetWriteDeadline + Flush to reach
// the real ResponseWriter. A wrapper that embeds http.ResponseWriter without
// Unwrap() silently breaks both, and the symptom is an empty response body —
// so this asserts the capability rather than the wrapper list.
func TestChassisWrappersPreserveResponseControllerCapabilities(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	wrappers := map[string]func(http.Handler) http.Handler{
		"Correlate": Correlate,
		"AccessLog": func(h http.Handler) http.Handler { return AccessLog(log, h) },
		"Correlate+AccessLog": func(h http.Handler) http.Handler {
			return Correlate(AccessLog(log, h))
		},
	}
	for name, wrap := range wrappers {
		t.Run(name, func(t *testing.T) {
			var deadlineErr, flushErr error
			// http.Get returns once the response HEADERS arrive, which the
			// first flush already produces — so the handler goroutine can still
			// be running when the assertions below read what it writes. served
			// is closed as the handler's last act, making the handoff explicit
			// rather than a race that happens to resolve.
			served := make(chan struct{})
			srv := httptest.NewServer(wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				defer close(served)
				rc := http.NewResponseController(w)
				deadlineErr = rc.SetWriteDeadline(time.Time{})
				_, _ = w.Write([]byte("x"))
				flushErr = rc.Flush()
			})))
			defer srv.Close()
			resp, err := http.Get(srv.URL)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := resp.Body.Close(); err != nil {
					t.Errorf("closing body: %v", err)
				}
			}()
			<-served
			if deadlineErr != nil {
				t.Errorf("SetWriteDeadline through %s: %v", name, deadlineErr)
			}
			if flushErr != nil {
				t.Errorf("Flush through %s: %v", name, flushErr)
			}
		})
	}
}

// TestMetricsHandsTheJobSectionItsOwnDeadlineNotTheRequests — the job read
// queries a table no index covers, so it is the one section that has to
// stay inside the handler's budget. The overlay section next door is wired
// with r.Context() and runs unbounded; a reader who pattern-matched that
// neighbour would inherit the unbounded query, so the difference is pinned
// here rather than left to a comment.
func TestMetricsHandsTheJobSectionItsOwnDeadlineNotTheRequests(t *testing.T) {
	var deadlineSet bool
	jobStats := func(ctx context.Context, _ io.Writer) error {
		_, deadlineSet = ctx.Deadline()
		return nil
	}

	rec := httptest.NewRecorder()
	// A request context with NO deadline of its own, so a deadline seen by
	// the section can only have come from the handler.
	Metrics(nil, unreadableBacklog, zeroPublished, nil, jobStats, nil)(
		rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	if !deadlineSet {
		t.Error("the job section ran without a deadline; an unbounded scan can hold the " +
			"scrape open for as long as the query takes")
	}
}

// TestMetricsStopsWritingWhenTheJobSectionRefusesAWrite — a truncated
// exposition parses as a smaller fleet rather than as a broken one, so a
// refused write ends the body instead of being rendered past.
func TestMetricsStopsWritingWhenTheJobSectionRefusesAWrite(t *testing.T) {
	// A writer that actually REFUSES, passed through the callback exactly as
	// the real section receives it. Returning a synthetic error without
	// touching w would exercise the handler's branch while proving nothing
	// about the truncated-scrape path this test is named for.
	refused := errors.New("connection reset")
	jobStats := func(_ context.Context, w io.Writer) error {
		if _, err := w.Write([]byte("margince_job_queue_depth{queue=\"q\",workspace_id=\"\"} 1\n")); err != nil {
			return err
		}
		return refused
	}
	overlayReached := false

	rec := httptest.NewRecorder()
	Metrics(nil, unreadableBacklog, zeroPublished, nil, jobStats, &OverlayMetrics{
		SourceLag: func(context.Context) (map[string]time.Duration, error) {
			overlayReached = true
			return nil, errors.New("unreached")
		},
		SyncedTotal:   func() uint64 { return 0 },
		ConflictTotal: func() uint64 { return 0 },
		DeletedTotal:  func() uint64 { return 0 },
	})(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	if overlayReached {
		t.Error("the handler kept writing after the job section reported the writer was gone")
	}
}

// TestMetricsWithNoJobSectionWiredStillServesTheRest — a process role that
// wires no job read (the same posture nil extra and nil overlay take) must
// still serve the families it does have.
func TestMetricsWithNoJobSectionWiredStillServesTheRest(t *testing.T) {
	rec := httptest.NewRecorder()
	Metrics(nil, func(context.Context) (int64, error) { return 7, nil },
		func() uint64 { return 3 }, nil, nil, nil)(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	if !strings.Contains(rec.Body.String(), "margince_outbox_unpublished 7") {
		t.Errorf("a nil job section suppressed the rest of the exposition:\n%s", rec.Body.String())
	}
}

// unreadableBacklog stands in for the outbox read on a pool that cannot be
// reached, which is the honest state of idlePool.
func unreadableBacklog(context.Context) (int64, error) {
	return 0, errors.New("the outbox is not readable in a wiring test")
}

func zeroPublished() uint64 { return 0 }

// TestMetricsOmitsThePoolGaugesWhenNoPoolIsInjected — every other section
// here is "declared or absent"; the pool gauges were the one that panicked
// instead. An unmeasured pool must be missing from the scrape, never
// reported as an idle one.
func TestMetricsOmitsThePoolGaugesWhenNoPoolIsInjected(t *testing.T) {
	rec := httptest.NewRecorder()
	Metrics(nil, unreadableBacklog, zeroPublished, nil, nil, nil)(
		rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	if strings.Contains(rec.Body.String(), "margince_pgxpool_conns") {
		t.Errorf("the pool gauges were rendered without a pool to measure:\n%s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "margince_relay_published_total") {
		t.Errorf("an absent pool suppressed the sections that did not need it:\n%s", rec.Body.String())
	}
}
