// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// A backfill page is a hundred messages and minutes of provider I/O, so the
// run reports what it has walked WHILE it walks — the activation view moves
// per message instead of once per committed page. The transient tally is
// advisory: the committed counters still move only at commit, and every write
// that ends a page clears it, so a page walked twice is counted once.

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// reportingConnector walks a page one message at a time, reporting the running
// tally exactly as the Gmail connector does, and hands control to the test
// after each report so the status read can be observed MID-page. failAfter > 0
// abandons the page once that many messages have been reported — the transient
// fault whose partial tally must not survive.
type reportingConnector struct {
	*pagedConnector
	perMessage func(scanned, captured int)
	failAfter  int
}

func (c *reportingConnector) BackfillPage(ctx context.Context, _ connector.Auth, _ time.Time, pageToken string, _ connector.Sink) (connector.BackfillPageResult, error) {
	c.pageCalls++
	progress := connector.BackfillProgressFrom(ctx)
	res := connector.BackfillPageResult{Scanned: c.pageSize, Captured: c.pageSize}
	if pageToken == "" && c.pageSize < c.messages {
		res.NextToken = "off:page2"
	}
	for walked := 1; walked <= c.pageSize; walked++ {
		progress.Observed(ctx, walked, walked, 0)
		if c.perMessage != nil {
			c.perMessage(walked, walked)
		}
		if c.failAfter > 0 && walked == c.failAfter {
			return connector.BackfillPageResult{}, &connector.RateLimitedError{}
		}
	}
	return res, nil
}

// readInflight reads the transient columns directly — the test's proof that
// they are cleared, which the summed status read alone cannot show.
func readInflight(t *testing.T, e *SearchEnv, id ids.UUID) (scanned, captured int) {
	t.Helper()
	err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(e.Admin(), `
			SELECT inflight_scanned, inflight_captured FROM capture_backfill WHERE id = $1`, id).
			Scan(&scanned, &captured)
	})
	if err != nil {
		t.Fatal(err)
	}
	return scanned, captured
}

// startReportingBackfill connects gmail for Rep1 over the given connector and
// opens a run against it. pacing is the live-tally write pacing: 0 writes every
// report, which is what a test walking a page in microseconds needs to observe
// the tally at all.
func startReportingBackfill(t *testing.T, e *SearchEnv, prov *reportingConnector, pacing time.Duration) (*capture.Registry, context.Context, ids.UUID) {
	t.Helper()
	registry := newTestCaptureRegistry(e, newTestKeyvault(t, e)).WithProgressPacing(pacing)
	registry.Register(prov)
	grantCtx := e.humanWithScopes(e.Rep1, []principal.Scope{principal.ScopeRead})
	if _, err := registry.Connect(grantCtx, "gmail", connector.Auth("refresh")); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	run, err := registry.StartBackfill(grantCtx, "gmail", ids.From[ids.UserKind](e.Rep1), 6, 20, enqueueNothing)
	if err != nil {
		t.Fatalf("StartBackfill: %v", err)
	}
	return registry, grantCtx, run.ID
}

func TestBackfillProgressIsVisibleWhileThePageRuns(t *testing.T) {
	e := SetupSearch(t)
	prov := &reportingConnector{pagedConnector: &pagedConnector{messages: 20, pageSize: 10}}
	registry, grantCtx, runID := startReportingBackfill(t, e, prov, 0)
	rep := ids.From[ids.UserKind](e.Rep1)

	// Every mid-page status read the walk produced, so the assertions below can
	// speak about what a watching browser would actually have seen.
	var seen []capture.BackfillRun
	prov.perMessage = func(int, int) {
		run, err := registry.BackfillStatus(grantCtx, "gmail", rep)
		switch {
		case err != nil:
			t.Fatalf("mid-page status read: %v", err)
		case run == nil:
			t.Fatal("a started run must be readable mid-page")
		}
		seen = append(seen, *run)
	}

	wsCtx := principal.WithWorkspaceID(context.Background(), e.WS)
	if _, _, _, err := registry.RunBackfillStep(wsCtx, runID); err != nil {
		t.Fatalf("page 1: %v", err)
	}

	if len(seen) != 10 {
		t.Fatalf("observed %d mid-page reads, want one per message", len(seen))
	}
	// The first message already moves the view off zero: that is the whole
	// point — the old behaviour showed nothing until the page committed.
	if seen[0].Scanned != 1 || seen[0].Captured != 1 {
		t.Fatalf("after message 1 the status read = scanned %d / captured %d, want 1/1", seen[0].Scanned, seen[0].Captured)
	}
	// And it reads as running, not queued: a run whose numbers climb must not
	// be labelled as one that has not begun.
	if seen[0].Status != "running" {
		t.Fatalf("mid-page status = %q, want running", seen[0].Status)
	}
	for i, run := range seen {
		if run.Scanned != i+1 || run.Captured != i+1 {
			t.Fatalf("read %d = scanned %d / captured %d, want %d/%d — the tally must climb by one per message", i, run.Scanned, run.Captured, i+1, i+1)
		}
	}

	// The commit folds the page in and clears the transient copy, so the same
	// ten messages are reported once, not twice.
	if scanned, captured := readInflight(t, e, runID); scanned != 0 || captured != 0 {
		t.Fatalf("after the commit inflight = %d/%d, want it cleared", scanned, captured)
	}
	after, err := registry.BackfillStatus(grantCtx, "gmail", rep)
	if err != nil {
		t.Fatalf("status after page 1: %v", err)
	}
	if after.Scanned != 10 || after.Captured != 10 {
		t.Fatalf("after page 1 = scanned %d / captured %d, want exactly the page's 10/10", after.Scanned, after.Captured)
	}
}

func TestBackfillProgressIsPacedRatherThanWrittenPerMessage(t *testing.T) {
	// A real import is thousands of messages. Written unpaced, that is one row
	// update per message purely so a number can move faster than anyone reads
	// it. Under a pacing longer than the page takes, only the first report is
	// written — and the page's commit still reports every message.
	e := SetupSearch(t)
	prov := &reportingConnector{pagedConnector: &pagedConnector{messages: 20, pageSize: 10}}
	registry, grantCtx, runID := startReportingBackfill(t, e, prov, time.Hour)
	rep := ids.From[ids.UserKind](e.Rep1)

	var highWater int
	prov.perMessage = func(int, int) {
		if s, _ := readInflight(t, e, runID); s > highWater {
			highWater = s
		}
	}

	wsCtx := principal.WithWorkspaceID(context.Background(), e.WS)
	if _, _, _, err := registry.RunBackfillStep(wsCtx, runID); err != nil {
		t.Fatalf("page 1: %v", err)
	}
	if highWater != 1 {
		t.Fatalf("the paced page wrote its tally up to %d, want only the first report — the rest were inside the pacing window", highWater)
	}
	after, err := registry.BackfillStatus(grantCtx, "gmail", rep)
	if err != nil {
		t.Fatalf("status after page 1: %v", err)
	}
	if after.Scanned != 10 || after.Captured != 10 {
		t.Fatalf("after page 1 = scanned %d / captured %d, want the commit's full 10/10 — pacing drops frames, never work", after.Scanned, after.Captured)
	}
}

func TestBackfillProgressDiesWithTheFailedPage(t *testing.T) {
	// A page that fails transiently is retried from the committed cursor, so
	// its partial tally must not survive — kept, it would be added to the same
	// messages when the retry counts them for real.
	e := SetupSearch(t)
	prov := &reportingConnector{pagedConnector: &pagedConnector{messages: 20, pageSize: 10}, failAfter: 4}
	registry, grantCtx, runID := startReportingBackfill(t, e, prov, 0)
	wsCtx := principal.WithWorkspaceID(context.Background(), e.WS)

	done, _, retryAfter, err := registry.RunBackfillStep(wsCtx, runID)
	if err == nil {
		t.Fatal("a rate-limited page must surface its fault")
	}
	if done || retryAfter <= 0 {
		t.Fatalf("a rate limit leaves the run live and retryable (done=%v retryAfter=%v)", done, retryAfter)
	}
	if scanned, captured := readInflight(t, e, runID); scanned != 0 || captured != 0 {
		t.Fatalf("after the failed page inflight = %d/%d, want it cleared so the retry cannot double-count", scanned, captured)
	}
	run, err := registry.BackfillStatus(grantCtx, "gmail", ids.From[ids.UserKind](e.Rep1))
	if err != nil {
		t.Fatalf("status after the fault: %v", err)
	}
	if run.Scanned != 0 || run.Captured != 0 {
		t.Fatalf("after the failed page the status read = scanned %d / captured %d, want 0/0 — nothing committed", run.Scanned, run.Captured)
	}
}

func TestBackfillProgressIsFencedByTheConnectionGeneration(t *testing.T) {
	// The connection goes away under the running page. Its commit is already
	// fenced off, and the live tally must be fenced the same way — otherwise
	// the screen keeps counting up mail from an account this run no longer has,
	// right until the commit cancels it.
	e := SetupSearch(t)
	seedCaptureRole(t, e)
	prov := &reportingConnector{pagedConnector: &pagedConnector{messages: 20, pageSize: 10}}
	registry, grantCtx, runID := startReportingBackfill(t, e, prov, 0)

	// What the row held at the moment the connection went away. The fence stops
	// LATER writes; it does not reach back and undo the ones already committed,
	// which is the commit's job.
	var frozen int
	prov.perMessage = func(scanned, _ int) {
		switch {
		case scanned == 3:
			if err := registry.Disconnect(grantCtx, "gmail"); err != nil {
				t.Errorf("mid-page disconnect: %v", err)
				return
			}
			frozen, _ = readInflight(t, e, runID)
		case scanned > 3:
			if s, _ := readInflight(t, e, runID); s != frozen {
				t.Errorf("message %d moved inflight to %d — a superseded page must stop reporting, it was frozen at %d", scanned, s, frozen)
			}
		}
	}

	wsCtx := principal.WithWorkspaceID(context.Background(), e.WS)
	if _, _, _, err := registry.RunBackfillStep(wsCtx, runID); err != nil {
		t.Fatalf("the superseded page's step: %v", err)
	}
	status, scanned, captured, _ := readBackfillRow(t, e, runID)
	if status != "cancelled" || scanned != 0 || captured != 0 {
		t.Fatalf("superseded run = %s scanned %d captured %d, want cancelled with nothing credited", status, scanned, captured)
	}
	if s, c := readInflight(t, e, runID); s != 0 || c != 0 {
		t.Fatalf("the superseded page left inflight = %d/%d behind", s, c)
	}
}

func TestCancelClearsTheLiveTallyAndTheRunningPageCannotWriteItBack(t *testing.T) {
	// The user stops the import while a page is still walking. Cancel is
	// terminal and its counts are what they keep, so the page's live tally must
	// go with it — and the messages the page keeps walking afterwards must not
	// write it back.
	e := SetupSearch(t)
	prov := &reportingConnector{pagedConnector: &pagedConnector{messages: 20, pageSize: 10}}
	registry, grantCtx, runID := startReportingBackfill(t, e, prov, 0)
	rep := ids.From[ids.UserKind](e.Rep1)

	prov.perMessage = func(scanned, _ int) {
		if scanned != 5 {
			return
		}
		if _, err := registry.CancelBackfill(grantCtx, "gmail", rep); err != nil {
			t.Errorf("CancelBackfill mid-page: %v", err)
			return
		}
		if s, c := readInflight(t, e, runID); s != 0 || c != 0 {
			t.Errorf("cancel left inflight = %d/%d, want it cleared with the run", s, c)
		}
	}

	wsCtx := principal.WithWorkspaceID(context.Background(), e.WS)
	done, completed, _, err := registry.RunBackfillStep(wsCtx, runID)
	if err != nil {
		t.Fatalf("the cancelled page's step: %v", err)
	}
	if !done || completed {
		t.Fatalf("a page whose run was cancelled under it ends the step (done=%v completed=%v)", done, completed)
	}
	if scanned, captured := readInflight(t, e, runID); scanned != 0 || captured != 0 {
		t.Fatalf("the rest of the page wrote inflight = %d/%d onto a cancelled run", scanned, captured)
	}
	run, err := registry.BackfillStatus(grantCtx, "gmail", rep)
	if err != nil {
		t.Fatalf("status after cancel: %v", err)
	}
	if run.Status != "cancelled" || run.Scanned != 0 || run.Captured != 0 {
		t.Fatalf("cancelled run = %s scanned %d captured %d, want cancelled with only its committed zeros", run.Status, run.Scanned, run.Captured)
	}
}
