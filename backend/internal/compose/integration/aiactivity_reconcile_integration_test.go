// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The repair path: an event that never arrived costs a delay, not a
// permanently wrong display.
//
// The loss is simulated by DELETING the projection row after a real reading
// wrote it, which is legitimate here for the reason seeding one would not be —
// this suite is modelling an event the consumer never received, not inventing a
// truth the writer never wrote. Everything the reconciler then puts back comes
// from the source table through the same emitter the ordinary path uses.

import (
	"context"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/activities"
)

// deleteProjection drops the occurrence's row, standing in for an event the
// consumer group never saw — trimmed off the stream while it was behind, which
// XAUTOCLAIM cannot recover.
func (f *readingFixture) deleteProjection(t *testing.T) {
	t.Helper()
	tag, err := f.env.Pool.Exec(context.Background(),
		`DELETE FROM ai_task_run WHERE source = $1 AND occurrence_key = $2`,
		"attachment_extraction", f.readID.String())
	if err != nil {
		t.Fatalf("simulating the lost event: %v", err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("deleted %d projection rows, want 1 — the test is not simulating a loss if there was nothing there", tag.RowsAffected())
	}
}

// A live reading the projection missed entirely comes back, in the state the
// SOURCE says it is in — not the state it was in when the event was lost.
func TestTheReconcilerRestoresAReadingTheProjectionNeverHeardAbout(t *testing.T) {
	f := newReadingFixture(t)
	f.drain(t)
	claim, err := f.store.BeginExtractionRead(f.ctx, f.readID, activities.ExtractionReadLease)
	if err != nil {
		t.Fatalf("BeginExtractionRead: %v", err)
	}
	f.drain(t)
	f.deleteProjection(t)

	announced, err := f.store.ReconcileExtractionActivity(f.ctx, 100, f.dbNow(t))
	if err != nil {
		t.Fatalf("ReconcileExtractionActivity: %v", err)
	}
	if announced == 0 {
		t.Fatal("the reconciler announced nothing; a live reading is exactly what it exists to re-assert")
	}
	f.drain(t)

	got := f.projection(t)
	if got.State != "running" || got.Attempt != 1 {
		t.Fatalf("state/attempt = %s/%d, want running/1 — the source's own current state", got.State, got.Attempt)
	}
	if claim.StartedAt == nil {
		t.Fatal("a claimed reading carries no start time")
	}
}

// A settled reading inside the window is re-asserted too, and that is the case
// that matters most: a reading whose CLOSING event was lost renders forever as
// running, which is the display the whole design exists to prevent.
func TestTheReconcilerRestoresASettledReadingInsideItsWindow(t *testing.T) {
	f := newReadingFixture(t)
	claim, err := f.store.BeginExtractionRead(f.ctx, f.readID, activities.ExtractionReadLease)
	if err != nil {
		t.Fatalf("BeginExtractionRead: %v", err)
	}
	if err := f.store.FinishExtractionRead(f.ctx, f.readID, activities.ExtractionReadOutcome{
		Status: activities.ExtractionReadFailed, ClaimedAt: *claim.StartedAt,
		Detail: "extraction_unavailable",
	}); err != nil {
		t.Fatalf("FinishExtractionRead: %v", err)
	}
	f.drain(t)
	f.deleteProjection(t)

	if _, err := f.store.ReconcileExtractionActivity(f.ctx, 100, f.dbNow(t)); err != nil {
		t.Fatalf("ReconcileExtractionActivity: %v", err)
	}
	f.drain(t)

	if got := f.projection(t); got.State != "failed" {
		t.Fatalf("state = %s, want failed — a reading whose close was lost must not stay live", got.State)
	}
}

// A reading settled longer ago than the window is NOT re-announced. Past the
// window the projection has aged the row out on purpose, and re-publishing it
// would resurrect an occurrence the retention pass deliberately dropped.
func TestTheReconcilerLeavesAReadingPastItsWindowAlone(t *testing.T) {
	f := newReadingFixture(t)
	claim, err := f.store.BeginExtractionRead(f.ctx, f.readID, activities.ExtractionReadLease)
	if err != nil {
		t.Fatalf("BeginExtractionRead: %v", err)
	}
	if err := f.store.FinishExtractionRead(f.ctx, f.readID, activities.ExtractionReadOutcome{
		Status: activities.ExtractionReadDone, ClaimedAt: *claim.StartedAt,
		Detail: "the document states none of the four fields",
	}); err != nil {
		t.Fatalf("FinishExtractionRead: %v", err)
	}
	f.drain(t)
	f.deleteProjection(t)

	// The pass's clock is a PARAMETER, so this states the instant it means
	// rather than ageing a row and racing the wall clock to read it back.
	future := f.dbNow(t).Add(2 * activities.ExtractionActivityReconcileWindow)
	announced, err := f.store.ReconcileExtractionActivity(f.ctx, 100, future)
	if err != nil {
		t.Fatalf("ReconcileExtractionActivity: %v", err)
	}
	if announced != 0 {
		t.Fatalf("the reconciler announced %d reading(s) past its window; it would resurrect what retention dropped", announced)
	}
}
