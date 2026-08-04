// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/platform/jobs"
)

// TestJobMetricsNameTheWorkspaceThatOwnsTheWorkAndLeaveItEmptyForADispatcher
// — the empty label is the workspace invariant on the wire, and ADR-0080
// admits the id and only the id.
func TestJobMetricsNameTheWorkspaceThatOwnsTheWorkAndLeaveItEmptyForADispatcher(t *testing.T) {
	ws := "6f1a0d64-9d3f-4d63-9c4a-2f0f3b8a1c77"
	var buf bytes.Buffer
	if err := writeJobMetrics(&buf, jobs.Snapshot{Rows: []jobs.StateRow{
		{Queue: "default", Kind: "tenant_pass", WorkspaceID: ws, State: "available", Count: 2},
		{Queue: "default", Kind: "the_dispatcher", Untenanted: true, State: "available", Count: 1},
	}}); err != nil {
		t.Fatalf("writeJobMetrics: %v", err)
	}

	want := []string{
		`margince_job_queue_depth{queue="default",workspace_id="` + ws + `"} 2`,
		`margince_job_queue_depth{queue="default",workspace_id=""} 1`,
	}
	for _, line := range want {
		if !strings.Contains(buf.String(), line) {
			t.Errorf("exposition missing %q\ngot:\n%s", line, buf.String())
		}
	}
}

// TestQueueDepthSumsEveryStateAJobCanBeWaitingIn — OPS-MET-2 is "queued
// jobs per queue", and a job backing off toward a retry is queued work
// nobody has done. Counting only 'available' reports a queue full of
// retrying jobs as empty, which is the failure mode the metric exists to
// catch.
func TestQueueDepthSumsEveryStateAJobCanBeWaitingIn(t *testing.T) {
	var buf bytes.Buffer
	if err := writeJobMetrics(&buf, jobs.Snapshot{Rows: []jobs.StateRow{
		{Queue: "default", Kind: "k", Untenanted: true, State: "available", Count: 1},
		{Queue: "default", Kind: "k", Untenanted: true, State: "scheduled", Count: 2},
		{Queue: "default", Kind: "k", Untenanted: true, State: "retryable", Count: 4},
		{Queue: "default", Kind: "k", Untenanted: true, State: "pending", Count: 16},
		{Queue: "default", Kind: "k", Untenanted: true, State: "running", Count: 8},
	}}); err != nil {
		t.Fatalf("writeJobMetrics: %v", err)
	}
	if !strings.Contains(buf.String(), `margince_job_queue_depth{queue="default",workspace_id=""} 23`) {
		t.Errorf("queue depth did not sum available+scheduled+retryable+pending\ngot:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), `margince_job_running{queue="default",workspace_id=""} 8`) {
		t.Errorf("running is its own gauge, not part of depth\ngot:\n%s", buf.String())
	}
}

// TestDiscardedAndCancelledAreReportedApartAndKeyedByKind — the two are
// different operator stories (every attempt spent vs stopped on purpose)
// and depth is per queue while dead work is per kind, because "which work
// will never run" is a question about the kind, not the lane it sat in.
func TestDiscardedAndCancelledAreReportedApartAndKeyedByKind(t *testing.T) {
	var buf bytes.Buffer
	if err := writeJobMetrics(&buf, jobs.Snapshot{Rows: []jobs.StateRow{
		{Queue: "default", Kind: "retention", Untenanted: true, State: "discarded", Count: 3},
		{Queue: "default", Kind: "retention", Untenanted: true, State: "cancelled", Count: 5},
	}}); err != nil {
		t.Fatalf("writeJobMetrics: %v", err)
	}
	if !strings.Contains(buf.String(), `margince_job_discarded{kind="retention",workspace_id=""} 3`) {
		t.Errorf("discarded is missing or folded in with cancelled\ngot:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), `margince_job_cancelled{kind="retention",workspace_id=""} 5`) {
		t.Errorf("cancelled is missing or folded in with discarded\ngot:\n%s", buf.String())
	}
	if strings.Contains(buf.String(), `margince_job_discarded{kind="retention",workspace_id=""} 8`) {
		t.Error("cancelled was counted as discarded; a job stopped on purpose is not one " +
			"that spent every attempt")
	}
}

// TestAStateTheRendererHasNeverHeardOfIsReportedRatherThanDropped — a
// silently discarded state is the exact posture this phase exists to
// remove. A River release that adds one must make the fleet's unreported
// work visible, not invisible.
func TestAStateTheRendererHasNeverHeardOfIsReportedRatherThanDropped(t *testing.T) {
	var buf bytes.Buffer
	if err := writeJobMetrics(&buf, jobs.Snapshot{Rows: []jobs.StateRow{
		{Queue: "default", Kind: "k", Untenanted: true, State: "hibernating", Count: 7},
	}}); err != nil {
		t.Fatalf("writeJobMetrics: %v", err)
	}
	if !strings.Contains(buf.String(), `margince_job_unrecognised_state{state="hibernating",queue="default",workspace_id=""} 7`) {
		t.Errorf("a state the renderer does not know vanished from the exposition\ngot:\n%s", buf.String())
	}
}

// TestTheOldestQueuedAgeIsTheWorstCaseAcrossTheQueuesKinds — the gauge is
// keyed per queue, and a queue holds many kinds. Reporting the last one
// read rather than the worst would make the number depend on row order.
func TestTheOldestQueuedAgeIsTheWorstCaseAcrossTheQueuesKinds(t *testing.T) {
	var buf bytes.Buffer
	if err := writeJobMetrics(&buf, jobs.Snapshot{Rows: []jobs.StateRow{
		{Queue: "default", Kind: "slow", Untenanted: true, State: "available", Count: 1, OldestRunnableAgeSeconds: ptrTo(900.0)},
		{Queue: "default", Kind: "fast", Untenanted: true, State: "available", Count: 1, OldestRunnableAgeSeconds: ptrTo(5.0)},
	}}); err != nil {
		t.Fatalf("writeJobMetrics: %v", err)
	}
	if !strings.Contains(buf.String(), `margince_job_oldest_queued_age_seconds{queue="default",workspace_id=""} 900`) {
		t.Errorf("the age gauge did not report the worst case for the queue\ngot:\n%s", buf.String())
	}
}

// TestTheSweepPairIsRenderedPerFanOutKind — the pair answers "are tenants
// being missed", so both halves must appear for the same sweep label or an
// alert cannot compare them.
func TestTheSweepPairIsRenderedPerFanOutKind(t *testing.T) {
	var buf bytes.Buffer
	if err := writeJobMetrics(&buf, jobs.Snapshot{Sweeps: []jobs.SweepPass{
		{Kind: "privacy_retention_workspace", Workspaces: 42, Failed: 3},
	}}); err != nil {
		t.Fatalf("writeJobMetrics: %v", err)
	}
	for _, line := range []string{
		`margince_sweep_workspaces_total{sweep="privacy_retention_workspace"} 42`,
		`margince_sweep_workspaces_failed{sweep="privacy_retention_workspace"} 3`,
	} {
		if !strings.Contains(buf.String(), line) {
			t.Errorf("exposition missing %q\ngot:\n%s", line, buf.String())
		}
	}
}

// TestTheSweepUnitPairIsRenderedAtTheDeclaredGrain — the pair exists because
// the per-workspace one cannot tell a workspace with one broken connection
// from a healthy workspace. Both halves carry the same sweep label so an alert
// can compare them, and the unit label says which grain is being read; without
// it the two pairs are indistinguishable in a dashboard.
func TestTheSweepUnitPairIsRenderedAtTheDeclaredGrain(t *testing.T) {
	var buf bytes.Buffer
	if err := writeJobMetrics(&buf, jobs.Snapshot{Units: []jobs.SweepUnit{
		{Kind: "capture_sync", Unit: jobs.FanOutConnection, Units: 9, Failed: 2},
	}}); err != nil {
		t.Fatalf("writeJobMetrics: %v", err)
	}
	for _, line := range []string{
		`margince_sweep_units_total{sweep="capture_sync",unit="connection"} 9`,
		`margince_sweep_units_failed{sweep="capture_sync",unit="connection"} 2`,
	} {
		if !strings.Contains(buf.String(), line) {
			t.Errorf("exposition missing %q\ngot:\n%s", line, buf.String())
		}
	}
}

// TestEverySeriesIsWrittenInAStableOrder — a scrape target's series order
// should not flap between scrapes for no reason, and map iteration order is
// not stable. sortedKeys next door exists for the same reason.
func TestEverySeriesIsWrittenInAStableOrder(t *testing.T) {
	snap := jobs.Snapshot{
		Rows: []jobs.StateRow{
			{Queue: "zeta", Kind: "k", Untenanted: true, State: "available", Count: 1},
			{Queue: "alpha", Kind: "k", Untenanted: true, State: "available", Count: 1},
			{Queue: "mid", Kind: "k", Untenanted: true, State: "available", Count: 1},
		},
		Sweeps: []jobs.SweepPass{
			{Kind: "zeta_sweep", Workspaces: 1},
			{Kind: "alpha_sweep", Workspaces: 1},
		},
	}
	var first bytes.Buffer
	if err := writeJobMetrics(&first, snap); err != nil {
		t.Fatalf("writeJobMetrics: %v", err)
	}
	for range 20 {
		var again bytes.Buffer
		if err := writeJobMetrics(&again, snap); err != nil {
			t.Fatalf("writeJobMetrics: %v", err)
		}
		if again.String() != first.String() {
			t.Fatal("two renders of one snapshot differed: the series order is unstable")
		}
	}
	if strings.Index(first.String(), `queue="alpha"`) > strings.Index(first.String(), `queue="zeta"`) {
		t.Error("queue series are not in sorted order")
	}
	if strings.Index(first.String(), `sweep="alpha_sweep"`) > strings.Index(first.String(), `sweep="zeta_sweep"`) {
		t.Error("sweep series are not in sorted order")
	}
}

// TestAnEmptySnapshotWritesEveryFamilyHeaderAndNoSeries — an idle fleet is
// a real state. Writing nothing at all makes "no jobs" and "the reader is
// broken" the same scrape, and a family whose header never appears cannot
// be graphed before its first series exists.
func TestAnEmptySnapshotWritesEveryFamilyHeaderAndNoSeries(t *testing.T) {
	var buf bytes.Buffer
	if err := writeJobMetrics(&buf, jobs.Snapshot{}); err != nil {
		t.Fatalf("writeJobMetrics: %v", err)
	}
	for _, family := range []string{
		"margince_job_queue_depth",
		"margince_job_running",
		"margince_job_discarded",
		"margince_job_cancelled",
		"margince_job_oldest_queued_age_seconds",
		"margince_sweep_workspaces_total",
		"margince_sweep_workspaces_failed",
		"margince_sweep_units_total",
		"margince_sweep_units_failed",
	} {
		if !strings.Contains(buf.String(), "# TYPE "+family+" gauge") {
			t.Errorf("family header for %s missing from an empty snapshot\ngot:\n%s", family, buf.String())
		}
		if !strings.Contains(buf.String(), "# HELP "+family+" ") {
			t.Errorf("HELP line for %s missing; the text is the contract with whoever reads "+
				"a dashboard six months from now", family)
		}
		if strings.Contains(buf.String(), family+"{") {
			t.Errorf("an empty snapshot invented a series for %s", family)
		}
	}
}

// TestARefusedWriteSurfacesRatherThanBeingRenderedPast — an io.Writer can
// fail, and a scrape that swallowed the failure would serve a truncated
// exposition that parses as a smaller fleet.
func TestARefusedWriteSurfacesRatherThanBeingRenderedPast(t *testing.T) {
	refused := errors.New("connection reset")
	snap := jobs.Snapshot{
		Rows:   []jobs.StateRow{{Queue: "default", Kind: "k", Untenanted: true, State: "available", Count: 1}},
		Sweeps: []jobs.SweepPass{{Kind: "s", Workspaces: 1}},
		Units:  []jobs.SweepUnit{{Kind: "u", Unit: jobs.FanOutConnection, Units: 1}},
	}

	// Fail at each write in turn, so no single family's writes are the only
	// ones checked. The count is deliberately larger than the exposition is
	// long — the declared catalogue alone is one write per declared kind —
	// so once the writer stops failing the render succeeds and the loop has
	// covered every position.
	var everSucceeded bool
	for n := range 256 {
		w := &failingWriter{failOn: n, err: refused}
		err := writeJobMetrics(w, snap)
		if err == nil {
			everSucceeded = true
			continue
		}
		if !errors.Is(err, refused) {
			t.Fatalf("write %d: the renderer lost the cause: %v", n, err)
		}
	}
	if !everSucceeded {
		t.Fatal("the failing-writer sweep never reached a successful render; the test is " +
			"not actually exercising the boundary it claims to")
	}
}

// failingWriter refuses the failOn'th write and accepts every other.
type failingWriter struct {
	writes int
	failOn int
	err    error
}

func (w *failingWriter) Write(p []byte) (int, error) {
	defer func() { w.writes++ }()
	if w.writes == w.failOn {
		return 0, w.err
	}
	return len(p), nil
}

// TestAQueueHoldingOnlyDeadWorkReportsNoAgeAtAll — the gauge answers "how
// long has the oldest RUNNABLE job waited", and a queue whose rows are all
// discarded has no such job. A zero there would read as a healthy queue on
// the one gauge meant to notice work is stuck.
func TestAQueueHoldingOnlyDeadWorkReportsNoAgeAtAll(t *testing.T) {
	var buf bytes.Buffer
	if err := writeJobMetrics(&buf, jobs.Snapshot{Rows: []jobs.StateRow{
		{Queue: "graveyard", Kind: "k", Untenanted: true, State: "discarded", Count: 50},
		{Queue: "graveyard", Kind: "k", Untenanted: true, State: "cancelled", Count: 2},
	}}); err != nil {
		t.Fatalf("writeJobMetrics: %v", err)
	}
	if strings.Contains(buf.String(), `margince_job_oldest_queued_age_seconds{queue="graveyard"`) {
		t.Errorf("a queue with nothing runnable reported an age\ngot:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), `margince_job_discarded{kind="k",workspace_id=""} 50`) {
		t.Errorf("the dead work itself went missing\ngot:\n%s", buf.String())
	}
}

// TestALabelValueCannotBreakTheWholeExposition — workspace_id is
// args->>'workspace_id' verbatim from a table with no constraint on it and
// direct app-role CRUD. Go's %q would encode a tab as \t, which the
// Prometheus text format does not define, and a parser meeting an undefined
// escape rejects the ENTIRE scrape — every metric, not the one series.
func TestALabelValueCannotBreakTheWholeExposition(t *testing.T) {
	var buf bytes.Buffer
	if err := writeJobMetrics(&buf, jobs.Snapshot{Rows: []jobs.StateRow{
		{Queue: "default", Kind: "k", WorkspaceID: "a\tb\x00c", State: "available", Count: 1},
		{Queue: `qu"ote`, Kind: "k", WorkspaceID: "x\ny", State: "available", Count: 1},
		{Queue: "default", Kind: `back\slash`, WorkspaceID: "", State: "discarded", Count: 1},
	}}); err != nil {
		t.Fatalf("writeJobMetrics: %v", err)
	}
	got := buf.String()

	for _, forbidden := range []string{`\t`, `\x00`, `\u`} {
		if strings.Contains(got, forbidden) {
			t.Errorf("the exposition carries %s, an escape the text format does not define"+
				"\ngot:\n%s", forbidden, got)
		}
	}
	if !strings.Contains(got, `workspace_id="a<0x09>b<0x00>c"`) {
		t.Errorf("control characters were not rewritten to a printable form\ngot:\n%s", got)
	}
	for _, want := range []string{`queue="qu\"ote"`, `kind="back\\slash"`, `workspace_id="x\ny"`} {
		if !strings.Contains(got, want) {
			t.Errorf("exposition missing %q — the three defined escapes must still be emitted"+
				"\ngot:\n%s", want, got)
		}
	}
}

// TestTheJobSectionWritesNothingWhenTheReadFails — a scrape that emitted
// zeroes it did not measure is worse than a gap: a gap is visible on a
// graph, and a fabricated zero reads as a healthy empty queue. This is the
// posture Metrics already takes for the outbox backlog, and it is the seam
// between the pool and the renderer that nothing else covers.
func TestTheJobSectionWritesNothingWhenTheReadFails(t *testing.T) {
	unreadable := func(context.Context) (jobs.Snapshot, error) {
		return jobs.Snapshot{}, errors.New("the job table could not be read in time")
	}
	var buf bytes.Buffer

	if err := jobMetricsSection(unreadable)(context.Background(), &buf); err != nil {
		t.Fatalf("an unreadable section must not fail the whole scrape: %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("an unmeasured section wrote %d bytes; a fabricated zero is "+
			"indistinguishable from a healthy empty queue\ngot:\n%s", buf.Len(), buf.String())
	}
}

// TestAQueueHoldingOnlyRunningWorkReportsNoAgeEither — the gauge measures
// the oldest runnable-and-UNCLAIMED job. A running job has been claimed, so
// a queue holding only running rows has no subject for this gauge, and a
// zero there would read as "nothing is late" rather than "nothing is
// waiting". The endpoint reports null for exactly these rows; the two
// surfaces must not disagree about the same table.
func TestAQueueHoldingOnlyRunningWorkReportsNoAgeEither(t *testing.T) {
	var buf bytes.Buffer
	if err := writeJobMetrics(&buf, jobs.Snapshot{Rows: []jobs.StateRow{
		{Queue: "busy", Kind: "k", Untenanted: true, State: "running", Count: 4},
	}}); err != nil {
		t.Fatalf("writeJobMetrics: %v", err)
	}
	if strings.Contains(buf.String(), `margince_job_oldest_queued_age_seconds{queue="busy"`) {
		t.Errorf("a queue whose work is all claimed reported a waiting age\ngot:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), `margince_job_running{queue="busy",workspace_id=""} 4`) {
		t.Errorf("the running work itself went missing\ngot:\n%s", buf.String())
	}
}

func ptrTo[T any](v T) *T { return &v }

// TestAQueueOfFutureScheduledWorkReportsNoAgeSeries — the group holds
// waiting work, but none of it is RUNNABLE yet, so the read answers null
// and the gauge must stay silent. A zero would claim the oldest runnable
// job has waited no time, about a job that does not exist — and the
// endpoint reports null for exactly this row, so a zero here would also put
// the two surfaces at odds.
func TestAQueueOfFutureScheduledWorkReportsNoAgeSeries(t *testing.T) {
	var buf bytes.Buffer
	if err := writeJobMetrics(&buf, jobs.Snapshot{Rows: []jobs.StateRow{
		{
			Queue: "nightly", Kind: "k", Untenanted: true, State: "scheduled", Count: 4,
			OldestRunnableAgeSeconds: nil,
		},
	}}); err != nil {
		t.Fatalf("writeJobMetrics: %v", err)
	}
	if strings.Contains(buf.String(), `margince_job_oldest_queued_age_seconds{queue="nightly"`) {
		t.Errorf("a queue whose work is all future-scheduled reported an age\ngot:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), `margince_job_queue_depth{queue="nightly",workspace_id=""} 4`) {
		t.Errorf("the queued work itself went missing\ngot:\n%s", buf.String())
	}
}

// TestAPresentButEmptyWorkspaceIsNotCountedAsADispatcher — the empty label
// means "dispatcher", exactly and in both directions, and that invariant is
// what every reader of these gauges stands on. river_job has no constraint
// forcing the key to be absent rather than empty, so a malformed row must
// be visible AS malformed instead of silently joining the dispatcher series.
func TestAPresentButEmptyWorkspaceIsNotCountedAsADispatcher(t *testing.T) {
	var buf bytes.Buffer
	if err := writeJobMetrics(&buf, jobs.Snapshot{Rows: []jobs.StateRow{
		{Queue: "default", Kind: "k", Untenanted: true, State: "available", Count: 1},
		// Present but empty: NOT untenanted, and not a real workspace either.
		{Queue: "default", Kind: "k", WorkspaceID: "", Untenanted: false, State: "available", Count: 9},
	}}); err != nil {
		t.Fatalf("writeJobMetrics: %v", err)
	}
	if !strings.Contains(buf.String(), `margince_job_queue_depth{queue="default",workspace_id=""} 1`) {
		t.Errorf("the real dispatcher row was disturbed\ngot:\n%s", buf.String())
	}
	if strings.Contains(buf.String(), `margince_job_queue_depth{queue="default",workspace_id=""} 10`) {
		t.Error("a malformed row was folded into the dispatcher series, which is the one " +
			"invariant these gauges promise")
	}
	if !strings.Contains(buf.String(), `margince_job_queue_depth{queue="default",workspace_id="malformed_workspace_id"} 9`) {
		t.Errorf("the malformed row is invisible rather than flagged\ngot:\n%s", buf.String())
	}
}

// TestTwoLabelsDifferingOnlyByAControlCharacterStayDistinct — dropping a
// control character is lossy, and lossy collapses two distinct malformed
// ids into one label set. Duplicate series make a scrape unparseable, which
// is the exact failure the escaping exists to prevent.
func TestTwoLabelsDifferingOnlyByAControlCharacterStayDistinct(t *testing.T) {
	var buf bytes.Buffer
	if err := writeJobMetrics(&buf, jobs.Snapshot{Rows: []jobs.StateRow{
		{Queue: "q", Kind: "k", WorkspaceID: "ab", State: "available", Count: 1},
		{Queue: "q", Kind: "k", WorkspaceID: "a\tb", State: "available", Count: 2},
	}}); err != nil {
		t.Fatalf("writeJobMetrics: %v", err)
	}
	got := buf.String()
	if strings.Contains(got, `\t`) {
		t.Errorf("an escape the text format does not define reached the wire\ngot:\n%s", got)
	}
	if !strings.Contains(got, `workspace_id="ab"} 1`) {
		t.Errorf("the clean label was rewritten\ngot:\n%s", got)
	}
	if !strings.Contains(got, `workspace_id="a<0x09>b"} 2`) {
		t.Errorf("the control character was dropped, collapsing two distinct ids into one "+
			"series\ngot:\n%s", got)
	}
}

// TestALiteralEscapeSequenceDoesNotCollideWithTheCharacterItEncodes is the
// direct test for the '<' branch, and the collision that branch exists to
// close. Escape only control bytes and the two values below both render as
// `<0x09>`: one row whose id contains the six literal characters, and one
// whose id contains a real tab. Two distinct ids, one label set, and
// Prometheus rejects the whole scrape — the third time this same failure
// appeared in this change, so it gets an assertion of its own rather than
// being implied by the control-character test next door.
func TestALiteralEscapeSequenceDoesNotCollideWithTheCharacterItEncodes(t *testing.T) {
	var buf bytes.Buffer
	if err := writeJobMetrics(&buf, jobs.Snapshot{Rows: []jobs.StateRow{
		{Queue: "q", Kind: "k", WorkspaceID: "<0x09>", State: "available", Count: 1},
		{Queue: "q", Kind: "k", WorkspaceID: "\t", State: "available", Count: 2},
	}}); err != nil {
		t.Fatalf("writeJobMetrics: %v", err)
	}
	got := buf.String()

	// The literal '<' is itself escaped, so the six-character id cannot
	// render as the encoding of a tab.
	if !strings.Contains(got, `workspace_id="<0x3c>0x09>"} 1`) {
		t.Errorf("the literal escape sequence was not itself escaped\ngot:\n%s", got)
	}
	if !strings.Contains(got, `workspace_id="<0x09>"} 2`) {
		t.Errorf("the real tab did not render as its escape\ngot:\n%s", got)
	}
	// The load-bearing assertion: two distinct ids, two distinct series.
	if strings.Count(got, `margince_job_queue_depth{queue="q"`) != 2 {
		t.Errorf("two distinct workspace ids collapsed into one series; a duplicate label "+
			"set makes Prometheus reject the entire scrape\ngot:\n%s", got)
	}
}
