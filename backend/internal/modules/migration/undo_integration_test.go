// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package migration

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// fakeUndoWriters is UndoWriters over a plain map — this package never
// imports people, so a real archive path is compose's own integration
// test (csvimport_integration_test.go); this one proves RunStore.Undo's
// own SQL: the kept/reversed/errored split, checkpoint paging/resume, and
// the lifecycle gates.
type fakeUndoWriters struct {
	reversed map[ids.UUID]bool
	// failAlways, if set, refuses every time this native id is reversed —
	// wrapped in apperrors.ErrConflict, a deterministic per-ROW refusal (a
	// business rule, a row-scope miss) that must land in the errored
	// bucket and never stop the rest of the run.
	failAlways ids.UUID
	// failUnreachable, if set, errors every time this native id is
	// reversed with a plain (unclassified) error — simulating the estate
	// itself being unreachable (a dropped connection, a timeout), which
	// must abort the pass and leave the run resumable rather than land in
	// errored.
	failUnreachable ids.UUID
}

func (w *fakeUndoWriters) Reverse(_ context.Context, _ string, nativeID ids.UUID) error {
	if nativeID == w.failAlways {
		return fmt.Errorf("simulated business-rule refusal: %w", apperrors.ErrConflict)
	}
	if nativeID == w.failUnreachable {
		return errors.New("simulated infrastructure failure")
	}
	if w.reversed == nil {
		w.reversed = map[ids.UUID]bool{}
	}
	w.reversed[nativeID] = true
	return nil
}

// completeCSVRun drives a fresh staged run all the way to `complete`, the
// only state Undo starts from.
func completeCSVRun(ctx context.Context, t *testing.T, s *RunStore) Run {
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

// landLead records one import_record_map row for the run, the bookkeeping a
// real Writers.Ensure commits alongside the native row.
func landLead(ctx context.Context, t *testing.T, s *RunStore, runID RunID, externalID string) ids.UUID {
	t.Helper()
	native := ids.NewV7()
	if err := s.RecordIdentity(ctx, runID, "import:csv", ObjectLead, externalID, native); err != nil {
		t.Fatalf("RecordIdentity: %v", err)
	}
	return native
}

// markHumanTouched inserts the audit_log row humanTouchedSince reads: a
// human action, occurring now — always after the row's own created_at,
// which a moment-earlier RecordIdentity call already committed.
func markHumanTouched(ctx context.Context, t *testing.T, db interface {
	Tx(context.Context, func(pgx.Tx) error) error
}, action string, nativeID ids.UUID,
) {
	t.Helper()
	err := db.Tx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO audit_log (id, actor_type, actor_id, action, entity_type, entity_id, occurred_at)
			VALUES ($1, 'human', 'human:tester', $2, $3, $4, now())`,
			ids.NewV7(), action, ObjectLead, nativeID)
		return err
	})
	if err != nil {
		t.Fatalf("seeding a human-touch audit row: %v", err)
	}
}

func TestUndoReversesUntouchedRowsAndKeepsHumanEdited(t *testing.T) {
	ctx, db := testWorkspaceCtx(t, adminImportRunGrant())
	s := NewRunStore(db)
	run := completeCSVRun(ctx, t, s)

	untouched := landLead(ctx, t, s, run.ID, "row-1")
	edited := landLead(ctx, t, s, run.ID, "row-2")
	markHumanTouched(ctx, t, db, "update", edited)

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
	if len(rep.Errored) != 0 {
		t.Fatalf("errored = %+v, want none", rep.Errored)
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

// A93's protection is "has a human touched this row", not narrowly
// "updated" it: a lead a human independently disqualified outside the
// import (an 'archive' audit action, not 'update') must still be kept, not
// reversed out from under them.
func TestUndoKeepsARowAHumanTouchedThroughAnyAction(t *testing.T) {
	ctx, db := testWorkspaceCtx(t, adminImportRunGrant())
	s := NewRunStore(db)
	run := completeCSVRun(ctx, t, s)

	disqualified := landLead(ctx, t, s, run.ID, "row-1")
	markHumanTouched(ctx, t, db, "archive", disqualified)

	w := &fakeUndoWriters{}
	rep, err := s.Undo(ctx, run.ID, w)
	if err != nil {
		t.Fatalf("Undo: %v", err)
	}
	if rep.ReversedCount != 0 || len(rep.Kept) != 1 || rep.Kept[0].ID != disqualified {
		t.Fatalf("undo report = %+v, want the disqualified row kept, not reversed", rep)
	}
	if w.reversed[disqualified] {
		t.Fatal("a row a human touched (any action) was reversed")
	}
}

// A row Reverse cannot process — a business rule refuses it, or it is no
// longer visible — is recorded and left in place, and the rest of the run
// still completes. The old behaviour (abort on the first such row) wedged
// the run in `undoing` forever, since a deterministic per-row refusal fails
// identically on every retry.
func TestUndoRecordsAnUnreversibleRowAndContinues(t *testing.T) {
	ctx, db := testWorkspaceCtx(t, adminImportRunGrant())
	s := NewRunStore(db)
	run := completeCSVRun(ctx, t, s)

	stuck := landLead(ctx, t, s, run.ID, "row-1")
	fine := landLead(ctx, t, s, run.ID, "row-2")

	w := &fakeUndoWriters{failAlways: stuck}
	rep, err := s.Undo(ctx, run.ID, w)
	if err != nil {
		t.Fatalf("Undo: %v", err)
	}
	if rep.ReversedCount != 1 || !w.reversed[fine] {
		t.Fatalf("undo report = %+v, want the other row reversed despite the stuck one", rep)
	}
	if len(rep.Errored) != 1 || rep.Errored[0].ID != stuck || rep.Errored[0].Reason == "" {
		t.Fatalf("errored = %+v, want the stuck row named with a reason", rep.Errored)
	}
	got, err := s.GetStaged(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetStaged: %v", err)
	}
	if got.Status != StatusUndone {
		t.Fatalf("status = %q, want undone — one unreversible row must not wedge the whole run", got.Status)
	}
}

// An error that is NOT a row refusal — the estate itself unreachable, not a
// fact about one record — must abort the pass and leave the run resumable,
// never landing in errored (which would misreport every row after it as
// individually unreversible) and never completing the run as `undone` (which
// would make it unrecoverable).
func TestUndoStopsResumableOnAnUnclassifiedFailure(t *testing.T) {
	ctx, db := testWorkspaceCtx(t, adminImportRunGrant())
	s := NewRunStore(db)
	run := completeCSVRun(ctx, t, s)

	unreachable := landLead(ctx, t, s, run.ID, "row-1")

	w := &fakeUndoWriters{failUnreachable: unreachable}
	if _, err := s.Undo(ctx, run.ID, w); err == nil {
		t.Fatal("Undo with an unreachable-estate error returned nil, want the error")
	}
	got, err := s.GetStaged(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetStaged: %v", err)
	}
	if got.Status != StatusUndoing {
		t.Fatalf("status = %q, want undoing — the run must stay resumable, not finish as undone", got.Status)
	}
	if got.UndoReport != nil && len(got.UndoReport.Errored) != 0 {
		t.Fatalf("errored = %+v, want the unreachable row NOT recorded as an individual refusal", got.UndoReport.Errored)
	}

	// A later call with a writer that can reach the estate resumes and
	// reverses the row the first attempt never got to record either way.
	resumed := &fakeUndoWriters{}
	rep, err := s.Undo(ctx, run.ID, resumed)
	if err != nil {
		t.Fatalf("resumed Undo: %v", err)
	}
	if rep.ReversedCount != 1 || !resumed.reversed[unreachable] {
		t.Fatalf("resumed undo report = %+v, want the row reversed", rep)
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

// A second call while an undo is genuinely under way for the same run is a
// conflict, not a second concurrent pass over the same rows — beginUndo's
// row lock only covers its own transaction, so it is claimUndo's advisory
// lock (held for the whole call) that must refuse this, not the lifecycle
// check alone.
func TestUndoRefusesAConcurrentSecondCallOnTheSameRun(t *testing.T) {
	ctx, db := testWorkspaceCtx(t, adminImportRunGrant())
	s := NewRunStore(db)
	run := completeCSVRun(ctx, t, s)
	landLead(ctx, t, s, run.ID, "row-1")

	release, err := s.claimUndo(ctx, run.ID)
	if err != nil {
		t.Fatalf("claimUndo: %v", err)
	}
	defer release()

	if _, err := s.Undo(ctx, run.ID, &fakeUndoWriters{}); !errors.Is(err, apperrors.ErrConflict) {
		t.Fatalf("undo while the lock is held err = %v, want ErrConflict", err)
	}
}

// A run resumed (or re-entered on a later page) must not miss an edit made
// while it was still awaiting the next page — the reference instant is per
// row (import_record_map.created_at), not the run's own last-touched time,
// which advances every page.
func TestUndoResumesFromAPersistedCheckpointAcrossPages(t *testing.T) {
	ctx, db := testWorkspaceCtx(t, adminImportRunGrant())
	s := NewRunStore(db)
	run := completeCSVRun(ctx, t, s)

	first := landLead(ctx, t, s, run.ID, "row-1")
	second := landLead(ctx, t, s, run.ID, "row-2")

	// Simulate a crash after one page (here, one row) already reversed and
	// its checkpoint persisted: begin the reversal directly, then advance
	// the cursor past `first` without going through the public loop.
	if _, _, err := s.beginUndo(ctx, run.ID); err != nil {
		t.Fatalf("beginUndo: %v", err)
	}
	partial := UndoReport{ReversedCount: 1}
	if err := s.advanceUndoCheckpoint(ctx, run.ID, 1, partial); err != nil {
		t.Fatalf("simulating a partial undo: %v", err)
	}

	resumed := &fakeUndoWriters{}
	rep, err := s.Undo(ctx, run.ID, resumed)
	if err != nil {
		t.Fatalf("resumed Undo: %v", err)
	}
	if resumed.reversed[first] {
		t.Fatal("the resumed call redid a row the persisted checkpoint already covered")
	}
	if !resumed.reversed[second] {
		t.Fatal("the resumed call never reached the row after the checkpoint")
	}
	// 1 carried from the simulated first page + 1 the resumed call itself
	// reversed — the count survives across pages, not just within one.
	if rep.ReversedCount != 2 {
		t.Fatalf("reversed_count = %d, want 2 (1 carried + 1 new)", rep.ReversedCount)
	}
}

// Undo resolves its run before it reverses anything, so a run id that names no
// run answers not-found rather than reporting a successful undo of nothing.
func TestUndoOfAnUnknownRunIsNotFound(t *testing.T) {
	ctx, db := testWorkspaceCtx(t, adminImportRunGrant())
	s := NewRunStore(db)
	if _, err := s.Undo(ctx, RunID(ids.NewV7()), &fakeUndoWriters{}); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("Undo of an unknown run = %v, want ErrNotFound", err)
	}
}
