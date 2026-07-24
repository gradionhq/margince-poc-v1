// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The ADR-0063 bounded backfill over a real migrated Postgres: preview
// estimates before anything spends, start is widen-only with one live run
// per connection, each page commits its cursor + counters (a killed worker
// resumes from the committed token, never re-paging), completion lands
// status 'done', and cancel is honest — captured counts survive it.

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// pagedConnector is a Backfiller whose provider holds a fixed message count,
// served pageSize at a time. It counts pages so resumability is observable.
type pagedConnector struct {
	messages  int
	pageSize  int
	pageCalls int
}

func (p *pagedConnector) Descriptor() connector.Descriptor {
	return connector.Descriptor{
		Name: "gmail", Version: "1",
		Scopes:   []principal.Scope{principal.ScopeRead},
		RiskTier: mcp.TierGreen,
		Produces: []datasource.EntityType{datasource.EntityActivity},
	}
}

func (p *pagedConnector) Authenticate(context.Context, connector.AuthRequest) (connector.Auth, error) {
	return connector.Auth("token"), nil
}

func (p *pagedConnector) Sync(_ context.Context, _ connector.Auth, cursor connector.Cursor, _ connector.Sink) (connector.Cursor, error) {
	return cursor, nil
}

func (p *pagedConnector) Normalize(context.Context, connector.RawRecord) ([]connector.NormalizedRecord, error) {
	return nil, connector.ErrSkip
}

func (p *pagedConnector) HealthCheck(context.Context, connector.Auth) error { return nil }

func (p *pagedConnector) EstimateBackfill(context.Context, connector.Auth, time.Time) (int, error) {
	return p.messages, nil
}

func (p *pagedConnector) BackfillPage(_ context.Context, _ connector.Auth, _ time.Time, pageToken string, _ connector.Sink) (connector.BackfillPageResult, error) {
	p.pageCalls++
	offset := 0
	if pageToken != "" {
		if _, err := fmt.Sscanf(pageToken, "off:%d", &offset); err != nil {
			return connector.BackfillPageResult{}, fmt.Errorf("bad token %q: %w", pageToken, err)
		}
	}
	n := p.pageSize
	if offset+n > p.messages {
		n = p.messages - offset
	}
	res := connector.BackfillPageResult{Scanned: n, Captured: n - 1, Skipped: 1}
	if offset+n < p.messages {
		res.NextToken = fmt.Sprintf("off:%d", offset+n)
	}
	return res, nil
}

func readBackfillRow(t *testing.T, e *searchEnv, id ids.UUID) (status string, scanned, captured int, cursor []byte) {
	t.Helper()
	err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `
			SELECT status, scanned, captured, cursor FROM capture_backfill WHERE id = $1`, id).
			Scan(&status, &scanned, &captured, &cursor)
	})
	if err != nil {
		t.Fatal(err)
	}
	return status, scanned, captured, cursor
}

func TestBackfillLifecycle(t *testing.T) {
	e := setupSearch(t)
	prov := &pagedConnector{messages: 25, pageSize: 10}
	registry := newTestCaptureRegistry(e, newTestKeyvault(t, e))
	registry.Register(prov)

	grantCtx := e.humanWithScopes(e.Rep1, []principal.Scope{principal.ScopeRead})
	if _, err := registry.Connect(grantCtx, "gmail", connector.Auth("refresh")); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	rep := ids.From[ids.UserKind](e.Rep1)

	t.Run("preview estimates before anything spends", func(t *testing.T) {
		msgs, err := registry.EstimateBackfill(grantCtx, "gmail", rep, 6)
		if err != nil {
			t.Fatalf("EstimateBackfill: %v", err)
		}
		if msgs != 25 {
			t.Fatalf("estimate = %d msgs, want 25", msgs)
		}
		if _, err := registry.EstimateBackfill(grantCtx, "gmail", rep, 5); !errors.Is(err, capture.ErrWindowInvalid) {
			t.Fatalf("a 5-month window must be refused, got %v", err)
		}
	})

	run, err := registry.StartBackfill(grantCtx, "gmail", rep, 6, 25)
	if err != nil {
		t.Fatalf("StartBackfill: %v", err)
	}

	t.Run("one live run per connection", func(t *testing.T) {
		if _, err := registry.StartBackfill(grantCtx, "gmail", rep, 6, 25); !errors.Is(err, capture.ErrBackfillRunning) {
			t.Fatalf("second start while running = %v, want ErrBackfillRunning", err)
		}
	})

	wsCtx := principal.WithWorkspaceID(context.Background(), e.WS)

	t.Run("each page commits cursor and counters — a resume never re-pages", func(t *testing.T) {
		done, completed, err := registry.RunBackfillStep(wsCtx, run.ID)
		if err != nil {
			t.Fatalf("step 1: %v", err)
		}
		if done || completed {
			t.Fatalf("25 messages at 10/page cannot finish in one step (done=%v completed=%v)", done, completed)
		}
		status, scanned, captured, cursor := readBackfillRow(t, e, run.ID)
		if status != "running" || scanned != 10 || captured != 9 {
			t.Fatalf("after page 1: status=%s scanned=%d captured=%d, want running/10/9", status, scanned, captured)
		}
		if len(cursor) == 0 {
			t.Fatal("the page token must be committed — a killed worker resumes from it")
		}

		// The "worker died, River retried" path is just: call again from the
		// committed row. The provider sees the NEXT token, not a replay.
		if done, completed, err = registry.RunBackfillStep(wsCtx, run.ID); err != nil || done || completed {
			t.Fatalf("step 2: done=%v completed=%v err=%v", done, completed, err)
		}
		done, completed, err = registry.RunBackfillStep(wsCtx, run.ID)
		if err != nil {
			t.Fatalf("step 3: %v", err)
		}
		if !done || !completed {
			t.Fatalf("the third page exhausts the window; the step must report done AND completed (done=%v completed=%v)", done, completed)
		}
		status, scanned, captured, _ = readBackfillRow(t, e, run.ID)
		if status != "done" || scanned != 25 || captured != 22 {
			t.Fatalf("terminal row: status=%s scanned=%d captured=%d, want done/25/22", status, scanned, captured)
		}
		if prov.pageCalls != 3 {
			t.Fatalf("provider saw %d pages, want exactly 3 — no replays, no extras", prov.pageCalls)
		}
		// A step on the already-terminal run is a done no-op — and NOT a
		// second completion, so it never re-fires the digest.
		if done, completed, err := registry.RunBackfillStep(wsCtx, run.ID); err != nil || !done || completed {
			t.Fatalf("a step on a terminal run must be a done, not-completed no-op, got done=%v completed=%v err=%v", done, completed, err)
		}
	})

	t.Run("windows only widen", func(t *testing.T) {
		if _, err := registry.StartBackfill(grantCtx, "gmail", rep, 3, 25); !errors.Is(err, capture.ErrWindowNarrowing) {
			t.Fatalf("narrowing 6m→3m = %v, want ErrWindowNarrowing", err)
		}
		wider, err := registry.StartBackfill(grantCtx, "gmail", rep, 12, 25)
		if err != nil {
			t.Fatalf("widening 6m→12m: %v", err)
		}

		// Cancel the widened run: terminal, with captured counts retained.
		if _, err := registry.CancelBackfill(grantCtx, "gmail", rep); err != nil {
			t.Fatalf("CancelBackfill: %v", err)
		}
		status, _, _, _ := readBackfillRow(t, e, wider.ID)
		if status != "cancelled" {
			t.Fatalf("status = %s, want cancelled", status)
		}
		if _, err := registry.CancelBackfill(grantCtx, "gmail", rep); err == nil {
			t.Fatal("cancelling with nothing live must refuse, not invent a run")
		}
	})

	t.Run("the list surface carries the newest run", func(t *testing.T) {
		views, err := registry.Connections(grantCtx)
		if err != nil {
			t.Fatalf("Connections: %v", err)
		}
		if len(views) != 1 || views[0].Backfill == nil {
			t.Fatalf("want one connection carrying its backfill summary, got %+v", views)
		}
		if views[0].Backfill.Status != "cancelled" {
			t.Fatalf("list backfill status = %s, want the newest run (cancelled)", views[0].Backfill.Status)
		}
	})
}

// A backfill step that faults before committing a page must fail the run
// terminally, never strand it queued or crash the pager. Two faults exercise
// the two pre-commit failure edges: an unreadable committed cursor (parse
// error) and a readable cursor the provider then rejects (page error).
func TestBackfillStepFaultsAreTerminal(t *testing.T) {
	e := setupSearch(t)
	registry := newTestCaptureRegistry(e, newTestKeyvault(t, e))
	registry.Register(&pagedConnector{messages: 25, pageSize: 10})
	grantCtx := e.humanWithScopes(e.Rep1, []principal.Scope{principal.ScopeRead})
	if _, err := registry.Connect(grantCtx, "gmail", connector.Auth("refresh")); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	rep := ids.From[ids.UserKind](e.Rep1)
	wsCtx := principal.WithWorkspaceID(context.Background(), e.WS)

	// startWithCursor opens a fresh run and overwrites its committed cursor.
	// The prior run is terminal (error) by the time the next opens, so the
	// one-live-run guard permits it and the same 6-month window never narrows.
	startWithCursor := func(t *testing.T, cursorJSON string) ids.UUID {
		t.Helper()
		run, err := registry.StartBackfill(grantCtx, "gmail", rep, 6, 25)
		if err != nil {
			t.Fatalf("StartBackfill: %v", err)
		}
		if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
			_, execErr := tx.Exec(e.Admin(),
				`UPDATE capture_backfill SET cursor = $2::jsonb WHERE id = $1`, run.ID, cursorJSON)
			return execErr
		}); err != nil {
			t.Fatalf("seed cursor: %v", err)
		}
		return run.ID
	}
	// A faulting step never reports completion and always records the run as
	// error. (done varies by fault: a cursor-parse fault is terminal-stop
	// done=true; a page fault returns done=false so the worker halts on the
	// error — both leave the row error, never queued.)
	assertTerminalError := func(t *testing.T, id ids.UUID) {
		t.Helper()
		_, completed, err := registry.RunBackfillStep(wsCtx, id)
		if completed || err == nil {
			t.Fatalf("faulting step = completed=%v err=%v, want a not-completed failure", completed, err)
		}
		if status, _, _, _ := readBackfillRow(t, e, id); status != "error" {
			t.Fatalf("row status = %s, want error — the fault was recorded, not looped", status)
		}
	}

	t.Run("an unreadable committed cursor (wrong JSON shape) fails the run", func(t *testing.T) {
		// Valid JSON, but an array — not the {"page_token":...} object.
		assertTerminalError(t, startWithCursor(t, `[1,2,3]`))
	})
	t.Run("a page the provider rejects fails the run", func(t *testing.T) {
		// Readable cursor; the provider then rejects the malformed token.
		assertTerminalError(t, startWithCursor(t, `{"page_token":"not-an-offset"}`))
	})
}

// The backfill status reports people and companies as LIVE counts of the
// connector-created rows since the run began (the stored counters are never
// filled), and it excludes anything a human created — so the hero shows the
// real reach of the import, not a stuck zero.
func TestBackfillStatusCountsConnectorCounterpartiesSinceStart(t *testing.T) {
	e := setupSearch(t)
	registry := newTestCaptureRegistry(e, newTestKeyvault(t, e))
	registry.Register(&pagedConnector{messages: 25, pageSize: 10})
	grantCtx := e.humanWithScopes(e.Rep1, []principal.Scope{principal.ScopeRead})
	if _, err := registry.Connect(grantCtx, "gmail", connector.Auth("refresh")); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	rep := ids.From[ids.UserKind](e.Rep1)

	if _, err := registry.StartBackfill(grantCtx, "gmail", rep, 6, 25); err != nil {
		t.Fatalf("StartBackfill: %v", err)
	}
	// Two connector-created counterparties (what the auto-create path lands)
	// and one human-created person that must NOT inflate the import's count.
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		for _, q := range []string{
			`INSERT INTO person (workspace_id, full_name, source, captured_by)
			   VALUES (current_setting('app.workspace_id')::uuid, 'Ada Capture', 'capture', 'connector:gmail')`,
			`INSERT INTO person (workspace_id, full_name, source, captured_by)
			   VALUES (current_setting('app.workspace_id')::uuid, 'Manually Typed', 'manual', 'human:someone')`,
			`INSERT INTO organization (workspace_id, display_name, source, captured_by)
			   VALUES (current_setting('app.workspace_id')::uuid, 'Acme Capture', 'capture', 'connector:gmail')`,
		} {
			if _, execErr := tx.Exec(e.Admin(), q); execErr != nil {
				return execErr
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seed counterparties: %v", err)
	}

	run, err := registry.BackfillStatus(grantCtx, "gmail", rep)
	if err != nil || run == nil {
		t.Fatalf("BackfillStatus: %v (run=%v)", err, run)
	}
	if run.People != 1 {
		t.Fatalf("people = %d, want 1 — the connector person counted, the human one excluded", run.People)
	}
	if run.Organizations != 1 {
		t.Fatalf("organizations = %d, want 1 — the connector org counted", run.Organizations)
	}
}
