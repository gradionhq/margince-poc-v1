// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package automation

// Task 14b's two integration proofs, generalizing occurrence_integration_test.go's
// scriptedClockWorkflow proof to the REAL handlers:
//
//   - check_in_cadence fires once per quiet spell, driven through
//     engine.runOne exactly as a real TimeScanner pass would (mirroring
//     no_activity_reminder's own unit-level rigor, at the DB layer).
//   - renewal_reminder, whose candidate source is deferred (see its own
//     doc in handlers_clock.go), is proven a genuine no-op:
//     a real TimeScanner.Scan pass over a workspace with an ENABLED
//     renewal_reminder instance never claims a workflow_run row for it —
//     the honest "environment absent" behavior, not silence masking a bug.

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
	"github.com/gradionhq/margince/backend/internal/shared/ports/workflow"
)

// fakeCreateTaskProvider is the minimal datasource.SystemOfRecordProvider
// stub check_in_cadence's real Apply needs: its only planned action is
// create_task, so only Create is stubbed to succeed; every other method
// panics — this suite reaching one would mean it exercised the wrong
// branch.
type fakeCreateTaskProvider struct {
	calls []datasource.CreateInput
}

func (p *fakeCreateTaskProvider) Create(_ context.Context, in datasource.CreateInput) (datasource.EntityRef, error) {
	p.calls = append(p.calls, in)
	return datasource.EntityRef{Type: in.EntityType, ID: ids.NewV7()}, nil
}

func (p *fakeCreateTaskProvider) Read(context.Context, datasource.EntityRef) (datasource.Record, error) {
	panic("fakeCreateTaskProvider: Read not stubbed for this test")
}

func (p *fakeCreateTaskProvider) Search(context.Context, datasource.SearchQuery) (datasource.SearchResult, error) {
	panic("fakeCreateTaskProvider: Search not stubbed for this test")
}

func (p *fakeCreateTaskProvider) ListObjects(context.Context) ([]datasource.ObjectDef, error) {
	panic("fakeCreateTaskProvider: ListObjects not stubbed for this test")
}

func (p *fakeCreateTaskProvider) ListFields(context.Context, datasource.EntityType) ([]datasource.FieldDef, error) {
	panic("fakeCreateTaskProvider: ListFields not stubbed for this test")
}

func (p *fakeCreateTaskProvider) RunReport(context.Context, datasource.ReportPlan) (datasource.ReportResult, error) {
	panic("fakeCreateTaskProvider: RunReport not stubbed for this test")
}

func (p *fakeCreateTaskProvider) StageSemantic(context.Context, ids.UUID) (string, ids.UUID, error) {
	panic("fakeCreateTaskProvider: StageSemantic not stubbed for this test")
}

func (p *fakeCreateTaskProvider) Update(context.Context, datasource.UpdateInput) (datasource.EntityRef, error) {
	panic("fakeCreateTaskProvider: Update not stubbed for this test")
}

func (p *fakeCreateTaskProvider) AdvanceDeal(context.Context, datasource.AdvanceDealInput) (datasource.EntityRef, error) {
	panic("fakeCreateTaskProvider: AdvanceDeal not stubbed for this test")
}

func (p *fakeCreateTaskProvider) Archive(context.Context, datasource.EntityRef) (datasource.EntityRef, error) {
	panic("fakeCreateTaskProvider: Archive not stubbed for this test")
}

func (p *fakeCreateTaskProvider) Merge(context.Context, datasource.MergeInput) (datasource.EntityRef, error) {
	panic("fakeCreateTaskProvider: Merge not stubbed for this test")
}

func (p *fakeCreateTaskProvider) PromoteLead(context.Context, ids.UUID, string, *string) (datasource.EntityRef, bool, error) {
	panic("fakeCreateTaskProvider: PromoteLead not stubbed for this test")
}

func (p *fakeCreateTaskProvider) Freshness(context.Context, datasource.EntityRef) (datasource.FreshnessInfo, error) {
	panic("fakeCreateTaskProvider: Freshness not stubbed for this test")
}

var _ datasource.SystemOfRecordProvider = (*fakeCreateTaskProvider)(nil)

// checkInClockEvent builds one check_in_cadence clock event the way
// TimeScanner's buildActivityAnchorEvent would for a real pass, with the
// AutomationID a real liveInstances read would carry.
func checkInClockEvent(t *testing.T, ws ids.UUID, automationID ids.AutomationID, entity datasource.EntityRef, now, anchor time.Time) workflow.Event {
	t.Helper()
	payload, err := json.Marshal(touchAnchorPayload{LastActivityAt: anchor})
	if err != nil {
		t.Fatalf("encoding anchor payload: %v", err)
	}
	return workflow.Event{
		ID:           ids.NewV7(),
		WorkspaceID:  ws,
		OccurredAt:   now,
		Entity:       entity,
		AutomationID: automationID.UUID,
		Payload:      payload,
	}
}

// TestCheckInCadenceFiresOncePerQuietSpell drives the REAL check_in_cadence
// handler (not a scripted stand-in) through engine.runOne twice over the
// SAME anchor — the occurrence key must absorb the redundant pass — then
// once more over a NEW anchor (the entity was touched, then went quiet a
// second time), which must re-arm it. Mirrors
// occurrence_integration_test.go's convention proof, generalized to the
// concrete handler Task 14b ships.
func TestCheckInCadenceFiresOncePerQuietSpell(t *testing.T) {
	fx := setupAutomationDB(t)
	provider := &fakeCreateTaskProvider{}
	engine := NewWorkflowEngine(fx.pool, nil) // nil resolver: this fixture's instance carries no owner_id, so the match-time gate skips before ever touching it
	h := checkInCadence{ex: Executors{Provider: provider}}
	instanceID := fx.seedAutomation(t, checkInCadenceName)
	entity := datasource.EntityRef{Type: datasource.EntityDeal, ID: ids.NewV7()}
	runCtx := principal.WithWorkspaceID(context.Background(), fx.ws)
	now := time.Date(2026, 7, 16, 9, 0, 0, 0, time.UTC)

	anchor := now.AddDate(0, 0, -defaultCheckInDays-1) // stale under the default cadence
	firstEvent := checkInClockEvent(t, fx.ws, instanceID, entity, now, anchor)
	firstKey := runKey(h, firstEvent)

	if err := engine.runOne(runCtx, h, firstEvent); err != nil {
		t.Fatalf("first pass: %v", err)
	}
	// A second pass over the SAME anchor: a fresh ev.ID (a real scan
	// mints one every tick) but the identical Payload.
	if err := engine.runOne(runCtx, h, checkInClockEvent(t, fx.ws, instanceID, entity, now, anchor)); err != nil {
		t.Fatalf("second pass over the unchanged anchor: %v", err)
	}
	got := runsForKey(t, fx, checkInCadenceName, firstKey)
	if len(got) != 1 || got[0].status != "applied" {
		t.Fatalf("rows for the unchanged anchor = %+v, want exactly one 'applied' row", got)
	}
	if len(provider.calls) != 1 {
		t.Fatalf("provider.Create called %d times across two passes over the SAME anchor, want exactly 1 — claimRun's ON CONFLICT must have absorbed the second pass before Apply ran", len(provider.calls))
	}

	// The entity is touched, then goes quiet again: a later, DIFFERENT
	// anchor is an additional occurrence, not a replay of the first.
	movedAnchor := anchor.AddDate(0, 0, -10)
	secondEvent := checkInClockEvent(t, fx.ws, instanceID, entity, now, movedAnchor)
	secondKey := runKey(h, secondEvent)
	if secondKey == firstKey {
		t.Fatal("moving the anchor produced the same runKey — this test would not exercise a re-arm")
	}
	if err := engine.runOne(runCtx, h, secondEvent); err != nil {
		t.Fatalf("firing on the second anchor: %v", err)
	}
	got = runsForKey(t, fx, checkInCadenceName, secondKey)
	if len(got) != 1 || got[0].status != "applied" {
		t.Fatalf("rows for the moved anchor = %+v, want exactly one 'applied' row", got)
	}
	if len(provider.calls) != 2 {
		t.Fatalf("provider.Create called %d times across two distinct anchors, want exactly 2", len(provider.calls))
	}
	// The first anchor's row must still be there, untouched: re-arming on
	// a new anchor is an ADDITIONAL firing, never a replacement.
	if got := runsForKey(t, fx, checkInCadenceName, firstKey); len(got) != 1 || got[0].status != "applied" {
		t.Fatalf("first anchor's row after the second firing = %+v, want it unchanged", got)
	}
}

// TestRenewalReminderScanIsANoOpWhenUnconfigured proves the honest
// deferred-enumeration posture end to end: a REAL TimeScanner.Scan pass
// over a workspace carrying an ENABLED renewal_reminder instance never
// claims a workflow_run row for it, because activityScanHandlers
// (timescan.go) has no candidate source wired for this handler. This is
// the sanctioned "environment absent" shape (like notify with no
// transport, seams.go's ErrNoNotificationTransport) — never a crash,
// never a fabricated firing.
func TestRenewalReminderScanIsANoOpWhenUnconfigured(t *testing.T) {
	fx := setupAutomationDB(t)
	engine := NewWorkflowEngine(fx.pool, nil)
	engine.RegisterWorkflow(renewalReminder{ex: Executors{}}) // Apply is never reached: Match never runs for an un-enumerated handler
	fx.seedAutomation(t, renewalReminderName)

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	now := time.Date(2026, 7, 16, 9, 0, 0, 0, time.UTC)
	scanner := NewTimeScannerWithClock(engine, &fakeActivityScan{}, func() time.Time { return now }, log)

	if err := scanner.Scan(context.Background()); err != nil {
		t.Fatalf("Scan: %v", err)
	}

	n := fx.count(t, `SELECT count(*) FROM workflow_run WHERE workspace_id = $1 AND handler = $2`, fx.ws, renewalReminderName)
	if n != 0 {
		t.Errorf("workflow_run rows for renewal_reminder = %d, want 0 — its candidate source is deferred, a scan pass must never fire it", n)
	}
}
