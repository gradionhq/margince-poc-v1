// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package migration

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
)

// fakeSource serves a fixed estate in a stable order.
type fakeSource struct {
	objects map[string][]Row
	order   []string
	assocs  []Assoc
}

func (f *fakeSource) Objects() []string { return f.order }

func (f *fakeSource) Counts(context.Context) (map[string]int, error) {
	c := make(map[string]int, len(f.objects))
	for k, rows := range f.objects {
		c[k] = len(rows)
	}
	return c, nil
}

func (f *fakeSource) Rows(_ context.Context, object string, offset, limit int) ([]Row, error) {
	rows := f.objects[object]
	if offset >= len(rows) {
		return nil, nil
	}
	end := min(offset+limit, len(rows))
	return rows[offset:end], nil
}

func (f *fakeSource) Associations(context.Context) ([]Assoc, error) { return f.assocs, nil }

// fakeWriters records every ensure; existing simulates rows already
// landed natively; failAt injects a crash at the Nth ensure call.
type fakeWriters struct {
	existing map[string]bool // object+"/"+ext
	ensured  []string
	assocs   []Assoc
	calls    int
	failAt   int // 0 = never
}

func (w *fakeWriters) Exists(_ context.Context, object, ext string) (bool, error) {
	return w.existing[object+"/"+ext], nil
}

func (w *fakeWriters) Ensure(_ context.Context, object string, row Row) (EnsureResult, error) {
	w.calls++
	if w.failAt > 0 && w.calls == w.failAt {
		return EnsureResult{}, errors.New("injected crash")
	}
	key := object + "/" + row.ExternalID
	created := !w.existing[key]
	w.existing[key] = true
	w.ensured = append(w.ensured, key)
	return EnsureResult{Created: created}, nil
}

func (w *fakeWriters) Associate(_ context.Context, a Assoc) error {
	w.assocs = append(w.assocs, a)
	return nil
}

// fakeRuns is the in-memory run record — the loop's checkpoint/resume
// contract is provable without Postgres (the SQL RunStore has its own
// integration coverage).
type fakeRuns struct{ run Run }

func newFakeRuns() *fakeRuns {
	return &fakeRuns{run: Run{Status: StatusRunning}}
}

func (r *fakeRuns) Get(context.Context, RunID) (Run, error) { return r.run, nil }

func (r *fakeRuns) advanceCheckpoint(_ context.Context, _ RunID, checkpoint int) error {
	r.run.Checkpoint = checkpoint
	return nil
}

func (r *fakeRuns) complete(_ context.Context, _ RunID, rep Report) error {
	r.run.Status = StatusComplete
	r.run.Report = &rep
	return nil
}

func (r *fakeRuns) failRun(_ context.Context, _ RunID, cause error) error {
	r.run.Status = StatusFailed
	r.run.Error = cause.Error()
	return nil
}

func twoObjectSource() *fakeSource {
	return &fakeSource{
		order: []string{"organization", "person"},
		objects: map[string][]Row{
			"organization": {
				{ExternalID: "org-1", Fields: map[string]any{"display_name": "BÄR Pharma"}},
				{ExternalID: "org-2", Fields: map[string]any{"display_name": "Gitex"}},
			},
			"person": {
				{ExternalID: "p-1", Fields: map[string]any{"full_name": "Mor Anders"}},
				{ExternalID: "p-2", Fields: map[string]any{}}, // payload-less → disclosed skip
				{ExternalID: "p-3", Fields: map[string]any{"full_name": "Riya Patel"}},
			},
		},
		assocs: []Assoc{{FromType: "person", FromID: "p-1", ToType: "organization", ToID: "org-1", Category: "employment"}},
	}
}

func TestDryRunClassifiesWithoutWriting(t *testing.T) {
	src := twoObjectSource()
	w := &fakeWriters{existing: map[string]bool{"person/p-1": true}}
	e := &Engine{w: w} // no run records on purpose: a dry-run must never touch them

	rep, err := e.DryRun(context.Background(), src)
	if err != nil {
		t.Fatalf("DryRun: %v", err)
	}
	if len(w.ensured) != 0 || len(w.assocs) != 0 {
		t.Fatalf("dry-run wrote: ensured=%v assocs=%v", w.ensured, w.assocs)
	}
	byObject := map[string]ObjectReport{}
	for _, or := range rep.Objects {
		byObject[or.Object] = or
	}
	org := byObject["organization"]
	if org.WillCreate != 2 || org.WillUpdate != 0 || org.MirrorCount != 2 {
		t.Errorf("organization report = %+v, want 2 creates of 2", org)
	}
	person := byObject["person"]
	if person.WillCreate != 1 || person.WillUpdate != 1 {
		t.Errorf("person report = %+v, want 1 create + 1 update", person)
	}
	if len(person.Skipped) != 1 || person.Skipped[0].Reason != "empty_payload" || person.Skipped[0].ExternalID != "p-2" {
		t.Errorf("person skips = %+v, want p-2 skipped as empty_payload", person.Skipped)
	}
	if rep.Associations != 1 {
		t.Errorf("associations = %d, want 1", rep.Associations)
	}
}

func TestRunImportsInOrderWithSkipsDisclosed(t *testing.T) {
	src := twoObjectSource()
	w := &fakeWriters{existing: map[string]bool{}}
	runs := newFakeRuns()
	e := &Engine{runs: runs, w: w}

	rep, err := e.Run(context.Background(), RunID{}, src)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	wantOrder := []string{"organization/org-1", "organization/org-2", "person/p-1", "person/p-3"}
	if len(w.ensured) != len(wantOrder) {
		t.Fatalf("ensured %v, want %v", w.ensured, wantOrder)
	}
	for i, k := range wantOrder {
		if w.ensured[i] != k {
			t.Fatalf("ensure order %v, want %v (parents before dependents)", w.ensured, wantOrder)
		}
	}
	if rep.Imported != 4 {
		t.Errorf("imported = %d, want 4", rep.Imported)
	}
	if len(w.assocs) != 1 {
		t.Errorf("assocs applied = %d, want 1", len(w.assocs))
	}
	if runs.run.Status != StatusComplete || runs.run.Report == nil {
		t.Errorf("run = %+v, want complete with report", runs.run)
	}
	// The skip is disclosed, never silent (AC-mode-flip-7).
	var personRep ObjectReport
	for _, or := range rep.Objects {
		if or.Object == "person" {
			personRep = or
		}
	}
	if len(personRep.Skipped) != 1 || personRep.Skipped[0].Reason != "empty_payload" {
		t.Errorf("person skips = %+v, want the payload-less row disclosed", personRep.Skipped)
	}
}

func TestRunResumesFromCheckpointAndConverges(t *testing.T) {
	src := twoObjectSource()
	w := &fakeWriters{existing: map[string]bool{}, failAt: 3} // crash on person/p-1
	runs := newFakeRuns()
	e := &Engine{runs: runs, w: w}

	if _, err := e.Run(context.Background(), RunID{}, src); err == nil {
		t.Fatal("Run must surface the injected crash")
	}
	if runs.run.Status != StatusFailed || runs.run.Error == "" {
		t.Fatalf("crashed run = %+v, want failed with the cause recorded", runs.run)
	}
	if runs.run.Checkpoint != 2 {
		t.Fatalf("checkpoint after crash = %d, want 2 (both organizations landed, the crashed row not)", runs.run.Checkpoint)
	}

	// Resume: same run id, cursor intact — the end state must equal an
	// uninterrupted run's (IEM-FORM-1: never from zero, never past it).
	runs.run.Status = StatusRunning
	w.failAt = 0
	rep, err := e.Run(context.Background(), RunID{}, src)
	if err != nil {
		t.Fatalf("resumed Run: %v", err)
	}
	if runs.run.Status != StatusComplete {
		t.Fatalf("resumed run status = %s, want complete", runs.run.Status)
	}
	uniq := map[string]bool{}
	for _, k := range w.ensured {
		if uniq[k] {
			t.Errorf("row %s ensured twice as a create — resume must not duplicate", k)
		}
		uniq[k] = true
	}
	if len(uniq) != 4 {
		t.Errorf("unique rows landed = %d, want 4 (identical to an uninterrupted run)", len(uniq))
	}
	if rep.Imported != 2 {
		t.Errorf("resumed attempt imported = %d, want 2 (only the remaining person rows)", rep.Imported)
	}
}

func TestRunRefusesANonRunningRecord(t *testing.T) {
	runs := newFakeRuns()
	runs.run.Status = StatusComplete
	e := &Engine{runs: runs, w: &fakeWriters{existing: map[string]bool{}}}
	_, err := e.Run(context.Background(), RunID{}, twoObjectSource())
	if !errors.Is(err, apperrors.ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict for a non-running record", err)
	}
}

func TestGuardIncumbentSourceBlocksRevokedAndError(t *testing.T) {
	if err := GuardIncumbentSource("active"); err != nil {
		t.Fatalf("active must pass: %v", err)
	}
	for _, status := range []string{"revoked", "error"} {
		err := GuardIncumbentSource(status)
		if err == nil {
			t.Fatalf("status %q must refuse a live-read import", status)
		}
		if !errors.Is(err, apperrors.ErrConflict) {
			t.Errorf("status %q: err = %v, want ErrConflict identity", status, err)
		}
		if !strings.Contains(err.Error(), ReasonIncumbentUnreachable) {
			// The reason constant must appear verbatim so the importer path
			// and the preflight blocking[] can never drift apart.
			t.Errorf("status %q: error %q must carry %s", status, err, ReasonIncumbentUnreachable)
		}
	}
}
