// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package migration

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// fakeUndoWriters is UndoWriters over a plain map — this package never
// imports people, so a real archive path is compose's own integration
// test (csvimport_integration_test.go); this one proves RunStore.Undo's
// own SQL: the kept/reversed split, checkpoint resume, and the lifecycle
// gates.
type fakeUndoWriters struct {
	reversed map[ids.UUID]bool
	// failOnce, if set, errors the FIRST time this native id is reversed
	// and succeeds on any later attempt — the crash-then-resume window
	// Reverse's own idempotence contract exists for.
	failOnce ids.UUID
	failed   bool
}

func (w *fakeUndoWriters) Reverse(_ context.Context, _ string, nativeID ids.UUID) error {
	if !w.failed && nativeID == w.failOnce {
		w.failed = true
		return errors.New("simulated reversal failure")
	}
	if w.reversed == nil {
		w.reversed = map[ids.UUID]bool{}
	}
	w.reversed[nativeID] = true
	return nil
}

// completeCSVRun drives a fresh staged run all the way to `complete`, the
// only state Undo starts from, and returns the run plus a helper closure
// that lands one import_record_map row for it (mirroring what a real
// Writers.Ensure commits alongside the native row).
func completeCSVRun(t *testing.T, s *RunStore, ctx context.Context) Run {
	t.Helper()
	run, err := s.CreateStagedRun(ctx, CreateStagedRunInput{
		Connector: ConnectorCSV, SourceRef: "src", Source: "import_api",
		Mapping: RunMapping{Object: ObjectLead, Fields: map[string]string{"Email": "email"}, SourceKey: "Email"},
	})
	if err != nil {
		t.Fatalf("CreateStagedRun: %v", err)
	}
	if err := s.AwaitApproval(ctx, run.ID, Report{}); err != nil {
		t.Fatalf("AwaitApproval: %v", err)
	}
	if _, err := s.Approve(ctx, run.ID); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if err := s.complete(ctx, run.ID, Report{Imported: 2}); err != nil {
		t.Fatalf("complete: %v", err)
	}
	got, err := s.GetStaged(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetStaged: %v", err)
	}
	return got
}

func landLead(t *testing.T, s *RunStore, ctx context.Context, runID RunID, externalID string) ids.UUID {
	t.Helper()
	native := ids.NewV7()
	if err := s.RecordIdentity(ctx, runID, "import:csv", ObjectLead, externalID, native); err != nil {
		t.Fatalf("RecordIdentity: %v", err)
	}
	return native
}

// markHumanEdited inserts the audit_log row humanEditedSince reads: a
// human 'update' after the run's completion instant.
func markHumanEdited(t *testing.T, db interface {
	Tx(context.Context, func(pgx.Tx) error) error
}, ctx context.Context, nativeID ids.UUID, since time.Time) {
	t.Helper()
	err := db.Tx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO audit_log (id, workspace_id, actor_type, actor_id, action, entity_type, entity_id, occurred_at)
			VALUES ($1, NULLIF(current_setting('app.workspace_id', true), '')::uuid, 'human', 'human:tester', 'update', $2, $3, $4)`,
			ids.NewV7(), ObjectLead, nativeID, since.Add(time.Minute))
		return err
	})
	if err != nil {
		t.Fatalf("seeding a human-edit audit row: %v", err)
	}
}

func TestUndoReversesUntouchedRowsAndKeepsHumanEdited(t *testing.T) {
	ctx, db := testWorkspaceCtx(t, adminImportRunGrant())
	s := NewRunStore(db)
	run := completeCSVRun(t, s, ctx)

	untouched := landLead(t, s, ctx, run.ID, "row-1")
	edited := landLead(t, s, ctx, run.ID, "row-2")
	markHumanEdited(t, db, ctx, edited, run.UpdatedAt)

	w := &fakeUndoWriters{}
	rep, err := s.Undo(ctx, run.ID, w)
	if err != nil {
		t.Fatalf("Undo: %v", err)
	}
	if rep.ReversedCount != 1 || !w.reversed[untouched] {
		t.Fatalf("undo report = %+v, reversed = %v, want the untouched row reversed and nothing else", rep, w.reversed)
	}
	if len(rep.Kept) != 1 || rep.Kept[0].ID != edited || rep.Kept[0].Object != ObjectLead {
		t.Fatalf("kept = %+v, want the human-edited row named", rep.Kept)
	}
	if w.reversed[edited] {
		t.Fatal("the human-edited row was reversed — A93 requires it be left in place")
	}

	got, err := s.GetStaged(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetStaged after undo: %v", err)
	}
	if got.Status != StatusUndone || got.UndoReport == nil || got.UndoReport.ReversedCount != 1 {
		t.Fatalf("run after undo = %+v, want status undone with the report persisted", got)
	}

	// Undoing an already-undone run is a conflict, not a no-op.
	if _, err := s.Undo(ctx, run.ID, w); !errors.Is(err, apperrors.ErrConflict) {
		t.Fatalf("second undo err = %v, want ErrConflict", err)
	}
}

func TestUndoRefusesEveryConnectorButCSV(t *testing.T) {
	ctx, db := testWorkspaceCtx(t, adminImportRunGrant())
	s := NewRunStore(db)
	run, err := s.Create(ctx, CreateRunInput{Connector: ConnectorMirror, SourceRef: "snap", Source: "overlay:flip"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := s.complete(ctx, run.ID, Report{}); err != nil {
		t.Fatalf("complete: %v", err)
	}
	if _, err := s.Undo(ctx, run.ID, &fakeUndoWriters{}); !errors.Is(err, apperrors.ErrConflict) {
		t.Fatalf("undo of a mirror run err = %v, want ErrConflict — undo is csv-only", err)
	}
}

func TestUndoRefusesAnythingButComplete(t *testing.T) {
	ctx, db := testWorkspaceCtx(t, adminImportRunGrant())
	s := NewRunStore(db)
	run, err := s.CreateStagedRun(ctx, CreateStagedRunInput{
		Connector: ConnectorCSV, SourceRef: "src", Source: "import_api",
		Mapping: RunMapping{Object: ObjectLead, Fields: map[string]string{"Email": "email"}, SourceKey: "Email"},
	})
	if err != nil {
		t.Fatalf("CreateStagedRun: %v", err)
	}
	if _, err := s.Undo(ctx, run.ID, &fakeUndoWriters{}); !errors.Is(err, apperrors.ErrConflict) {
		t.Fatalf("undo of a validating run err = %v, want ErrConflict", err)
	}
}

// TestUndoResumesAfterAPartialFailure proves the checkpoint/Since contract
// IEM-WIRE-9 promises: a reversal interrupted mid-way is picked up again
// by calling undo a second time, from where it stopped — not from the
// start, and the human-edit reference instant does not move with it.
func TestUndoResumesAfterAPartialFailure(t *testing.T) {
	ctx, db := testWorkspaceCtx(t, adminImportRunGrant())
	s := NewRunStore(db)
	run := completeCSVRun(t, s, ctx)

	first := landLead(t, s, ctx, run.ID, "row-1")
	second := landLead(t, s, ctx, run.ID, "row-2")

	failing := &fakeUndoWriters{failOnce: first}
	if _, err := s.Undo(ctx, run.ID, failing); err == nil {
		t.Fatal("Undo with a failing writer returned nil, want the row's error")
	}
	stopped, err := s.GetStaged(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetStaged after the interrupted undo: %v", err)
	}
	if stopped.Status != StatusUndoing || stopped.Checkpoint != 0 {
		t.Fatalf("interrupted run = %+v, want undoing at checkpoint 0 (the failed row never advanced)", stopped)
	}

	// A second call with a writer that no longer fails resumes rather than
	// restarting: `first` was already attempted (and errored, so it was
	// never reversed) and `second` was never reached.
	resumed := &fakeUndoWriters{}
	rep, err := s.Undo(ctx, run.ID, resumed)
	if err != nil {
		t.Fatalf("resumed Undo: %v", err)
	}
	if rep.ReversedCount != 2 || !resumed.reversed[first] || !resumed.reversed[second] {
		t.Fatalf("resumed undo report = %+v, reversed = %v, want both rows reversed", rep, resumed.reversed)
	}
}

func TestUndoIsTenantFenced(t *testing.T) {
	ctxA, dbA := testWorkspaceCtx(t, adminImportRunGrant())
	sA := NewRunStore(dbA)
	run := completeCSVRun(t, sA, ctxA)

	ctxB, dbB := testWorkspaceCtx(t, adminImportRunGrant())
	sB := NewRunStore(dbB)
	if _, err := sB.Undo(ctxB, run.ID, &fakeUndoWriters{}); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("cross-workspace Undo err = %v, want ErrNotFound (existence-hiding)", err)
	}
}
