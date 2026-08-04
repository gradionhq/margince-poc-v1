// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package main

// The worker's readiness probe against its REAL dependencies. It is here
// rather than in the unit lane because the probe's whole content is whether
// those dependencies answer: a stubbed pool and bus would prove the stub, and
// a readiness check that cannot fail is indistinguishable from one that always
// passes.

import (
	"net"
	"net/http"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/platform/overlaybudget/budgettest"
)

// TestTheWorkerReadinessProbeAnswersFromItsRealDependencies — an orchestrator
// reads /readyz to decide whether this replica should be left in service, and
// the answer has to come from the database and the bus this process actually
// holds. Both are named in the 200 body's checks by passing, and the pool
// section of /metrics is asserted alongside because a pool wired here and not
// there would mean the two surfaces disagree about the same process.
func TestTheWorkerReadinessProbeAnswersFromItsRealDependencies(t *testing.T) {
	pool := workerTestPool(t)
	rdb := budgettest.Client(t)

	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserving a port: %v", err)
	}
	addr := probe.Addr().String()
	if err := probe.Close(); err != nil {
		t.Fatalf("releasing the reserved port: %v", err)
	}

	stop, err := startObserveListener(t.Context(), workerConfig{observeAddr: addr}, pool, rdb, quietLog())
	if err != nil {
		t.Fatalf("startObserveListener: %v", err)
	}
	t.Cleanup(stop)

	status, body := get(t, "http://"+addr+"/readyz")
	if status != http.StatusOK {
		t.Fatalf("GET /readyz = %d (%s), want 200 with a live pool and bus", status, strings.TrimSpace(body))
	}
	if !strings.HasPrefix(body, "ready") {
		t.Errorf("GET /readyz body = %q, want it to start with %q", body, "ready")
	}

	_, metrics := get(t, "http://"+addr+"/metrics")
	if !strings.Contains(metrics, "margince_pgxpool_conns") {
		t.Errorf("the worker published no pool gauges for a pool it holds; /readyz and /metrics "+
			"would then disagree about the same process\ngot:\n%s", metrics)
	}
}

// TestAnUnreachableDependencyMakesTheWorkerUnready is the other half, and the
// one that makes the probe worth serving: a closed pool must produce a 503
// naming the dependency, not a 200. A check that only ever passes tells an
// orchestrator nothing it did not already assume.
func TestAnUnreachableDependencyMakesTheWorkerUnready(t *testing.T) {
	pool := workerTestPool(t)
	rdb := budgettest.Client(t)

	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserving a port: %v", err)
	}
	addr := probe.Addr().String()
	if err := probe.Close(); err != nil {
		t.Fatalf("releasing the reserved port: %v", err)
	}

	stop, err := startObserveListener(t.Context(), workerConfig{observeAddr: addr}, pool, rdb, quietLog())
	if err != nil {
		t.Fatalf("startObserveListener: %v", err)
	}
	t.Cleanup(stop)

	// Closing the pool is what a database outage looks like from inside this
	// process: every acquire fails from here on. The pool is this test's own —
	// workerTestPool opens one per test — so nothing else is reading it.
	pool.Close()

	status, body := get(t, "http://"+addr+"/readyz")
	if status != http.StatusServiceUnavailable {
		t.Fatalf("GET /readyz = %d, want 503 with the database gone", status)
	}
	if !strings.Contains(body, "postgres") {
		t.Errorf("the 503 body does not name the failed dependency: %q", body)
	}
}
