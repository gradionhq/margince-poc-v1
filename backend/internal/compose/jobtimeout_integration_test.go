// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose_test

// The declared timeout is what River actually applies. Proving this needed an
// injectable seam before jobs.Govern existed (PR #394's job-health test
// settled for asserting the constant and saying so in its own name); it now
// needs only the same wrapper production registers, over a fixture kind and a
// real River client.
//
// The fixture kind, timeout_probe, cannot live in api/jobs.yaml (Task 9's
// census would then demand a production Go type for it) and cannot register
// through addDeclaredWorker either (nothing outside the contract can reach
// that path). The escape hatch is the forbidigo exclusion for _test.go files:
// this registers directly through river.AddWorker + jobs.Govern with a
// hand-built Spec, which is legitimate because everything downstream of
// Govern — governedWorker, the Timeout() River calls, the Work() it wraps —
// is the exact path production's addDeclaredWorker also drives. Only the Spec
// is hand-built rather than read from the compiled table.

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/riverqueue/river"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	"github.com/gradionhq/margince/backend/internal/platform/jobs"
)

type timeoutProbeArgs struct{}

func (timeoutProbeArgs) Kind() string { return "timeout_probe" }

type timeoutProbeWorker struct {
	deadline chan time.Time
	// release lets a job whose context is NEVER cancelled by design (the
	// {none: true} case) return once the test has read what it needs, rather
	// than blocking forever. Left nil for the fixed-timeout case, where
	// ctx.Done() alone is the wait: a nil channel's case in a select never
	// fires, so Work there still blocks on the real cancellation and nothing
	// else -- the strongest proof available that the deadline is enforced,
	// not merely reported.
	release chan struct{}
}

// Work reports the deadline it was given (or its deliberate absence) and
// then waits to return until either the context is really cancelled or the
// test releases it. It never sleeps -- a select on ctx.Done()/release is the
// wait, so the test finishes the moment River cancels (or the test says so),
// not after a fixed delay.
func (w *timeoutProbeWorker) Work(ctx context.Context, _ *river.Job[timeoutProbeArgs]) error {
	if d, ok := ctx.Deadline(); ok {
		w.deadline <- d
	} else {
		close(w.deadline)
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-w.release:
		return nil
	}
}

// newProbeRunner boots a worker-role jobs.Runner over the shared migrated
// integration database, working only the given fixture workers on River's
// default queue. It is the same jobs.New the composition layer's own
// NewJobRunner calls underneath addDeclaredWorker -- only the Workers bundle
// differs, because a fixture kind has no declared Spec to route through that
// helper. The returned cleanup stops the runner; callers defer it.
func newProbeRunner(t *testing.T, workers *river.Workers) (*jobs.Runner, func()) {
	t.Helper()
	e := integration.Setup(t)
	integration.ApplyRiverSchema(t)

	runner, err := jobs.New(e.Pool, jobs.Config{
		Queues:  map[string]river.QueueConfig{river.QueueDefault: {MaxWorkers: 1}},
		Workers: workers,
	}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("jobs.New: %v", err)
	}
	if err := runner.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	cleanup := func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := runner.Stop(stopCtx); err != nil {
			t.Errorf("Stop: %v", err)
		}
	}
	return runner, cleanup
}

func TestTheDeclaredTimeoutIsTheDeadlineRiverApplies(t *testing.T) {
	const declared = 100 * time.Millisecond

	probe := &timeoutProbeWorker{deadline: make(chan time.Time, 1)}
	workers := river.NewWorkers()
	river.AddWorker(workers, jobs.Govern[timeoutProbeArgs](
		probe,
		jobs.Spec{Kind: "timeout_probe", Timeout: jobs.TimeoutPolicy{Fixed: declared}},
		0,
	))

	runner, cleanup := newProbeRunner(t, workers)
	defer cleanup()

	started := time.Now()
	if err := runner.Enqueue(context.Background(), timeoutProbeArgs{}, nil); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	select {
	case deadline, ok := <-probe.deadline:
		if !ok {
			t.Fatal("Work ran with NO deadline — the declared timeout did not reach River")
		}
		got := deadline.Sub(started)
		if got < declared/2 || got > declared*10 {
			t.Errorf("deadline %v is not the declared %v — Govern's value is not what River applied", got, declared)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Work never started; the probe job was not picked up")
	}
}

// TestADeclaredAbsenceLeavesTheJobWithNoDeadline is the honest second case: a
// {none: true} declaration must leave the job with NO deadline at all, not a
// long one. Two production kinds (embed_reindex_workspace,
// embed_drift_workspace) depend on exactly this to stay outside River's
// rescuer, and -1 silently coercing into "some timeout" would break that
// without any other test noticing, because every OTHER kind in the fleet
// carries a real deadline and would not catch the coercion.
func TestADeclaredAbsenceLeavesTheJobWithNoDeadline(t *testing.T) {
	// release is required here: this job's context is NEVER cancelled (that
	// is the property under test), and jobs.Runner.Stop performs River's
	// GRACEFUL stop -- it waits for in-flight work rather than cancelling
	// it. Without release, Work would still be waiting on a deadline that
	// will never arrive when cleanup calls Stop, and Stop would hang out its
	// full budget every run.
	probe := &timeoutProbeWorker{deadline: make(chan time.Time, 1), release: make(chan struct{})}
	workers := river.NewWorkers()
	river.AddWorker(workers, jobs.Govern[timeoutProbeArgs](
		probe,
		jobs.Spec{Kind: "timeout_probe", Timeout: jobs.TimeoutPolicy{None: true}},
		0,
	))

	runner, cleanup := newProbeRunner(t, workers)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := runner.Enqueue(ctx, timeoutProbeArgs{}, nil); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	select {
	case _, ok := <-probe.deadline:
		if ok {
			t.Error("a {none: true} declaration must leave the job with NO deadline — that is what takes it out of River's rescuer")
		}
	case <-ctx.Done():
		t.Fatal("Work never started; the probe job was not picked up")
	}
	// The absence is proved; let Work return so cleanup's graceful Stop does
	// not wait out a context that, correctly, never cancels itself.
	close(probe.release)
}
