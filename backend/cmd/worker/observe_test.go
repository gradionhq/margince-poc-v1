// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

import (
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// quietLog keeps a listener's own log lines out of the test output; what is
// under test is what it SERVES, not what it says about itself.
func quietLog() *slog.Logger { return slog.New(slog.DiscardHandler) }

// startForTest brings the listener up on a kernel-chosen port and answers its
// base URL. A fixed port would make two packages' tests collide on a busy
// machine, which reads as a flake in whichever ran second.
func startForTest(t *testing.T) string {
	t.Helper()

	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserving a port: %v", err)
	}
	addr := probe.Addr().String()
	if err := probe.Close(); err != nil {
		t.Fatalf("releasing the reserved port: %v", err)
	}

	// A nil pool and bus are enough for the two endpoints asserted below:
	// /healthz reads nothing, and the metrics handler omits the pool section
	// when it was handed none. /readyz is the one endpoint that would call
	// into them, and it is the integration lane's to prove against real
	// dependencies rather than against a stub that would only restate itself.
	stop, err := startObserveListener(t.Context(), workerConfig{observeAddr: addr}, nil, nil, quietLog())
	if err != nil {
		t.Fatalf("startObserveListener: %v", err)
	}
	t.Cleanup(stop)
	return "http://" + addr
}

// get fetches one path and answers its status and body.
func get(t *testing.T, url string) (int, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("building a request for %s: %v", url, err)
	}
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			t.Errorf("closing the response body: %v", err)
		}
	}()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading the response to %s: %v", url, err)
	}
	return resp.StatusCode, string(body)
}

// TestAnUnconfiguredObserveAddressServesNothing — off is the default, and off
// has to mean no listener rather than one bound somewhere the operator did not
// choose. The stop function must still be callable, because the caller defers
// it unconditionally and a nil there would panic every worker that never
// enabled the surface.
func TestAnUnconfiguredObserveAddressServesNothing(t *testing.T) {
	stop, err := startObserveListener(t.Context(), workerConfig{}, nil, nil, quietLog())
	if err != nil {
		t.Fatalf("an empty --observe-addr must be a legitimate configuration, got: %v", err)
	}
	if stop == nil {
		t.Fatal("no stop function returned; run() defers it unconditionally and would panic")
	}
	stop()
}

// TestAnUnusableObserveAddressFailsTheBoot — the listen happens during boot
// rather than inside the serving goroutine, so a busy port or a malformed
// address stops the process with a message naming it. Logged instead, the
// worker would carry on looking healthy while publishing nothing.
func TestAnUnusableObserveAddressFailsTheBoot(t *testing.T) {
	held, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("holding a port: %v", err)
	}
	t.Cleanup(func() {
		if err := held.Close(); err != nil {
			t.Errorf("releasing the held port: %v", err)
		}
	})

	stop, err := startObserveListener(t.Context(),
		workerConfig{observeAddr: held.Addr().String()}, nil, nil, quietLog())
	if err == nil {
		stop()
		t.Fatal("binding a port already in use succeeded; a worker that could not serve its probes must fail its boot")
	}
	if !strings.Contains(err.Error(), held.Addr().String()) {
		t.Errorf("the error does not name the address that failed: %v", err)
	}
}

// TestTheWorkerServesItsLivenessProbe — the point of the listener: an
// orchestrator can ask this process whether it is alive at all.
func TestTheWorkerServesItsLivenessProbe(t *testing.T) {
	status, body := get(t, startForTest(t)+"/healthz")
	if status != http.StatusOK {
		t.Errorf("GET /healthz = %d, want 200", status)
	}
	if body != "ok" {
		t.Errorf("GET /healthz body = %q, want %q", body, "ok")
	}
}

// TestTheWorkerMetricsAreProcessLocalAndReServeNoFleetGauge is the boundary
// the whole listener rests on. What this role adds is per-REPLICA visibility:
// the fleet-wide readings stay a single copy on the api, because two roles
// answering one number is a worse operator surface than one gap. A section
// added here that reads a shared table would silently recreate that.
func TestTheWorkerMetricsAreProcessLocalAndReServeNoFleetGauge(t *testing.T) {
	status, body := get(t, startForTest(t)+"/metrics")
	if status != http.StatusOK {
		t.Fatalf("GET /metrics = %d, want 200", status)
	}

	for _, family := range []string{
		"margince_process_goroutines",
		"margince_process_heap_bytes",
		"margince_process_heap_sys_bytes",
		"margince_process_gc_cycles_total",
		"margince_relay_published_total",
	} {
		if !strings.Contains(body, "# TYPE "+family+" ") {
			t.Errorf("the worker publishes no %s; it is process-local and served nowhere else\ngot:\n%s", family, body)
		}
	}

	// Each of these is a projection of a table every role shares, so the api's
	// copy is the fleet's one reading of it.
	for _, family := range []string{
		"margince_job_queue_depth",
		"margince_job_declared_info",
		"margince_sweep_workspaces_total",
		"margince_sweep_units_total",
		"margince_outbox_unpublished",
	} {
		if strings.Contains(body, family) {
			t.Errorf("the worker re-serves %s, which is a fleet-wide reading the api already answers; "+
				"two sources for one number is worse than one\ngot:\n%s", family, body)
		}
	}
}

// TestTheWorkerMetricsOmitThePoolSectionRatherThanZeroingIt — a role wired
// without a pool must publish no pool gauges at all. A zero-valued section
// reads exactly like an idle pool, which is the reading an operator would act
// on; the same "declared or absent" posture every other section takes.
func TestTheWorkerMetricsOmitThePoolSectionRatherThanZeroingIt(t *testing.T) {
	_, body := get(t, startForTest(t)+"/metrics")
	if strings.Contains(body, "margince_pgxpool_conns") {
		t.Errorf("a nil pool produced pool gauges, which read as an idle pool rather than as no pool\ngot:\n%s", body)
	}
}
