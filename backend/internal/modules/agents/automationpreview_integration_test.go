// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package agents

// The designer's dry-run (A72/ADR-0035 Am.1) over a real migrated
// Postgres: the preview matches current records under the caller's row
// scope with the masked count stated honestly, estimates the trailing
// window from real firing history, and — the heart of it — applies
// NOTHING: no domain write, no audit row, no outbox event, no run row.

import (
	"errors"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// dealFixture seeds a pipeline with an open and a won stage plus five
// deals: rep1's open deal, two of rep2's, a won one, an archived one.
type dealFixture struct {
	openStage ids.UUID
	wonStage  ids.UUID
	ownDeal   ids.UUID
}

func seedDealBoard(t *testing.T, fx *autoFixture) dealFixture {
	t.Helper()
	pipeline, openStage, wonStage := ids.NewV7(), ids.NewV7(), ids.NewV7()
	fx.exec(t, `INSERT INTO pipeline (id, workspace_id, name, is_default) VALUES ($1, $2, 'Sales', true)`, pipeline, fx.ws)
	fx.exec(t, `INSERT INTO stage (id, workspace_id, pipeline_id, name, position, semantic, win_probability)
		VALUES ($1, $2, $3, 'Qualify', 1, 'open', 20)`, openStage, fx.ws, pipeline)
	fx.exec(t, `INSERT INTO stage (id, workspace_id, pipeline_id, name, position, semantic, win_probability)
		VALUES ($1, $2, $3, 'Won', 2, 'won', 100)`, wonStage, fx.ws, pipeline)

	seedDeal := func(owner ids.UUID, status string, archived bool) ids.UUID {
		id := ids.NewV7()
		var closedAt, archivedAt *time.Time
		if status != "open" {
			ts := time.Date(2026, 6, 20, 10, 0, 0, 0, time.UTC)
			closedAt = &ts
		}
		if archived {
			ts := time.Date(2026, 6, 21, 10, 0, 0, 0, time.UTC)
			archivedAt = &ts
		}
		fx.exec(t, `
			INSERT INTO deal (id, workspace_id, name, pipeline_id, stage_id, owner_id, status, closed_at, archived_at, source, captured_by)
			VALUES ($1, $2, 'Deal', $3, $4, $5, $6, $7, $8, 'manual', 'human:test')`,
			id, fx.ws, pipeline, openStage, owner, status, closedAt, archivedAt)
		return id
	}
	own := seedDeal(fx.rep1, "open", false)
	seedDeal(fx.rep2, "open", false)
	seedDeal(fx.rep2, "open", false)
	seedDeal(fx.rep1, "won", false)
	seedDeal(fx.rep1, "open", true)
	return dealFixture{openStage: openStage, wonStage: wonStage, ownDeal: own}
}

func TestPreviewMatchesCurrentRecordsWithoutApplying(t *testing.T) {
	fx := setupAutomationDB(t)
	store := NewAutomationStore(fx.pool)
	autoID := fx.seedAutomation(t, "stage_change_create_task")
	board := seedDealBoard(t, fx)

	// Firing history: three open-destination moves inside the 30-day
	// window, one outside it, one to a won stage — only the three count.
	now := time.Now().UTC()
	moveAt := func(stage ids.UUID, at time.Time) {
		fx.exec(t, `
			INSERT INTO deal_stage_history (workspace_id, deal_id, to_stage_id, changed_by, changed_at)
			VALUES ($1, $2, $3, 'human:test', $4)`, fx.ws, board.ownDeal, stage, at)
	}
	moveAt(board.openStage, now.AddDate(0, 0, -1))
	moveAt(board.openStage, now.AddDate(0, 0, -5))
	moveAt(board.openStage, now.AddDate(0, 0, -29))
	moveAt(board.openStage, now.AddDate(0, 0, -40))
	moveAt(board.wonStage, now.AddDate(0, 0, -2))

	ctx := fx.humanCtx(fx.rep1, principal.RowScopeOwn)
	auditBefore := fx.count(t, `SELECT count(*) FROM audit_log WHERE workspace_id = $1`, fx.ws)
	outboxBefore := fx.count(t, `SELECT count(*) FROM event_outbox`)

	res, err := store.Preview(ctx, autoID, AutomationPreviewInput{})
	if err != nil {
		t.Fatal(err)
	}
	if res.WindowDays != 30 {
		t.Fatalf("window = %d, want the 30-day default", res.WindowDays)
	}
	// Three live open deals match; rep1 (row_scope=own) sees only their
	// own — the other two are masked, honestly counted.
	if res.MatchesNow != 1 || res.ExcludedByPermission != 2 {
		t.Fatalf("matches=%d excluded=%d, want 1 visible + 2 masked", res.MatchesNow, res.ExcludedByPermission)
	}
	if len(res.Sample) != 1 || res.Sample[0] != board.ownDeal {
		t.Fatalf("sample = %v, want exactly the caller's own matching deal", res.Sample)
	}
	if res.WouldHaveFired == nil || *res.WouldHaveFired != 3 {
		t.Fatalf("would_have_fired = %v, want 3 open-destination moves in the window", res.WouldHaveFired)
	}

	// The dry-run is a READ: no audit row, no outbox event, no task
	// minted, no run recorded.
	if after := fx.count(t, `SELECT count(*) FROM audit_log WHERE workspace_id = $1`, fx.ws); after != auditBefore {
		t.Fatalf("preview wrote %d audit rows — a preview is a read", after-auditBefore)
	}
	if after := fx.count(t, `SELECT count(*) FROM event_outbox`); after != outboxBefore {
		t.Fatalf("preview wrote %d outbox rows — a preview is a read", after-outboxBefore)
	}
	if n := fx.count(t, `SELECT count(*) FROM activity WHERE workspace_id = $1`, fx.ws); n != 0 {
		t.Fatalf("preview minted %d activities — it must never apply the plan", n)
	}
	if n := fx.count(t, `SELECT count(*) FROM workflow_run WHERE workspace_id = $1`, fx.ws); n != 0 {
		t.Fatalf("preview recorded %d runs — a dry-run is not a firing", n)
	}
}

func TestPreviewDraftOverrideAndValidation(t *testing.T) {
	fx := setupAutomationDB(t)
	store := NewAutomationStore(fx.pool)
	autoID := fx.seedAutomation(t, "stage_change_create_task")
	ctx := fx.humanCtx(fx.rep1, principal.RowScopeOwn)

	fx.exec(t, `INSERT INTO lead (workspace_id, full_name, source, captured_by) VALUES ($1, 'Unrouted A', 'manual', 'human:test')`, fx.ws)
	fx.exec(t, `INSERT INTO lead (workspace_id, full_name, source, captured_by) VALUES ($1, 'Unrouted B', 'manual', 'human:test')`, fx.ws)
	fx.exec(t, `INSERT INTO lead (workspace_id, full_name, owner_id, status, source, captured_by) VALUES ($1, 'Routed', $2, 'working', 'manual', 'human:test')`, fx.ws, fx.rep2)
	fx.exec(t, `INSERT INTO lead (workspace_id, full_name, status, source, captured_by) VALUES ($1, 'Dead', 'disqualified', 'manual', 'human:test')`, fx.ws)

	// A draft override previews another recipe under the same stored
	// instance's RBAC/404 anchor (the editor's preview-before-save).
	routeKey := "route_lead"
	window := 7
	draft, err := store.Preview(ctx, autoID, AutomationPreviewInput{Key: &routeKey, WindowDays: &window})
	if err != nil {
		t.Fatal(err)
	}
	// Two unrouted live leads match (ownerless rows are visible at every
	// scope); the routed and disqualified ones sit outside the When/If.
	if draft.MatchesNow != 2 || draft.ExcludedByPermission != 0 || draft.WindowDays != 7 {
		t.Fatalf("draft preview = %+v, want 2 visible matches over a 7-day window", draft)
	}
	if draft.WouldHaveFired == nil || *draft.WouldHaveFired != 4 {
		t.Fatalf("draft would_have_fired = %v, want all 4 leads created in the window", draft.WouldHaveFired)
	}

	badKey := "not_a_type"
	if _, err := store.Preview(ctx, autoID, AutomationPreviewInput{Key: &badKey}); err == nil {
		t.Fatal("an unknown draft key must be a validation error")
	}
	badWindow := 0
	if _, err := store.Preview(ctx, autoID, AutomationPreviewInput{WindowDays: &badWindow}); err == nil {
		t.Fatal("window_days=0 must be a validation error")
	}
	if _, err := store.Preview(ctx, ids.New[ids.AutomationKind](), AutomationPreviewInput{}); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("absent automation preview → %v, want ErrNotFound", err)
	}
}
