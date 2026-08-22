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
// The fixture kind, timeout_probe, cannot live in api/jobs.yaml: adding it
// there (say, `timeout_probe: {go_type: TimeoutProbeArgs}`) would fail to
// COMPILE with `undefined: TimeoutProbeArgs` — declaredJobArgs
// (internal/compose/jobkinds_gen.go) is a closed union naming every
// declared kind's Go type by name, generated from the contract, and no
// production type answers for this fixture. It also cannot register
// through addDeclaredWorker (nothing outside the contract can reach that
// path). The escape hatch is the forbidigo exclusion for _test.go files:
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

// deadlineReadMargin is the ONLY slack TestTheDeclaredTimeoutIsTheDeadlineRiverApplies
// grants, and it is absolute rather than a fraction of the declared value:
// a multiplicative window (e.g. declared/2..declared*10) pins the order of
// magnitude, not the value, and would admit a deadline that drifted from
// what was declared by a fixed offset or a small scaling error just as
// happily as it admits the right one.
//
// It covers the gap between River computing the deadline as Work is invoked
// and Work's first statement reading ctx.Deadline() -- a few goroutine
// scheduling slots, microseconds in practice. It deliberately does NOT cover
// pickup: the measurement starts INSIDE Work rather than at Enqueue, so
// LISTEN/NOTIFY dispatch, the insert round trip and a busy runner are outside
// the window entirely instead of being budgeted for.
//
// That distinction is why this constant is 25ms rather than the 200ms it
// replaces. Measuring from enqueue made the assertion "the declared timeout is
// right AND the runner picked the job up quickly", and the second half is a
// property of the machine, not of the code under test: a loaded runner put the
// total at 311ms, 329ms and 333ms against a 300ms ceiling on three separate
// occasions, reddening main once and two innocent pull requests. A wall-clock
// ceiling over pickup is a bet that CI never has a bad minute, and widening it
// only moves the bet.
const deadlineReadMargin = 25 * time.Millisecond

type timeoutProbeArgs struct{}

func (timeoutProbeArgs) Kind() string { return "timeout_probe" }

// deadlineRead is what the probe saw and when it looked. The pair is the
// whole point: `deadline` alone can only be compared against a clock reading
// taken outside Work, which folds pickup latency into the comparison.
type deadlineRead struct {
	deadline time.Time
	readAt   time.Time
}

type timeoutProbeWorker struct {
	deadline chan deadlineRead
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
		// Read the clock HERE, beside the deadline it is compared against.
		//
		// This is a real clock rather than an injected one, and it cannot be
		// otherwise: River builds the worker deadline with context.WithTimeout
		// against its own clock, jobs.Config exposes no seam onto it, and a
		// deadline is a time.Time -- comparing it to anything requires reading
		// a clock somewhere. What the pairing buys is the SIZE of the window
		// that reading is exposed to. Measured from enqueue it spanned a
		// LISTEN/NOTIFY round trip and a queue, which is why 200ms was not
		// enough three times; measured from here it spans the goroutine
		// scheduling slots between River computing the deadline and this
		// statement running. For that to exceed deadlineReadMargin the runtime
		// would have to stall this goroutine for 25ms between two adjacent
		// statements, at which point the machine has a problem the test is
		// right to report.
		w.deadline <- deadlineRead{deadline: d, readAt: time.Now()}
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

	probe := &timeoutProbeWorker{deadline: make(chan deadlineRead, 1)}
	workers := river.NewWorkers()
	river.AddWorker(workers, jobs.Govern[timeoutProbeArgs](
		probe,
		jobs.Spec{Kind: "timeout_probe", Timeout: jobs.TimeoutPolicy{Fixed: declared}},
		0,
	))

	runner, cleanup := newProbeRunner(t, workers)
	defer cleanup()

	if err := runner.Enqueue(context.Background(), timeoutProbeArgs{}, nil); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	select {
	case read, ok := <-probe.deadline:
		if !ok {
			t.Fatal("Work ran with NO deadline — the declared timeout did not reach River")
		}
		// From the moment Work looked, never from the moment the job was
		// enqueued: how long River took to pick the job up says nothing about
		// whether the declared timeout is the one it applied.
		got := read.deadline.Sub(read.readAt)
		off := got - declared
		if off < 0 {
			off = -off
		}
		if off > deadlineReadMargin {
			t.Errorf("deadline sits %v after Work began, want %v ± %v (declared %v) — Govern's value is not what River applied",
				got, declared, deadlineReadMargin, declared)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Work never started; the probe job was not picked up")
	}
}

// TestADeclaredAbsenceLeavesTheJobWithNoDeadline is the honest second case: a
// {none: true} declaration must leave the job with NO deadline at all, not a
// long one. Two production kinds (embed_reindex, embed_drift_workspace)
// depend on exactly this to stay outside River's
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
	probe := &timeoutProbeWorker{deadline: make(chan deadlineRead, 1), release: make(chan struct{})}
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
