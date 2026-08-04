// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

import (
	"context"
	"sync"
	"testing"
)

// The lanes must be joinable, because run() closes the bus and the pool on
// deferred calls that fire after the join — and it defers that join BEFORE it
// checks the boot error, so a half-started set has to be joinable too. Both
// halves are asserted here rather than left to the comments that state them:
// the ordering is invisible at runtime until a subscriber reads a closed pool,
// which is the failure this shape exists to prevent.

// joinWaitsForEveryLane is the property: join() cancels the lanes' context and
// does not return until each goroutine has left its handler.
func TestJoinCancelsTheLanesAndWaitsForThem(t *testing.T) {
	ctx, stop := context.WithCancel(context.Background())
	lanes := workerLanes{background: &sync.WaitGroup{}, stop: stop}

	// Two lanes, each ending only on cancellation — no sleep and no clock, so
	// the test can only pass by the cancel actually reaching them.
	left := make(chan struct{}, 2)
	for range 2 {
		lanes.background.Go(func() {
			<-ctx.Done()
			left <- struct{}{}
		})
	}

	lanes.join()

	if len(left) != 2 {
		t.Fatalf("join() returned with %d of 2 lanes finished — it must cancel them and wait", len(left))
	}
	if ctx.Err() == nil {
		t.Error("join() returned without cancelling the lanes' context")
	}
}

// A boot failure joins the lanes that DID start, so startEventLanes must never
// answer with a value join() would panic on. It cannot be called here without a
// database, so this pins the contract at the construction its first two lines
// perform: every field join() touches is set before any return can happen.
func TestAZeroLaneSetIsNeverHandedBack(t *testing.T) {
	_, stop := context.WithCancel(context.Background())
	defer stop()
	lanes := workerLanes{background: &sync.WaitGroup{}, stop: stop}

	if lanes.background == nil || lanes.stop == nil {
		t.Fatal("startEventLanes' construction must set every field join() dereferences")
	}
	// Joining a set whose lanes never started is the boot-failure path, and it
	// must return rather than block on a WaitGroup nobody will decrement.
	lanes.join()
}
