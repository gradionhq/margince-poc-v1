// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package migration

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// Every RunStore entry point admits on the import_run object BEFORE it
// opens a transaction, so an ungranted actor is refused without a
// database: the nil pool proves no connection is ever consulted.
func TestRunStoreRefusesUngrantedRole(t *testing.T) {
	ctx := principal.WithActor(context.Background(), principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:ungranted",
		SeatType: principal.SeatFull,
		Permissions: principal.Permissions{
			Objects:  map[string]principal.ObjectGrant{importRunObject: {Read: false}}, // a rep: no import_run grant at all
			RowScope: principal.RowScopeAll,
		},
	})
	s := NewRunStore(nil)

	if _, err := s.Create(ctx, CreateRunInput{Connector: ConnectorMirror, SourceRef: "x", Source: "t"}); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Fatalf("ungranted Create err = %v, want ErrPermissionDenied", err)
	}
	if _, err := s.Latest(ctx, ConnectorMirror); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Fatalf("ungranted Latest err = %v, want ErrPermissionDenied", err)
	}
}

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

// fakeWriters models the real writer's TWO-STEP landing, because the
// gap between the steps is the thing worth testing: `landed` is the
// native record (the module store's own transaction) and `mapped` is
// the identity map (a second one). Exists reads `mapped`, exactly as
// the real lookup does, so a record that landed without being mapped is
// invisible until a reconcile adopts it.
//
// failAt injects a crash at the Nth ensure call; failAfterCreate makes
// that crash land the record first, which is the process death this
// whole seam exists for.
type fakeWriters struct {
	landed          map[string]bool // object+"/"+ext — the native row exists
	mapped          map[string]bool // …and the identity map knows it
	ensured         []string
	assocs          []Assoc
	calls           int
	failAt          int // 0 = never
	failAfterCreate bool
	reconciles      int
}

// newFakeWriters starts with nothing landed and nothing mapped.
func newFakeWriters() *fakeWriters {
	return &fakeWriters{landed: map[string]bool{}, mapped: map[string]bool{}}
}

func (w *fakeWriters) Exists(_ context.Context, object, ext string) (bool, error) {
	return w.mapped[object+"/"+ext], nil
}

// ReconcileIdentities adopts everything that landed but was never
// mapped — the repair the resume depends on.
func (w *fakeWriters) ReconcileIdentities(context.Context) error {
	w.reconciles++
	for key := range w.landed {
		w.mapped[key] = true
	}
	return nil
}

func (w *fakeWriters) Ensure(_ context.Context, object string, row Row) (EnsureResult, error) {
	w.calls++
	key := object + "/" + row.ExternalID
	if w.failAt > 0 && w.calls == w.failAt {
		if w.failAfterCreate {
			// The record IS in the database; the process died before the
			// identity map learned of it.
			w.landed[key] = true
			w.ensured = append(w.ensured, key)
		}
		return EnsureResult{}, errors.New("injected crash")
	}
	// The real writer opens with the same lookup: a row the identity map
	// already binds is a replayed page, not work to redo.
	if w.mapped[key] {
		return EnsureResult{Unchanged: true}, nil
	}
	w.landed[key] = true
	w.mapped[key] = true
	w.ensured = append(w.ensured, key)
	return EnsureResult{Created: true}, nil
}

func (w *fakeWriters) Associate(_ context.Context, a Assoc) (AssocResult, error) {
	if a.ToType == "nowhere" {
		return AssocResult{Reason: "endpoint_not_imported"}, nil
	}
	w.assocs = append(w.assocs, a)
	return AssocResult{Applied: true}, nil
}

// fakeRuns is the in-memory run record — the loop's checkpoint/resume
// contract is provable without Postgres (the SQL RunStore has its own
// integration coverage).
type fakeRuns struct{ run Run }

func newFakeRuns() *fakeRuns {
	return &fakeRuns{run: Run{Status: StatusRunning}}
}

func (r *fakeRuns) Get(context.Context, RunID) (Run, error) { return r.run, nil }

// advanceCheckpoint mirrors the SQL store's monotonic guard (run.go's
// `checkpoint <= $2`): a fake that accepts a backwards cursor would hide
// exactly the resume bugs this suite exists to catch.
func (r *fakeRuns) advanceCheckpoint(_ context.Context, _ RunID, checkpoint int) error {
	if checkpoint < r.run.Checkpoint {
		return fmt.Errorf("checkpoint %d is behind %d: %w", checkpoint, r.run.Checkpoint, apperrors.ErrConflict)
	}
	r.run.Checkpoint = checkpoint
	return nil
}

// complete and failRun both FOLD the attempt's report into whatever the
// run already recorded, exactly as the SQL store does. A double that
// replaced it instead would report a resumed run's final leg only, and
// the understated-count bug would pass this suite untouched.
func (r *fakeRuns) complete(_ context.Context, _ RunID, rep Report) error {
	r.run.Status = StatusComplete
	r.record(rep)
	return nil
}

func (r *fakeRuns) failRun(_ context.Context, _ RunID, rep Report, cause error) error {
	r.run.Status = StatusFailed
	r.run.Error = cause.Error()
	r.record(rep)
	return nil
}

func (r *fakeRuns) record(rep Report) {
	if r.run.Report != nil {
		rep = r.run.Report.mergedWith(rep)
	}
	r.run.Report = &rep
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
		assocs: []Assoc{
			{FromType: "person", FromID: "p-1", ToType: "organization", ToID: "org-1", Category: "employment"},
			// An edge whose target never landed: disclosed, never counted
			// as applied.
			{FromType: "person", FromID: "p-1", ToType: "nowhere", ToID: "x-1", Category: "employment"},
		},
	}
}

func TestDryRunClassifiesWithoutWriting(t *testing.T) {
	src := twoObjectSource()
	w := &fakeWriters{landed: map[string]bool{"person/p-1": true}, mapped: map[string]bool{"person/p-1": true}}
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
	if rep.Associations != 2 {
		t.Errorf("dry-run associations = %d, want 2 (edges OFFERED — the dry-run resolves no endpoints)", rep.Associations)
	}
}

func TestRunImportsInOrderWithSkipsDisclosed(t *testing.T) {
	src := twoObjectSource()
	w := newFakeWriters()
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
	if rep.Associations != 1 {
		t.Errorf("report associations = %d, want 1 APPLIED (not the 2 offered)", rep.Associations)
	}
	if len(rep.AssociationsSkipped) != 1 || rep.AssociationsSkipped[0].Reason != "endpoint_not_imported" {
		t.Errorf("skipped assocs = %+v, want the unresolvable edge disclosed", rep.AssociationsSkipped)
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
	w := newFakeWriters()
	w.failAt = 3 // crash on person/p-1
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

	// The RETURNED report is this attempt's leg — but the RECORDED one is
	// what the operator is shown for a one-way cutover, and it has to
	// cover the whole estate. A stored report that only knew the final
	// leg would tell someone who resumed a crashed flip that half their
	// records never arrived.
	stored := runs.run.Report
	if stored == nil {
		t.Fatal("a completed run must record a report")
	}
	if stored.Imported != 4 {
		t.Errorf("recorded imported = %d, want 4 — the two pre-crash organizations plus the two resumed persons", stored.Imported)
	}
	landed := map[string]int{}
	for _, or := range stored.Objects {
		if _, dup := landed[or.Object]; dup {
			t.Errorf("object %q appears twice in the recorded report; the merge must fold by class", or.Object)
		}
		landed[or.Object] = or.Created + or.Updated
	}
	if landed["organization"] != 2 || landed["person"] != 2 {
		t.Errorf("recorded dispositions = %v, want 2 organizations and 2 persons across both attempts", landed)
	}
}

// threeObjectSource crashes-and-resumes across a LATER class boundary
// than twoObjectSource can reach — the offset where a checkpoint that
// wrongly tracks the finished class's cursor sends the loop backwards.
func threeObjectSource() *fakeSource {
	return &fakeSource{
		order: []string{"organization", "person", "deal"},
		objects: map[string][]Row{
			"organization": {
				{ExternalID: "org-1", Fields: map[string]any{"display_name": "One"}},
				{ExternalID: "org-2", Fields: map[string]any{"display_name": "Two"}},
			},
			"person": {
				{ExternalID: "p-1", Fields: map[string]any{"full_name": "Ada"}},
				{ExternalID: "p-2", Fields: map[string]any{"full_name": "Mor"}},
			},
			"deal": {
				{ExternalID: "d-1", Fields: map[string]any{"name": "First"}},
				{ExternalID: "d-2", Fields: map[string]any{"name": "Second"}},
			},
		},
	}
}

// A crash in the LAST class must resume from the run's checkpoint, not
// from the finished class's cursor: the store's monotonic guard refuses
// a backwards cursor, so getting this wrong wedges every retry.
func TestRunResumesAcrossALaterClassBoundary(t *testing.T) {
	src := threeObjectSource()
	w := newFakeWriters()
	w.failAt = 6 // crash on deal/d-2
	runs := newFakeRuns()
	e := &Engine{runs: runs, w: w}

	if _, err := e.Run(context.Background(), RunID{}, src); err == nil {
		t.Fatal("Run must surface the injected crash")
	}
	if runs.run.Checkpoint != 5 {
		t.Fatalf("checkpoint after crash = %d, want 5 (five rows landed before the failing one)", runs.run.Checkpoint)
	}

	runs.run.Status = StatusRunning
	w.failAt = 0
	if _, err := e.Run(context.Background(), RunID{}, src); err != nil {
		t.Fatalf("resumed Run: %v — a crash past the first class must still resume", err)
	}
	uniq := map[string]bool{}
	for _, k := range w.ensured {
		if uniq[k] {
			t.Errorf("row %s imported twice across the resume", k)
		}
		uniq[k] = true
	}
	if len(uniq) != 6 {
		t.Errorf("rows landed = %d, want 6 (identical to an uninterrupted run)", len(uniq))
	}
}

func TestRunRefusesANonRunningRecord(t *testing.T) {
	runs := newFakeRuns()
	runs.run.Status = StatusComplete
	e := &Engine{runs: runs, w: newFakeWriters()}
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

// A system entry the incumbent owns but never populated is still a
// system entry. Owner metadata travels beside the payload precisely so
// this stays true: were it folded into Fields, every owned blank row
// would read as substantive and the writer would land a nameless
// native record instead of disclosing the skip (AC-mode-flip-7).
func TestAnOwnedRowWithNoPayloadIsStillAnEmptyPayloadSkip(t *testing.T) {
	src := &fakeSource{
		order: []string{"person"},
		objects: map[string][]Row{"person": {
			{ExternalID: "p-blank", OwnerExternalID: "owner-1"},
			{ExternalID: "p-real", OwnerExternalID: "owner-1", Fields: map[string]any{"full_name": "Real"}},
		}},
	}

	dry, err := (&Engine{w: newFakeWriters()}).DryRun(context.Background(), src)
	if err != nil {
		t.Fatalf("DryRun: %v", err)
	}
	if len(dry.Objects) != 1 {
		t.Fatalf("dry-run report = %+v, want one object", dry.Objects)
	}
	if got := dry.Objects[0]; got.WillCreate != 1 || len(got.Skipped) != 1 ||
		got.Skipped[0].ExternalID != "p-blank" || got.Skipped[0].Reason != "empty_payload" {
		t.Errorf("dry-run person = %+v, want p-blank skipped and only p-real counted", got)
	}

	w := newFakeWriters()
	rep, err := (&Engine{runs: newFakeRuns(), w: w}).Run(context.Background(), RunID{}, src)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(w.ensured) != 1 || w.ensured[0] != "person/p-real" {
		t.Errorf("ensured %v, want only person/p-real — a blank row must never reach the writer", w.ensured)
	}
	if rep.Imported != 1 {
		t.Errorf("imported = %d, want 1; a nameless row counted as imported overstates the cutover", rep.Imported)
	}
	if len(rep.Objects) != 1 || len(rep.Objects[0].Skipped) != 1 ||
		rep.Objects[0].Skipped[0].Reason != "empty_payload" {
		t.Errorf("import report = %+v, want the owned blank row disclosed as empty_payload", rep.Objects)
	}
}

// The native create and the identity write are two transactions. A
// process that dies between them leaves a record the identity map has
// never heard of — and since Exists reads that map, the resumed run
// would happily create the SAME record a second time. In a one-way
// cutover that is a duplicate nobody asked for and nothing removes.
func TestAResumeAdoptsRecordsTheCrashLandedButNeverMapped(t *testing.T) {
	src := twoObjectSource()
	w := newFakeWriters()
	w.failAt, w.failAfterCreate = 3, true // person/p-1 lands, then the process dies
	runs := newFakeRuns()
	e := &Engine{runs: runs, w: w}

	if _, err := e.Run(context.Background(), RunID{}, src); err == nil {
		t.Fatal("Run must surface the injected crash")
	}
	if !w.landed["person/p-1"] || w.mapped["person/p-1"] {
		t.Fatalf("the crash should leave p-1 landed but unmapped (landed=%v mapped=%v)",
			w.landed["person/p-1"], w.mapped["person/p-1"])
	}

	runs.run.Status = StatusRunning
	w.failAt, w.failAfterCreate = 0, false
	if _, err := e.Run(context.Background(), RunID{}, src); err != nil {
		t.Fatalf("resumed Run: %v", err)
	}
	if w.reconciles == 0 {
		t.Error("the resume never repaired, so it could only have re-created what the crash landed")
	}
	seen := map[string]int{}
	for _, k := range w.ensured {
		seen[k]++
	}
	if seen["person/p-1"] != 1 {
		t.Errorf("person/p-1 was written %d times; the record the crash landed was created again", seen["person/p-1"])
	}
	if len(seen) != 4 {
		t.Errorf("distinct records = %d (%v), want the same 4 an uninterrupted run lands", len(seen), seen)
	}
}

// The checkpoint advances only AFTER a row lands, so a crash on the
// FIRST row leaves it at zero — and a re-created run (a fresh bundle
// upload, a re-sealed snapshot) also starts at zero with the previous
// attempt's orphans still on disk. Gating the repair on the checkpoint
// therefore skipped it in exactly the cases it exists for.
func TestTheRepairRunsEvenWhenNoCheckpointWasEverRecorded(t *testing.T) {
	src := twoObjectSource()
	w := newFakeWriters()
	w.failAt, w.failAfterCreate = 1, true // the very first row: checkpoint never moves
	runs := newFakeRuns()
	e := &Engine{runs: runs, w: w}

	if _, err := e.Run(context.Background(), RunID{}, src); err == nil {
		t.Fatal("Run must surface the injected crash")
	}
	if runs.run.Checkpoint != 0 {
		t.Fatalf("checkpoint = %d, want 0 — this test is only meaningful at the boundary", runs.run.Checkpoint)
	}

	runs.run.Status = StatusRunning
	w.failAt, w.failAfterCreate = 0, false
	if _, err := e.Run(context.Background(), RunID{}, src); err != nil {
		t.Fatalf("resumed Run: %v", err)
	}
	if w.reconciles == 0 {
		t.Error("a zero checkpoint skipped the repair; the first record of every attempt was left duplicable")
	}
	seen := map[string]int{}
	for _, k := range w.ensured {
		seen[k]++
	}
	for key, n := range seen {
		if n != 1 {
			t.Errorf("%s was written %d times; the record the crash landed was created again", key, n)
		}
	}
}
