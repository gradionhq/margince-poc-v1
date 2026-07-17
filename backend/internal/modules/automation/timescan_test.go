// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package automation

// DB-free proofs for TimeScanner's two pure building blocks
// (timescan.go): scanWorkspaces (the per-workspace error-isolation loop)
// and scanInstanceCandidates (the event-synthesis step). Both are free
// functions specifically because TimeScanner.Scan itself always opens a
// real Postgres connection (fleet enumeration, then a per-workspace
// transaction for liveInstances/runOne) — exactly like
// deals/closedatesweep.go's Sweep, which carries no unit test of its own
// at all. Factoring the DB-free pieces out lets this suite prove the
// load-bearing behavior (isolation, fresh provenance, the anchor
// contract) without a database, while the full Scan wiring is proven
// against a real one by the integration suite.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
	"github.com/gradionhq/margince/backend/internal/shared/ports/workflow"
)

// TestScanWorkspacesIsolatesAFailingWorkspace proves the fleet-pass
// posture closedatesweep.go documents: one workspace's failure is logged,
// never returned, and never stops the pass from reaching the rest of the
// fleet.
func TestScanWorkspacesIsolatesAFailingWorkspace(t *testing.T) {
	failing := ids.NewV7()
	healthy := ids.NewV7()
	var visited []ids.UUID

	var logBuf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logBuf, nil))

	scanWorkspaces([]ids.UUID{failing, healthy}, func(wsID ids.UUID) error {
		visited = append(visited, wsID)
		if wsID == failing {
			return errors.New("boom: this workspace's automation table is unreachable")
		}
		return nil
	}, log)

	if len(visited) != 2 {
		t.Fatalf("workspaces visited = %v, want both %s and %s", visited, failing, healthy)
	}
	if !strings.Contains(logBuf.String(), failing.String()) {
		t.Errorf("log output %q does not name the failing workspace %s", logBuf.String(), failing)
	}
	if strings.Contains(logBuf.String(), healthy.String()) {
		t.Errorf("log output %q names the healthy workspace — only the failing one's error should be logged", logBuf.String())
	}
}

// fakeActivityScan is a DB-free stand-in for the ActivityScan seam: it
// records the cutoff/limit it was called with and returns a fixed
// candidate set.
type fakeActivityScan struct {
	candidates []EntityAnchor
	err        error
	calls      []struct {
		cutoff time.Time
		limit  int
	}
}

func (f *fakeActivityScan) LastTouchBefore(_ context.Context, cutoff time.Time, limit int) ([]EntityAnchor, error) {
	f.calls = append(f.calls, struct {
		cutoff time.Time
		limit  int
	}{cutoff, limit})
	if f.err != nil {
		return nil, f.err
	}
	return f.candidates, nil
}

// recordedRunCall is one invocation scanInstanceCandidates's run stub
// captured, so the test can inspect exactly what TimeScanner would have
// handed to WorkflowEngine.runOne without ever opening a transaction.
type recordedRunCall struct {
	handler workflow.Handler
	ev      workflow.Event
}

// TestScanInstanceCandidatesSynthesizesOneEventPerCandidate proves the
// occurrence-key contract's producing side (timescan.go's
// buildNoActivityEvent): each candidate gets its OWN fresh ev.ID
// (trigger_event provenance, engine_run.go's claimRun doc), the
// instance's OwnerID and AutomationID ride along (the Task-13 gate reads
// OwnerID), and the candidate's Anchor is recoverable from the event's
// Payload — the anchor a real handler's IdempotencyKey derives its key
// from.
func TestScanInstanceCandidatesSynthesizesOneEventPerCandidate(t *testing.T) {
	now := time.Date(2026, 7, 16, 9, 0, 0, 0, time.UTC)
	wsID := ids.NewV7()
	owner := ids.NewV7()
	automationID := ids.New[ids.AutomationKind]()
	inst := automationInstance{id: automationID, owner: owner, params: json.RawMessage(`{"no_activity_days": 14}`)}

	anchor1 := now.AddDate(0, 0, -20)
	anchor2 := now.AddDate(0, 0, -30)
	entity1 := datasource.EntityRef{Type: datasource.EntityDeal, ID: ids.NewV7()}
	entity2 := datasource.EntityRef{Type: datasource.EntityLead, ID: ids.NewV7()}
	scan := &fakeActivityScan{candidates: []EntityAnchor{
		{Ref: entity1, Anchor: anchor1},
		{Ref: entity2, Anchor: anchor2},
	}}

	var calls []recordedRunCall
	run := func(_ context.Context, h workflow.Handler, ev workflow.Event) error {
		calls = append(calls, recordedRunCall{handler: h, ev: ev})
		return nil
	}

	h := noActivityReminder{}
	if err := scanInstanceCandidates(context.Background(), scan, h, inst, wsID, now, run, noActivityDays); err != nil {
		t.Fatalf("scanInstanceCandidates: %v", err)
	}

	if len(scan.calls) != 1 {
		t.Fatalf("LastTouchBefore called %d times, want exactly 1", len(scan.calls))
	}
	wantCutoff := now.AddDate(0, 0, -14) // the instance's own params, not the 7-day default
	if !scan.calls[0].cutoff.Equal(wantCutoff) {
		t.Errorf("cutoff = %s, want %s (the instance's own no_activity_days=14)", scan.calls[0].cutoff, wantCutoff)
	}

	if len(calls) != 2 {
		t.Fatalf("run called %d times, want exactly 2 (one per candidate)", len(calls))
	}
	if calls[0].ev.ID == calls[1].ev.ID {
		t.Error("both synthesized events share an ev.ID — trigger_event provenance must be fresh per candidate")
	}
	for i, call := range calls {
		if call.ev.ID == ids.Nil {
			t.Errorf("call %d: ev.ID is the zero UUID — workflow_run.trigger_event is NOT NULL", i)
		}
		if call.ev.WorkspaceID != wsID {
			t.Errorf("call %d: ev.WorkspaceID = %s, want %s", i, call.ev.WorkspaceID, wsID)
		}
		if call.ev.AutomationID != automationID.UUID {
			t.Errorf("call %d: ev.AutomationID = %s, want %s", i, call.ev.AutomationID, automationID.UUID)
		}
		if call.ev.OwnerID != owner {
			t.Errorf("call %d: ev.OwnerID = %s, want %s — the match-time owner gate reads this", i, call.ev.OwnerID, owner)
		}
	}
	if calls[0].ev.Entity != entity1 || calls[1].ev.Entity != entity2 {
		t.Errorf("entities = %+v, %+v — want %+v, %+v in order", calls[0].ev.Entity, calls[1].ev.Entity, entity1, entity2)
	}

	gotAnchor1, err := touchAnchor(calls[0].ev)
	if err != nil || !gotAnchor1.Equal(anchor1) {
		t.Errorf("first event's anchor = %v (err %v), want %v", gotAnchor1, err, anchor1)
	}
	gotAnchor2, err := touchAnchor(calls[1].ev)
	if err != nil || !gotAnchor2.Equal(anchor2) {
		t.Errorf("second event's anchor = %v (err %v), want %v", gotAnchor2, err, anchor2)
	}
}

// TestScanInstanceCandidatesStopsOnARunFailure proves a real dispatch
// failure (as opposed to a per-workspace isolation boundary, which lives
// one level up in scanWorkspaces) surfaces rather than being swallowed —
// the second candidate's run error must reach the caller.
func TestScanInstanceCandidatesStopsOnARunFailure(t *testing.T) {
	now := time.Date(2026, 7, 16, 9, 0, 0, 0, time.UTC)
	inst := automationInstance{id: ids.New[ids.AutomationKind]()}
	scan := &fakeActivityScan{candidates: []EntityAnchor{
		{Ref: datasource.EntityRef{Type: datasource.EntityDeal, ID: ids.NewV7()}, Anchor: now.AddDate(0, 0, -10)},
	}}
	runErr := errors.New("runOne: claiming the run row failed")
	run := func(context.Context, workflow.Handler, workflow.Event) error { return runErr }

	err := scanInstanceCandidates(context.Background(), scan, noActivityReminder{}, inst, ids.NewV7(), now, run, noActivityDays)
	if !errors.Is(err, runErr) {
		t.Fatalf("scanInstanceCandidates err = %v, want it to wrap %v", err, runErr)
	}
}

// TestActivityScanHandlersRoutesEachHandlerToItsOwnDaysReader is the
// generalized-dispatch proof (Task 14b): scanWorkspace looks a clock
// handler's enumerator up in this map rather than a growing if/else
// chain, so this proves each of the two ActivityScan-driven catalog
// names resolves to ITS OWN days reader (never the other's, never a
// shared default) and that a handler with no ActivityScan enumeration —
// renewal_reminder, whose candidate source is deferred, see
// handlers_clock.go's renewalReminder doc — has no entry at
// all, so scanWorkspace's map lookup honestly skips it instead of
// mishandling it as an ActivityScan consumer.
func TestActivityScanHandlersRoutesEachHandlerToItsOwnDaysReader(t *testing.T) {
	noActivity, ok := activityScanHandlers[noActivityReminderName]
	if !ok {
		t.Fatal("activityScanHandlers has no entry for no_activity_reminder")
	}
	days, err := noActivity(nil)
	if err != nil || days != defaultNoActivityDays {
		t.Errorf("activityScanHandlers[no_activity_reminder](nil) = (%d, %v), want (%d, nil) — it must resolve to noActivityDays, not check_in_cadence's reader", days, err, defaultNoActivityDays)
	}

	checkIn, ok := activityScanHandlers[checkInCadenceName]
	if !ok {
		t.Fatal("activityScanHandlers has no entry for check_in_cadence")
	}
	days, err = checkIn(nil)
	if err != nil || days != defaultCheckInDays {
		t.Errorf("activityScanHandlers[check_in_cadence](nil) = (%d, %v), want (%d, nil) — it must resolve to checkInCadenceDays, not no_activity_reminder's reader", days, err, defaultCheckInDays)
	}

	if _, ok := activityScanHandlers[renewalReminderName]; ok {
		t.Error("activityScanHandlers has an entry for renewal_reminder — its candidate source is deferred (handlers_clock.go), scanWorkspace must skip it honestly rather than treat it as an ActivityScan consumer")
	}
}
