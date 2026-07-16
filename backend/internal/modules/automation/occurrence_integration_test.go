// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package automation

// The load-bearing proof for Task 12's fix (interfaces.md §5): a clock
// trigger's condition is continuously true once its anchor is stale
// enough ("no activity for 7 days" stays true on day 8, 9, 10 …), so its
// idempotency key is the ANCHOR, not ev.ID — and runOne must never let a
// non-matching pass claim that key. Before the fix, runOne routed every
// non-match through recordSkip, which claims (workspace_id, handler,
// idempotency_key) with ON CONFLICT DO NOTHING: the first coarse-filter
// pass over a not-yet-stale anchor would claim the row as 'skipped', and
// the real firing once the anchor went stale would hit that same row and
// silently do nothing — a redelivery-shaped bug indistinguishable from
// "working" until a run history audit asked why an automation everyone
// believed was live had never once fired.
//
// Each test drives Match with the scripted handler's own boolean
// (occurrence_test.go) rather than a real clock — the anchor only needs
// to be a stable value two runOne calls can share, never real time.

import (
	"context"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
	"github.com/gradionhq/margince/backend/internal/shared/ports/workflow"
)

// clockRun is one workflow_run row's shape as this suite needs to read
// it back: the status the fix must get right, plus trigger_event to
// prove the NOT NULL provenance contract (workflow_run.trigger_event is
// NOT NULL; a clock pass has no source event, so its caller — the
// time-scan, Task 14 — must synthesize a real ids.NewV7() per pass
// rather than let the zero UUID reach this column).
type clockRun struct {
	status       string
	triggerEvent ids.UUID
}

// runsForKey reads every row this engine ever claimed for one exact
// (handler, idempotency_key) pair, oldest first — the anchor key IS the
// unit of dedupe under test, so queries here never scope by anything
// coarser (a plain handler-name query would hide the very row-per-anchor
// distinction test 3 proves).
func runsForKey(t *testing.T, fx *autoFixture, handler, key string) []clockRun {
	t.Helper()
	rows, err := fx.owner.Query(context.Background(), `
		SELECT status, trigger_event FROM workflow_run
		WHERE workspace_id = $1 AND handler = $2 AND idempotency_key = $3
		ORDER BY created_at`, fx.ws, handler, key)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []clockRun
	for rows.Next() {
		var r clockRun
		if err := rows.Scan(&r.status, &r.triggerEvent); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// clockPass builds the workflow.Event one time-scan evaluation would
// synthesize for a clock candidate: a FRESH per-pass id (the
// trigger_event provenance contract above), the same automation
// instance and entity across passes.
func clockPass(ws ids.UUID, automationID ids.AutomationID, entity datasource.EntityRef) workflow.Event {
	return workflow.Event{
		ID:           ids.NewV7(),
		Type:         "clock.no_activity",
		WorkspaceID:  ws,
		OccurredAt:   time.Date(2026, 7, 16, 9, 0, 0, 0, time.UTC),
		Entity:       entity,
		AutomationID: automationID.UUID,
	}
}

// TestClockTriggerFiresAfterEarlierNonMatchingPasses is the trap-catcher:
// against the pre-fix code, pass 1's non-match would claim the anchor
// key as 'skipped', and pass 2's real match would then hit claimRun's
// ON CONFLICT DO NOTHING, find claimed == false, and return nil having
// never called Apply — the automation silently never fires again for
// this anchor. A test that only asserted "at most one run" cannot tell
// that outcome apart from a healthy single firing; this one asserts the
// firing actually happened.
func TestClockTriggerFiresAfterEarlierNonMatchingPasses(t *testing.T) {
	fx := setupAutomationDB(t)
	engine := NewWorkflowEngine(fx.pool)
	instanceID := fx.seedAutomation(t, "clock_first_firing")
	entity := datasource.EntityRef{Type: datasource.EntityDeal, ID: ids.NewV7()}
	runCtx := principal.WithWorkspaceID(context.Background(), fx.ws)

	h := &scriptedClockWorkflow{name: "clock_first_firing", anchor: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	key := runKey(h, workflow.Event{AutomationID: instanceID.UUID})

	// Pass 1: the coarse pre-filter yields this candidate, but the anchor
	// has not gone stale enough yet for the real condition.
	h.matches = false
	if err := engine.runOne(runCtx, h, clockPass(fx.ws, instanceID, entity)); err != nil {
		t.Fatalf("pass 1 (non-match): %v", err)
	}
	if got := runsForKey(t, fx, h.name, key); len(got) != 0 {
		t.Fatalf("after a non-matching clock pass, rows for the anchor key = %+v, want none — "+
			"the fix must not let recordSkip claim a clock trigger's anchor key", got)
	}

	// Pass 2: the SAME anchor, now stale enough — the real firing.
	h.matches = true
	if err := engine.runOne(runCtx, h, clockPass(fx.ws, instanceID, entity)); err != nil {
		t.Fatalf("pass 2 (match): %v", err)
	}

	got := runsForKey(t, fx, h.name, key)
	if len(got) != 1 || got[0].status != "applied" {
		t.Fatalf("rows for the anchor key after the real firing = %+v, want exactly ONE row with status "+
			"'applied' — not zero (the automation never fired) and not 'skipped' (pass 1's non-match trap)", got)
	}
	if got[0].triggerEvent == ids.Nil {
		t.Error("workflow_run.trigger_event is the zero UUID — a clock firing must carry a real per-pass event id as provenance")
	}
	if h.applies != 1 {
		t.Fatalf("Apply called %d times, want exactly 1", h.applies)
	}
}

// TestClockTriggerAtMostOnceOnAnUnchangedAnchor proves the anchor key
// dedupes a second true evaluation of the SAME anchor — e.g. two
// overlapping time-scan passes before the first one's effects (moving
// last_activity_at, say) would have re-armed it.
func TestClockTriggerAtMostOnceOnAnUnchangedAnchor(t *testing.T) {
	fx := setupAutomationDB(t)
	engine := NewWorkflowEngine(fx.pool)
	instanceID := fx.seedAutomation(t, "clock_at_most_once")
	entity := datasource.EntityRef{Type: datasource.EntityDeal, ID: ids.NewV7()}
	runCtx := principal.WithWorkspaceID(context.Background(), fx.ws)

	h := &scriptedClockWorkflow{
		name:    "clock_at_most_once",
		anchor:  time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		matches: true,
	}
	key := runKey(h, workflow.Event{AutomationID: instanceID.UUID})

	if err := engine.runOne(runCtx, h, clockPass(fx.ws, instanceID, entity)); err != nil {
		t.Fatalf("first pass: %v", err)
	}
	if err := engine.runOne(runCtx, h, clockPass(fx.ws, instanceID, entity)); err != nil {
		t.Fatalf("second pass over the unchanged anchor: %v", err)
	}

	got := runsForKey(t, fx, h.name, key)
	if len(got) != 1 || got[0].status != "applied" {
		t.Fatalf("rows for the unchanged anchor = %+v, want exactly one 'applied' row", got)
	}
	if h.applies != 1 {
		t.Fatalf("Apply called %d times across two passes over the SAME anchor, want exactly 1 — "+
			"claimRun's ON CONFLICT must have absorbed the second pass before Apply ran", h.applies)
	}
}

// TestClockTriggerRearmsWhenTheAnchorMoves proves the key is scoped to
// the anchor's VALUE, not "once forever" for the automation instance: a
// second true evaluation over a NEW anchor (last_activity_at moved
// again after a fresh burst of inactivity) must fire again.
func TestClockTriggerRearmsWhenTheAnchorMoves(t *testing.T) {
	fx := setupAutomationDB(t)
	engine := NewWorkflowEngine(fx.pool)
	instanceID := fx.seedAutomation(t, "clock_rearm")
	entity := datasource.EntityRef{Type: datasource.EntityDeal, ID: ids.NewV7()}
	runCtx := principal.WithWorkspaceID(context.Background(), fx.ws)

	h := &scriptedClockWorkflow{
		name:    "clock_rearm",
		anchor:  time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		matches: true,
	}
	firstKey := runKey(h, workflow.Event{AutomationID: instanceID.UUID})
	if err := engine.runOne(runCtx, h, clockPass(fx.ws, instanceID, entity)); err != nil {
		t.Fatalf("firing on the first anchor: %v", err)
	}
	if got := runsForKey(t, fx, h.name, firstKey); len(got) != 1 || got[0].status != "applied" {
		t.Fatalf("rows for the first anchor = %+v, want exactly one 'applied' row", got)
	}

	// The anchor moves: a later burst of inactivity, or a later due date.
	h.anchor = time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	secondKey := runKey(h, workflow.Event{AutomationID: instanceID.UUID})
	if secondKey == firstKey {
		t.Fatal("moving h.anchor produced the same runKey — the test fixture itself is not exercising a re-arm")
	}
	if err := engine.runOne(runCtx, h, clockPass(fx.ws, instanceID, entity)); err != nil {
		t.Fatalf("firing on the second anchor: %v", err)
	}

	if got := runsForKey(t, fx, h.name, secondKey); len(got) != 1 || got[0].status != "applied" {
		t.Fatalf("rows for the second anchor = %+v, want exactly one 'applied' row", got)
	}
	// The first anchor's row must still be there, untouched: re-arming on
	// a new anchor is an ADDITIONAL firing, never a replacement of the old
	// one's history.
	if got := runsForKey(t, fx, h.name, firstKey); len(got) != 1 || got[0].status != "applied" {
		t.Fatalf("first anchor's row after the second firing = %+v, want it unchanged", got)
	}
	if h.applies != 2 {
		t.Fatalf("Apply called %d times across two distinct anchors, want exactly 2", h.applies)
	}
}
