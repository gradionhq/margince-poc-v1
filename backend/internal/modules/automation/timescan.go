// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package automation

// The CLOCK-trigger entry point (Task 14): event triggers reach runOne
// off the bus (engine.go's HandleEvent); a clock trigger has no event
// to arrive, so TimeScanner enumerates candidates itself and converges
// them onto the SAME runOne (engine_run.go) — the Task-12 occurrence
// key and the Task-13 match-time owner gate (gate.go) apply automatically,
// because nothing downstream of runOne can tell a synthesized clock pass
// from a bus delivery. River-agnostic by construction: this file never
// imports River (compose/jobs.go's own doc — the adapters are the only
// code that knows about River); a River periodic job simply calls Scan.
//
// Mirrors deals/closedatesweep.go's CloseDateCorrector.Sweep shape: fleet-
// enumerate workspaces (the rls-exempt marker below), then a per-workspace
// pass whose own failure is logged, never returned, so one bad tenant
// never starves the rest of the fleet.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/workflow"
)

// clockScanBatchLimit bounds how many stale candidates one instance's
// pass draws per tick, for every ActivityScan-driven clock handler
// (no_activity_reminder, check_in_cadence) — the same fleet-pass-cap
// reasoning closedatesweep.go's closeDateBatch documents (a migrated
// backlog drains over successive ticks rather than blocking the pass).
const clockScanBatchLimit = 200

// activityScanHandlers maps each ActivityScan-driven clock handler's
// catalog name to its own days-knob reader (handlers_clock.go):
// no_activity_reminder and check_in_cadence share the IDENTICAL
// LastTouchBefore candidate source and differ only in which params key
// names their own cadence. scanWorkspace looks a handler's enumerator up
// here rather than growing an if/else chain, so adding a THIRD
// ActivityScan-driven handler later is one map entry, not a new branch.
//
// A handler with no entry here — renewal_reminder, today — rides a
// different anchor entirely (a custom field's value, not a last-touch
// timestamp) and has no candidate source wired at all; see its own doc
// in handlers_clock.go for why. scanWorkspace below skips it
// honestly rather than mishandling it as an ActivityScan consumer it
// is not.
var activityScanHandlers = map[string]clockDaysExtractor{
	noActivityReminderName: noActivityDays,
	checkInCadenceName:     checkInCadenceDays,
}

// TimeScanner drives every CLOCK-triggered automation instance: it holds
// the WorkflowEngine so it can call e.runOne (same package) and the
// ActivityScan seam so.no_activity_reminder's candidates are read from
// activities' own tables, never a direct query against a sibling's.
type TimeScanner struct {
	engine *WorkflowEngine
	scan   ActivityScan
	// now is the scanner's clock (the quotas.NewStoreWithClock spelling —
	// there is no Clock interface in this repo): captured ONCE per Scan
	// call so every workspace and every instance in one pass agrees on
	// what "before the cutoff" means.
	now func() time.Time
	log *slog.Logger
}

// NewTimeScannerWithClock is NewTimeScanner with an explicit clock — the
// fixed-clock fixture a firing-set test pins.
func NewTimeScannerWithClock(engine *WorkflowEngine, scan ActivityScan, now func() time.Time, log *slog.Logger) *TimeScanner {
	return &TimeScanner{engine: engine, scan: scan, now: now, log: log}
}

// NewTimeScanner wires the scanner over the real clock for production use
// (compose/timescan.go).
func NewTimeScanner(engine *WorkflowEngine, scan ActivityScan, log *slog.Logger) *TimeScanner {
	return NewTimeScannerWithClock(engine, scan, time.Now, log)
}

// ScanWorkspace is one pass over the workspace already bound in ctx: it loads
// that workspace's enabled clock automations and, for each instance whose
// handler has an ActivityScan enumerator wired (activityScanHandlers above),
// converges its stale candidates onto runOne. Re-entrant, not exactly-once —
// the occurrence key (IdempotencyKey, handlers_clock.go) is what makes a
// redelivered or overlapping pass over the SAME anchor a no-op.
//
// The fleet fan-out lives in the job layer, so a workspace whose pass fails
// fails its own job row rather than becoming a log line inside a run River
// recorded as completed.
func (s *TimeScanner) ScanWorkspace(ctx context.Context, wsID ids.UUID) error {
	now := s.now()
	wsCtx := principal.WithActor(ctx, principal.Principal{Type: principal.PrincipalSystem, ID: "system:time-scan"})
	wsCtx = principal.WithCorrelationID(wsCtx, ids.NewV7())

	instances, err := s.engine.liveInstances(wsCtx)
	if err != nil {
		return fmt.Errorf("loading clock automation instances: %w", err)
	}
	for _, h := range s.engine.clockHandlers() {
		daysFor, ok := activityScanHandlers[h.Spec().Name]
		if !ok {
			continue
		}
		for _, inst := range instances[h.Spec().Name] {
			if err := scanInstanceCandidates(wsCtx, s.scan, h, inst, wsID, now, s.engine.runOne, daysFor); err != nil {
				return fmt.Errorf("%s instance %s: %w", h.Spec().Name, inst.id, err)
			}
		}
	}
	return nil
}

// scanInstanceCandidates is one automation instance's body: derive its
// N-day cutoff via the caller's own days reader, draw stale candidates
// through the ActivityScan seam, and hand each one to run (production:
// engine.runOne; a unit test substitutes a recording stub so the
// event-synthesis contract below is provable without a workspace
// transaction). Factored out as a free function — rather than a
// TimeScanner method — for exactly that substitution.
func scanInstanceCandidates(
	ctx context.Context,
	scan ActivityScan,
	h workflow.Handler,
	inst automationInstance,
	wsID ids.UUID,
	now time.Time,
	run func(ctx context.Context, h workflow.Handler, ev workflow.Event) error,
	daysFor clockDaysExtractor,
) error {
	days, err := daysFor(inst.params)
	if err != nil {
		return err
	}
	cutoff := now.AddDate(0, 0, -days)
	candidates, err := scan.LastTouchBefore(ctx, cutoff, clockScanBatchLimit)
	if err != nil {
		return fmt.Errorf("scanning stale entities: %w", err)
	}
	for _, cand := range candidates {
		ev, err := buildActivityAnchorEvent(wsID, now, inst, cand)
		if err != nil {
			return err
		}
		if err := run(ctx, h, ev); err != nil {
			return err
		}
	}
	return nil
}

// buildActivityAnchorEvent synthesizes the workflow.Event one stale
// candidate fires with — the occurrence-key contract (Task 12,
// occurrence_test.go), shared by both ActivityScan-driven handlers: ID is
// a FRESH ids.NewV7() every call (trigger_event is NOT NULL and is pure
// per-pass provenance, engine_run.go's claimRun doc — never the
// dedupe key), while the anchor rides Payload so the handler's own
// IdempotencyKey (handlers_clock.go's anchorIdempotencyKey)
// can derive the REAL dedupe key from it instead.
func buildActivityAnchorEvent(wsID ids.UUID, now time.Time, inst automationInstance, cand EntityAnchor) (workflow.Event, error) {
	payload, err := json.Marshal(touchAnchorPayload{LastActivityAt: cand.Anchor})
	if err != nil {
		return workflow.Event{}, fmt.Errorf("automation: encoding the last-touch anchor: %w", err)
	}
	return workflow.Event{
		ID:           ids.NewV7(),
		WorkspaceID:  wsID,
		OccurredAt:   now,
		Entity:       cand.Ref,
		AutomationID: inst.id.UUID,
		OwnerID:      inst.owner,
		Params:       inst.params,
		Payload:      payload,
	}, nil
}
