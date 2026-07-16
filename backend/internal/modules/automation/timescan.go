// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package automation

// The CLOCK-trigger entry point (Task 14): event triggers reach runOne
// off the bus (workflows.go's HandleEvent); a clock trigger has no event
// to arrive, so TimeScanner enumerates candidates itself and converges
// them onto the SAME runOne (workflows_run.go) — the Task-12 occurrence
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

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/workflow"
)

// noActivityBatchLimit bounds how many stale candidates one instance's
// pass draws per tick — the same fleet-pass-cap reasoning
// closedatesweep.go's closeDateBatch documents (a migrated backlog drains
// over successive ticks rather than blocking the pass).
const noActivityBatchLimit = 200

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

// Scan is one pass over every live workspace, converging every clock
// automation instance's stale candidates onto runOne. Re-entrant, not
// exactly-once: the occurrence key (IdempotencyKey, workflows_clock_handlers.go)
// is what makes a redelivered or overlapping pass over the SAME anchor a
// no-op, not this method's own bookkeeping.
func (s *TimeScanner) Scan(ctx context.Context) error {
	now := s.now()
	workspaces, err := s.enumerateWorkspaces(ctx)
	if err != nil {
		return err
	}
	scanWorkspaces(workspaces, func(wsID ids.UUID) error {
		return s.scanWorkspace(ctx, wsID, now)
	}, s.log)
	return nil
}

// enumerateWorkspaces is the fleet-wide read closedatesweep.go's Sweep
// also opens with: the workspace table is not itself workspace-scoped, so
// this one query legitimately addresses the pool directly, before any
// per-workspace transaction exists to scope inside.
func (s *TimeScanner) enumerateWorkspaces(ctx context.Context) ([]ids.UUID, error) {
	// rls-exempt: fleet enumeration — the workspace table is not workspace-scoped; this reads every tenant before entering a per-workspace tx.
	rows, err := s.engine.pool.Query(ctx, `SELECT id FROM workspace WHERE archived_at IS NULL ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[ids.UUID])
}

// scanWorkspaces is Scan's per-workspace loop, factored out as a pure
// function (no pool, no engine) so a DB-free unit test can drive it
// directly over a fixed workspace list and a stub per-workspace body:
// the load-bearing behavior under test — one workspace's failure is
// logged and never aborts the pass — needs no database to prove.
func scanWorkspaces(workspaces []ids.UUID, scanOne func(wsID ids.UUID) error, log *slog.Logger) {
	for _, wsID := range workspaces {
		if err := scanOne(wsID); err != nil {
			log.Error("time-scan: workspace pass failed", "workspace", wsID, "err", err)
		}
	}
}

// scanWorkspace loads one workspace's enabled clock automations and, for
// each no_activity_reminder instance, converges its stale candidates onto
// runOne. Task 14b's clock handlers (check_in_cadence, renewal_reminder)
// ride a different candidate seam than ActivityScan, so they are skipped
// here rather than mishandled — this task wires exactly one.
func (s *TimeScanner) scanWorkspace(ctx context.Context, wsID ids.UUID, now time.Time) error {
	wsCtx := principal.WithWorkspaceID(ctx, wsID)
	wsCtx = principal.WithActor(wsCtx, principal.Principal{Type: principal.PrincipalSystem, ID: "system:time-scan"})
	wsCtx = principal.WithCorrelationID(wsCtx, ids.NewV7())

	instances, err := s.engine.liveInstances(wsCtx)
	if err != nil {
		return fmt.Errorf("loading clock automation instances: %w", err)
	}
	for _, h := range s.engine.clockHandlers() {
		if h.Spec().Name != noActivityReminderName {
			continue
		}
		for _, inst := range instances[h.Spec().Name] {
			if err := scanInstanceCandidates(wsCtx, s.scan, h, inst, wsID, now, s.engine.runOne); err != nil {
				return fmt.Errorf("no_activity_reminder instance %s: %w", inst.id, err)
			}
		}
	}
	return nil
}

// scanInstanceCandidates is one automation instance's body: derive its
// N-day cutoff, draw stale candidates through the ActivityScan seam, and
// hand each one to run (production: engine.runOne; a unit test substitutes
// a recording stub so the event-synthesis contract below is provable
// without a workspace transaction). Factored out as a free function —
// rather than a TimeScanner method — for exactly that substitution.
func scanInstanceCandidates(
	ctx context.Context,
	scan ActivityScan,
	h workflow.Handler,
	inst automationInstance,
	wsID ids.UUID,
	now time.Time,
	run func(ctx context.Context, h workflow.Handler, ev workflow.Event) error,
) error {
	days, err := noActivityDays(inst.params)
	if err != nil {
		return err
	}
	cutoff := now.AddDate(0, 0, -days)
	candidates, err := scan.LastTouchBefore(ctx, cutoff, noActivityBatchLimit)
	if err != nil {
		return fmt.Errorf("scanning stale entities: %w", err)
	}
	for _, cand := range candidates {
		ev, err := buildNoActivityEvent(wsID, now, inst, cand)
		if err != nil {
			return err
		}
		if err := run(ctx, h, ev); err != nil {
			return err
		}
	}
	return nil
}

// buildNoActivityEvent synthesizes the workflow.Event one stale candidate
// fires with — the occurrence-key contract (Task 12, occurrence_test.go):
// ID is a FRESH ids.NewV7() every call (trigger_event is NOT NULL and is
// pure per-pass provenance, workflows_run.go's claimRun doc — never the
// dedupe key), while the anchor rides Payload so
// noActivityReminder.IdempotencyKey (workflows_clock_handlers.go) can
// derive the REAL dedupe key from it instead.
func buildNoActivityEvent(wsID ids.UUID, now time.Time, inst automationInstance, cand EntityAnchor) (workflow.Event, error) {
	payload, err := json.Marshal(noActivityAnchorPayload{LastActivityAt: cand.Anchor})
	if err != nil {
		return workflow.Event{}, fmt.Errorf("automation: encoding the no_activity anchor: %w", err)
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
