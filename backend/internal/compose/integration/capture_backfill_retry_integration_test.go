// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// A backfill spans hours of a real mailbox, and a provider that rate-limits or
// blinks mid-run is weather, not a fault: the run keeps its committed cursor,
// counts the consecutive failure, and asks the caller to come back later. Only
// the classes a retry cannot fix (a rejected credential, our own bug) end it.

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// flakyConnector serves pagedConnector's pages, but consumes one entry of
// faults per BackfillPage call first: a non-nil entry is the provider refusing
// that page, a nil entry (or an exhausted list) serves the page normally.
type flakyConnector struct {
	*pagedConnector
	faults []error
}

func (f *flakyConnector) BackfillPage(ctx context.Context, auth connector.Auth, after time.Time, pageToken string, sink connector.Sink) (connector.BackfillPageResult, error) {
	if len(f.faults) > 0 {
		fault := f.faults[0]
		f.faults = f.faults[1:]
		if fault != nil {
			return connector.BackfillPageResult{}, fault
		}
	}
	return f.pagedConnector.BackfillPage(ctx, auth, after, pageToken, sink)
}

// readBackfillRetryState reads the scheduling half of a run: what the pager
// must not lose across a transient fault.
func readBackfillRetryState(t *testing.T, e *searchEnv, id ids.UUID) (status string, failures int, errClass *string, cursor []byte) {
	t.Helper()
	err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(e.Admin(), `
			SELECT status, consecutive_failures, last_error_class, cursor
			FROM capture_backfill WHERE id = $1`, id).
			Scan(&status, &failures, &errClass, &cursor)
	})
	if err != nil {
		t.Fatal(err)
	}
	return status, failures, errClass, cursor
}

// startFlakyBackfill connects gmail for Rep1 over a connector with the given
// page faults and opens a run against it.
func startFlakyBackfill(t *testing.T, e *searchEnv, faults []error) (*capture.Registry, ids.UUID) {
	t.Helper()
	registry := newTestCaptureRegistry(e, newTestKeyvault(t, e))
	registry.Register(&flakyConnector{pagedConnector: &pagedConnector{messages: 25, pageSize: 10}, faults: faults})
	grantCtx := e.humanWithScopes(e.Rep1, []principal.Scope{principal.ScopeRead})
	if _, err := registry.Connect(grantCtx, "gmail", connector.Auth("refresh")); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	run, err := registry.StartBackfill(grantCtx, "gmail", ids.From[ids.UserKind](e.Rep1), 6, 25, enqueueNothing)
	if err != nil {
		t.Fatalf("StartBackfill: %v", err)
	}
	return registry, run.ID
}

func TestBackfillSurvivesATransientProviderFault(t *testing.T) {
	e := setupSearch(t)
	registry, runID := startFlakyBackfill(t, e, []error{&connector.RateLimitedError{}})
	wsCtx := principal.WithWorkspaceID(context.Background(), e.WS)

	done, completed, retryAfter, err := registry.RunBackfillStep(wsCtx, runID)
	if err == nil {
		t.Fatal("a rate-limited page must surface its fault to the caller's log, not vanish")
	}
	if done || completed {
		t.Fatalf("a rate limit does not end a run (done=%v completed=%v)", done, completed)
	}
	if retryAfter <= 0 {
		t.Fatalf("retryAfter = %v, want a positive delay — the caller has to know when to come back", retryAfter)
	}
	status, failures, errClass, cursor := readBackfillRetryState(t, e, runID)
	if status != "running" {
		t.Fatalf("status = %s, want running — the run is alive, waiting out the limit", status)
	}
	if failures != 1 {
		t.Fatalf("consecutive_failures = %d, want 1", failures)
	}
	if errClass == nil || *errClass != "rate_limited" {
		t.Fatalf("last_error_class = %v, want rate_limited", errClass)
	}
	if len(cursor) != 0 {
		t.Fatal("a failed page must not advance the committed cursor")
	}

	// The retry: the same run, from the same cursor, against a provider that has
	// stopped refusing. A committed page clears the ladder.
	done, completed, retryAfter, err = registry.RunBackfillStep(wsCtx, runID)
	if err != nil || done || completed || retryAfter != 0 {
		t.Fatalf("retry step: done=%v completed=%v retryAfter=%v err=%v", done, completed, retryAfter, err)
	}
	status, failures, _, cursor = readBackfillRetryState(t, e, runID)
	if status != "running" || failures != 0 || len(cursor) == 0 {
		t.Fatalf("after the recovered page: status=%s consecutive_failures=%d cursor=%q, want running/0/committed", status, failures, cursor)
	}
	if _, scanned, captured, _ := readBackfillRow(t, e, runID); scanned != 10 || captured != 9 {
		t.Fatalf("the recovered page must count once: scanned=%d captured=%d, want 10/9", scanned, captured)
	}
}

// dyingJobConnector is the commonest page failure of all: the job context
// expires while the page is out at the provider, and the provider client reports
// that deadline in its OWN vocabulary — a wrapped connector.ErrUnreachable
// (gmail and graph both count a timeout as unreachable), never a bare
// context error.
type dyingJobConnector struct {
	*pagedConnector
	// killJob stands in for River's page timeout firing mid-fetch.
	killJob func()
}

func (c *dyingJobConnector) BackfillPage(context.Context, connector.Auth, time.Time, string, connector.Sink) (connector.BackfillPageResult, error) {
	c.killJob()
	return connector.BackfillPageResult{}, fmt.Errorf("gmail: could not reach the provider: %w", connector.ErrUnreachable)
}

// A page whose job context died must not leave the run both live and unowned:
// the run row is the only thing that can bring the import back, and every write
// that decides its fate has to outlive the context that killed the page.
func TestABackfillPageWhoseJobContextDiedNeverWedgesTheRun(t *testing.T) {
	e := setupSearch(t)
	registry := newTestCaptureRegistry(e, newTestKeyvault(t, e))
	fake := &dyingJobConnector{pagedConnector: &pagedConnector{messages: 25, pageSize: 10}}
	registry.Register(fake)
	rep := ids.From[ids.UserKind](e.Rep1)
	grantCtx := e.humanWithScopes(e.Rep1, []principal.Scope{principal.ScopeRead})
	if _, err := registry.Connect(grantCtx, "gmail", connector.Auth("refresh")); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	run, err := registry.StartBackfill(grantCtx, "gmail", rep, 6, 25, enqueueNothing)
	if err != nil {
		t.Fatalf("StartBackfill: %v", err)
	}

	// Each step runs on a fresh job context the page then kills, exactly as a
	// River delivery whose timeout fires mid-page does.
	step := func() (bool, time.Duration, error) {
		jobCtx, killJob := context.WithCancel(principal.WithWorkspaceID(context.Background(), e.WS))
		defer killJob()
		fake.killJob = killJob
		done, _, retryAfter, err := registry.RunBackfillStep(jobCtx, run.ID)
		return done, retryAfter, err
	}

	done, retryAfter, err := step()
	if err == nil {
		t.Fatal("a page that died with its job context must surface its fault to the caller's log")
	}
	if done {
		t.Fatal("done=true over a run still waiting out a transient fault: the import would never be paged again")
	}
	if retryAfter <= 0 {
		t.Fatalf("retryAfter = %v, want a positive delay: the run is still live, and only a redelivery brings it back", retryAfter)
	}
	status, failures, errClass, _ := readBackfillRetryState(t, e, run.ID)
	if status != "running" || failures != 1 {
		t.Fatalf("status=%s consecutive_failures=%d, want running/1 — the ladder write has to outlive the job context", status, failures)
	}
	if errClass == nil || *errClass != "unreachable" {
		t.Fatalf("last_error_class = %v, want unreachable", errClass)
	}

	// The ladder still ends the run: a provider that refuses every page is a
	// fault no delay repairs, and that terminal write is detached too.
	const ladderBound = 32 // the give-up cap is the engine's; this only stops a runaway loop
	for i := 0; i < ladderBound && retryAfter > 0; i++ {
		done, retryAfter, err = step()
		if err == nil {
			t.Fatal("a page this connector always refuses cannot report success")
		}
	}
	if retryAfter > 0 {
		t.Fatalf("the run was still asking for a retry after %d dead pages — the ladder has no end", ladderBound)
	}
	if !done {
		t.Fatal("done=false on the step that spent the ladder: the run is over, and done is what says so")
	}
	if status, _, _, _ = readBackfillRetryState(t, e, run.ID); status != "error" {
		t.Fatalf("status = %s, want error — a run the pager gave up on is over, not left running", status)
	}
	// The assertion that matters: uq_capture_backfill_live is not holding the
	// connection hostage, so the human can import again without an operator.
	if _, err := registry.StartBackfill(grantCtx, "gmail", rep, 6, 25, enqueueNothing); err != nil {
		t.Fatalf("a fresh import is blocked by the run that failed: %v", err)
	}
}

func TestBackfillEndsOnAFaultNoRetryCanFix(t *testing.T) {
	e := setupSearch(t)
	registry, runID := startFlakyBackfill(t, e, []error{connector.ErrAuthRejected})
	wsCtx := principal.WithWorkspaceID(context.Background(), e.WS)

	done, completed, retryAfter, err := registry.RunBackfillStep(wsCtx, runID)
	if err == nil || completed {
		t.Fatalf("a rejected credential is a failure, got completed=%v err=%v", completed, err)
	}
	if !done {
		t.Fatal("done=false over a run this step already ended: a caller reading done alone would page a finished run")
	}
	if retryAfter != 0 {
		t.Fatalf("retryAfter = %v, want 0 — no delay makes a revoked grant work", retryAfter)
	}
	status, failures, errClass, _ := readBackfillRetryState(t, e, runID)
	if status != "error" {
		t.Fatalf("status = %s, want error — the run needs its human, not a retry", status)
	}
	if failures != 0 {
		t.Fatalf("consecutive_failures = %d, want 0 — the ladder is for transients only", failures)
	}
	if errClass == nil || *errClass != "auth" {
		t.Fatalf("last_error_class = %v, want auth", errClass)
	}
}
