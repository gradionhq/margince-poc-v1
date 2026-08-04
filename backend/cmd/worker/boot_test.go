// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

import (
	"context"
	"sync"
	"testing"
)

// run() closes the bus and the pool on deferred calls that fire after the lanes
// are joined, so join() has to actually end them — the ordering is invisible at
// runtime until a subscriber reads a closed pool.
//
// That startEventLanes returns a JOINABLE value on its failure paths is not
// covered here: driving it to a real failure needs a live bus and pool, so it
// belongs to the integration lane. A unit test that rebuilt the struct by hand
// and asserted its own fields would only restate its own setup.

// TestJoinCancelsTheLanesAndWaitsForThem is the property: join() cancels the
// lanes' context and does not return until each goroutine has left its handler.
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
