// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package jobs

import (
	"context"
	"testing"
	"time"

	"github.com/riverqueue/river"
)

type fixtureArgs struct{}

func (fixtureArgs) Kind() string { return "govern_fixture" }

// stubbornWorker declares its own Timeout. Embedding alone would let this
// shadow the declared value; the adapter is what makes it unreachable.
type stubbornWorker struct {
	river.WorkerDefaults[fixtureArgs]
}

func (stubbornWorker) Work(context.Context, *river.Job[fixtureArgs]) error { return nil }
func (stubbornWorker) Timeout(*river.Job[fixtureArgs]) time.Duration       { return 99 * time.Hour }

func TestGovernSuppliesTheDeclaredTimeoutOverTheWorkersOwn(t *testing.T) {
	spec := Spec{Kind: "govern_fixture", Timeout: TimeoutPolicy{Fixed: 10 * time.Minute}}
	governed := Govern[fixtureArgs](stubbornWorker{}, spec, 0)

	if got := governed.Timeout(nil); got != 10*time.Minute {
		t.Errorf("Timeout() = %v, want the declared 10m — the worker's own 99h must be unreachable", got)
	}
}

func TestGovernYieldsNegativeForADeliberateAbsence(t *testing.T) {
	spec := Spec{Kind: "govern_fixture", Timeout: TimeoutPolicy{None: true}}
	governed := Govern[fixtureArgs](stubbornWorker{}, spec, 0)

	if got := governed.Timeout(nil); got != -1 {
		t.Errorf("Timeout() = %v, want -1 — a declared absence takes the job out of River's rescuer", got)
	}
}

func TestGovernUsesTheSuppliedValueForAnOperatorPolicy(t *testing.T) {
	spec := Spec{Kind: "govern_fixture", Timeout: TimeoutPolicy{OperatorField: "DeepReadCaps"}}
	governed := Govern[fixtureArgs](stubbornWorker{}, spec, 12*time.Minute)

	if got := governed.Timeout(nil); got != 12*time.Minute {
		t.Errorf("Timeout() = %v, want the 12m supplied at registration", got)
	}
}

func TestGovernedWorkerStillSatisfiesRiversInterface(t *testing.T) {
	// Registration is the only proof that matters: river.AddWorkerSafely is
	// what the runner reaches, and it is River — not this package's signature
	// — that decides whether a wrapped worker is a worker at all.
	if err := river.AddWorkerSafely[fixtureArgs](river.NewWorkers(), Govern[fixtureArgs](stubbornWorker{}, Spec{}, 0)); err != nil {
		t.Fatalf("River refused the governed worker: %v", err)
	}
}

func TestGovernForwardsWork(t *testing.T) {
	called := false
	governed := Govern[fixtureArgs](workFunc[fixtureArgs](func(context.Context, *river.Job[fixtureArgs]) error {
		called = true
		return nil
	}), Spec{}, 0)

	if err := governed.Work(context.Background(), nil); err != nil {
		t.Fatalf("Work: %v", err)
	}
	if !called {
		t.Error("Govern must forward Work to the wrapped worker, not swallow it")
	}
}

type workFunc[T river.JobArgs] func(context.Context, *river.Job[T]) error

func (f workFunc[T]) Work(ctx context.Context, j *river.Job[T]) error { return f(ctx, j) }
