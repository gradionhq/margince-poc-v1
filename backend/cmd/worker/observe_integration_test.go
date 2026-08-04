// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package main

// The worker's readiness probe against its REAL dependencies. It is here
// rather than in the unit lane because the probe's whole content is whether
// those dependencies answer: a stubbed pool and bus would prove the stub, and
// a readiness check that cannot fail is indistinguishable from one that always
// passes.
//
// So every check the probe declares is driven BOTH ways below — ready, and
// then broken one dependency at a time. A check nobody can break is one that
// could be deleted with every test still green, which is the same as not
// having it.

import (
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/gradionhq/margince/backend/internal/platform/overlaybudget/budgettest"
)

// startProbeListener brings the surface up on a kernel-chosen port against the
// dependencies given, and answers its base URL.
func startProbeListener(t *testing.T, pool *pgxpool.Pool, rdb *redis.Client, boot *bootGate) string {
	t.Helper()
	observe, err := startObserveListener(t.Context(),
		workerConfig{observeAddr: "127.0.0.1:0"}, pool, rdb, boot, quietLog())
	if err != nil {
		t.Fatalf("startObserveListener: %v", err)
	}
	t.Cleanup(observe.Stop)
	return "http://" + observe.Addr
}

// bootedGate is a gate already marked complete — the state a replica is in
// once run() has started its lanes and its job runner.
func bootedGate() *bootGate {
	var boot bootGate
	boot.complete()
	return &boot
}

// TestTheWorkerReadinessProbeAnswersFromItsRealDependencies — an orchestrator
// reads /readyz to decide whether this replica should be left in service, and
// the answer has to come from the database and the bus this process actually
// holds. The pool section of /metrics is asserted alongside, because a pool
// wired into one surface and not the other would mean the two disagree about
// the same process.
func TestTheWorkerReadinessProbeAnswersFromItsRealDependencies(t *testing.T) {
	base := startProbeListener(t, workerTestPool(t), budgettest.Client(t), bootedGate())

	status, body := get(t, base+"/readyz")
	if status != http.StatusOK {
		t.Fatalf("GET /readyz = %d (%s), want 200 with a booted replica, a live pool and a live bus", status, strings.TrimSpace(body))
	}
	if !strings.HasPrefix(body, "ready") {
		t.Errorf("GET /readyz body = %q, want it to start with %q", body, "ready")
	}
	// The visibility lines the shared probe renders are reached only on the
	// 200 path — a failing check returns before them — so this is the one
	// place the omission can be asserted rather than passed over. The worker
	// wires no AI lane, and an empty "ai: " reads as a state that could not be
	// DETERMINED rather than as one that does not apply.
	if strings.Contains(body, "ai:") {
		t.Errorf("the worker reports an AI line it wires nothing for: %q", body)
	}

	_, metrics := get(t, base+"/metrics")
	if !strings.Contains(metrics, "margince_pgxpool_conns") {
		t.Errorf("the worker published no pool gauges for a pool it holds; /readyz and /metrics "+
			"would then disagree about the same process\ngot:\n%s", metrics)
	}
}

// TestEachDeclaredReadinessCheckCanActuallyFail drives all three the other
// way. Each arm breaks exactly ONE dependency and asserts the 503 names that
// one: an arm that passed because a different check failed would prove nothing
// about the check it is named for.
func TestEachDeclaredReadinessCheckCanActuallyFail(t *testing.T) {
	t.Run("boot", func(t *testing.T) {
		// A gate never marked complete is the state during boot. The listener
		// comes up before the lanes and the runner on purpose, so this is the
		// window a rollout must not read as ready.
		base := startProbeListener(t, workerTestPool(t), budgettest.Client(t), &bootGate{})

		status, body := get(t, base+"/readyz")
		if status != http.StatusServiceUnavailable {
			t.Fatalf("GET /readyz = %d, want 503 while the lanes and the job runner are still starting", status)
		}
		if !strings.Contains(body, "boot") {
			t.Errorf("the 503 body does not name the boot check: %q", body)
		}
	})

	t.Run("postgres", func(t *testing.T) {
		// Closing the pool is what a database outage looks like from inside
		// this process: every acquire fails from here on. The pool is this
		// subtest's own, so nothing else is reading it.
		pool := workerTestPool(t)
		base := startProbeListener(t, pool, budgettest.Client(t), bootedGate())
		pool.Close()

		status, body := get(t, base+"/readyz")
		if status != http.StatusServiceUnavailable {
			t.Fatalf("GET /readyz = %d, want 503 with the database gone", status)
		}
		if !strings.Contains(body, "postgres") {
			t.Errorf("the 503 body does not name the failed dependency: %q", body)
		}
	})

	t.Run("draining", func(t *testing.T) {
		// The other end of the same gate. run() defers draining() AFTER
		// complete(), so LIFO puts it ahead of the job-runner stop: a replica
		// that has begun shutting down must stop being sent work while it is
		// still putting down what it holds. The listener outlives the drain —
		// it is stopped last — which is exactly why the answer has to change.
		boot := bootedGate()
		base := startProbeListener(t, workerTestPool(t), budgettest.Client(t), boot)
		if status, _ := get(t, base+"/readyz"); status != http.StatusOK {
			t.Fatalf("GET /readyz = %d before the drain, want 200", status)
		}

		boot.draining()

		status, body := get(t, base+"/readyz")
		if status != http.StatusServiceUnavailable {
			t.Fatalf("GET /readyz = %d once shutdown began, want 503 — a draining replica must leave the pool", status)
		}
		if !strings.Contains(body, "boot") {
			t.Errorf("the 503 body does not name the lifecycle check: %q", body)
		}
	})

	t.Run("redis", func(t *testing.T) {
		// Same shape for the bus, pointed at an address nothing listens on:
		// that is what a bus outage looks like from inside this process, and
		// unlike closing the shared test client it leaves no other test's
		// dependency broken. Without this arm the redis check could be deleted
		// outright and every other test here would stay green.
		unreachable := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"})
		t.Cleanup(func() {
			if err := unreachable.Close(); err != nil {
				t.Errorf("closing the unreachable bus client: %v", err)
			}
		})
		base := startProbeListener(t, workerTestPool(t), unreachable, bootedGate())

		status, body := get(t, base+"/readyz")
		if status != http.StatusServiceUnavailable {
			t.Fatalf("GET /readyz = %d, want 503 with the bus gone", status)
		}
		if !strings.Contains(body, "redis") {
			t.Errorf("the 503 body does not name the failed dependency: %q", body)
		}
	})
}
