// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package overlay

// The sweep schedule is written by ONE clock, derived from this package's
// source rather than remembered. DueOverlayConnections compares next_sweep_at
// against now() INSIDE Postgres (connectionreads.go), so every statement that
// writes the column has to take its value from that same now(); a deadline
// bound from Go makes the comparison cross-clock, and two clocks are only ever
// coincidentally equal.
//
// The check itself lives in gatekit, because capture owes exactly the same
// thing for capture_sync_state.next_sync_at and the tree had written this
// rationale as a comment twice before either place had a gate. Scope is this
// package because overlay_sync_state is owned here, pinned by
// tableownership_test.go — no other package may write the column at all.

import (
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/gatekit"
)

func TestEverySweepScheduleWriteTakesTheDatabaseClock(t *testing.T) {
	gatekit.DatabaseClock{Dir: ".", Column: "next_sweep_at"}.Require(t)
}
