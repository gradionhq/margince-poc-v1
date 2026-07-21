// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package overlay

// MirrorSyncedTotal/MirrorConflictTotal's own unit-level proof: both are
// thin reads over a package-level atomic counter this file's package-
// internal test can advance directly (mirrorstore.go's Ingest and
// reconcile.go's Reconcile are the real production increments, proven
// by their own integration suites) — this test only pins that the
// exported getter actually reflects the counter it reads, a relative
// delta so it stays correct however many times other tests in this
// package have already incremented the same process-wide counter.

import "testing"

func TestMirrorSyncedTotalReflectsTheCounter(t *testing.T) {
	before := MirrorSyncedTotal()
	// The counter is a process-wide global; restore it so this test's
	// advance never contaminates another test that reads it absolutely.
	t.Cleanup(func() { mirrorSyncedTotal.Store(before) })
	mirrorSyncedTotal.Add(3)
	if got := MirrorSyncedTotal(); got != before+3 {
		t.Fatalf("MirrorSyncedTotal() = %d, want %d (before %d + 3)", got, before+3, before)
	}
}

func TestMirrorConflictTotalReflectsTheCounter(t *testing.T) {
	before := MirrorConflictTotal()
	// The counter is a process-wide global; restore it so this test's
	// advance never contaminates another test that reads it absolutely.
	t.Cleanup(func() { mirrorConflictTotal.Store(before) })
	mirrorConflictTotal.Add(2)
	if got := MirrorConflictTotal(); got != before+2 {
		t.Fatalf("MirrorConflictTotal() = %d, want %d (before %d + 2)", got, before+2, before)
	}
}
