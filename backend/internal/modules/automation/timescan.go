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
// code that knows about River); a River dispatcher enqueues one
// ScanWorkspace per tenant.
//
// Mirrors deals/closedatesweep.go's CloseDateCorrector.Sweep shape: fleet-
// enumerate workspaces (the rls-exempt marker below), then a per-workspace
// pass whose own failure is logged, never returned, so one bad tenant
// never starves the rest of the fleet.

import (
	"context"
	"encoding/json"
	"errors"
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

// dateFieldScanParams is one instance's DateFieldScan call, read off its
// own params: which (object, column) to watch, how many days ahead
// counts as "approaching", and whether the field recurs yearly.
type dateFieldScanParams struct {
	Object       string
	Column       string
	DaysBefore   int
	RecursYearly bool
}

// dateFieldScanExtractor reads one clock automation instance's own
// DateFieldScan call off its params — the DateFieldScan-driven
// counterpart of clockDaysExtractor above. Handlers_clock.go's own
// param readers (Task 2) grow the same three keys for renewalReminder's
// Match/Plan; this reader exists independently of those so Task 1's
// candidate seam does not have to wait on that later slice.
type dateFieldScanExtractor func(params json.RawMessage) (dateFieldScanParams, error)

// errRenewalScanParamsMissing names the two required keys together: a
// renewal_reminder instance with neither configured has no candidate
// source to draw from at all, which is a save-time misconfiguration this
// scan honestly refuses rather than silently drawing an empty batch
// forever.
var errRenewalScanParamsMissing = errors.New(
	"automation: renewal_reminder requires \"object\" and \"date_field\" params")

// renewalDateFieldScanParams is dateFieldScanHandlers' entry for
// renewalReminderName: object/date_field name the watched cf_* column,
// days_before defaults to defaultRenewalDaysBefore (the same fallback
// renewalDaysBefore uses for the precise Match re-check, handlers_clock.go),
// and recurs_yearly defaults to false — a one-time renewal date unless the
// instance explicitly opts into yearly recurrence.
func renewalDateFieldScanParams(params json.RawMessage) (dateFieldScanParams, error) {
	if len(params) == 0 {
		return dateFieldScanParams{}, errRenewalScanParamsMissing
	}
	var decoded struct {
		Object       string `json:"object"`
		DateField    string `json:"date_field"`
		DaysBefore   *int   `json:"days_before"`
		RecursYearly *bool  `json:"recurs_yearly"`
	}
	if err := json.Unmarshal(params, &decoded); err != nil {
		return dateFieldScanParams{}, fmt.Errorf("automation: renewal_reminder params: %w", err)
	}
	if decoded.Object == "" || decoded.DateField == "" {
		return dateFieldScanParams{}, errRenewalScanParamsMissing
	}
	days := defaultRenewalDaysBefore
	if decoded.DaysBefore != nil {
		days = *decoded.DaysBefore
	}
	var recurring bool
	if decoded.RecursYearly != nil {
		recurring = *decoded.RecursYearly
	}
	return dateFieldScanParams{Object: decoded.Object, Column: decoded.DateField, DaysBefore: days, RecursYearly: recurring}, nil
}

// dateFieldScanHandlers mirrors activityScanHandlers for the
// DateFieldScan-driven clock handlers — today just renewal_reminder,
// whose candidate source used to be entirely deferred (handlers_clock.go's
// renewalReminder doc). A handler absent from BOTH this map and
// activityScanHandlers has no candidate source wired at all and
// ScanWorkspace skips it honestly.
var dateFieldScanHandlers = map[string]dateFieldScanExtractor{
	renewalReminderName: renewalDateFieldScanParams,
}

// TimeScanner drives every CLOCK-triggered automation instance: it holds
// the WorkflowEngine so it can call e.runOne (same package), the
// ActivityScan seam so no_activity_reminder/check_in_cadence's
// candidates are read from activities' own tables, and the DateFieldScan
// seam so renewal_reminder's are read from customfields' own tables —
// never a direct query against either sibling's.
type TimeScanner struct {
	engine   *WorkflowEngine
	scan     ActivityScan
	dateScan DateFieldScan
	// now is the scanner's clock (the quotas.NewStoreWithClock spelling —
	// there is no Clock interface in this repo): captured ONCE per Scan
	// call so every workspace and every instance in one pass agrees on
	// what "before the cutoff" means.
	now func() time.Time
	log *slog.Logger
}

// NewTimeScannerWithClock is NewTimeScanner with an explicit clock — the
// fixed-clock fixture a firing-set test pins.
func NewTimeScannerWithClock(engine *WorkflowEngine, scan ActivityScan, dateScan DateFieldScan, now func() time.Time, log *slog.Logger) *TimeScanner {
	return &TimeScanner{engine: engine, scan: scan, dateScan: dateScan, now: now, log: log}
}

// NewTimeScanner wires the scanner over the real clock for production use
// (compose/timescan.go).
func NewTimeScanner(engine *WorkflowEngine, scan ActivityScan, dateScan DateFieldScan, log *slog.Logger) *TimeScanner {
	return NewTimeScannerWithClock(engine, scan, dateScan, time.Now, log)
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
		name := h.Spec().Name
		if daysFor, ok := activityScanHandlers[name]; ok {
			for _, inst := range instances[name] {
				if err := scanInstanceCandidates(wsCtx, s.scan, h, inst, wsID, now, s.engine.runOne, daysFor); err != nil {
					return fmt.Errorf("%s instance %s: %w", name, inst.id, err)
				}
			}
			continue
		}
		if paramsFor, ok := dateFieldScanHandlers[name]; ok {
			for _, inst := range instances[name] {
				if err := scanDateFieldInstanceCandidates(wsCtx, s.dateScan, h, inst, wsID, now, s.engine.runOne, paramsFor); err != nil {
					return fmt.Errorf("%s instance %s: %w", name, inst.id, err)
				}
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

// scanDateFieldInstanceCandidates is scanInstanceCandidates' DateFieldScan
// counterpart: derive the instance's own (object, column, days_before,
// recurs_yearly) via paramsFor, draw candidates whose watched field falls
// in [now, now+days_before] through the DateFieldScan seam, and hand each
// one to run — same production/test substitution reasoning as
// scanInstanceCandidates (a free function so a unit test can pass a
// recording stub in place of engine.runOne).
//
// An instance that has never been given its own object/date_field
// (errRenewalScanParamsMissing) is skipped rather than failed: the
// template is seeded regardless of whether a workspace has configured
// it yet (renewalReminder's own doc), the identical honest-out-of-scope
// posture ApplyActions' notify case already carries for a workspace with
// no channel wired. A malformed params blob is a real error and still
// propagates — only "not configured yet" is the sanctioned no-op.
func scanDateFieldInstanceCandidates(
	ctx context.Context,
	scan DateFieldScan,
	h workflow.Handler,
	inst automationInstance,
	wsID ids.UUID,
	now time.Time,
	run func(ctx context.Context, h workflow.Handler, ev workflow.Event) error,
	paramsFor dateFieldScanExtractor,
) error {
	p, err := paramsFor(inst.params)
	if err != nil {
		if errors.Is(err, errRenewalScanParamsMissing) {
			return nil
		}
		return err
	}
	horizon := now.AddDate(0, 0, p.DaysBefore)
	candidates, err := scan.Candidates(ctx, p.Object, p.Column, now, horizon, p.RecursYearly, clockScanBatchLimit)
	if err != nil {
		return fmt.Errorf("scanning date-field candidates: %w", err)
	}
	for _, cand := range candidates {
		ev, err := buildDateFieldAnchorEvent(wsID, now, inst, cand)
		if err != nil {
			return err
		}
		if err := run(ctx, h, ev); err != nil {
			return err
		}
	}
	return nil
}

// buildDateFieldAnchorEvent is buildActivityAnchorEvent's DateFieldScan
// counterpart: the same fresh-ID/anchor-in-Payload occurrence-key
// contract, but reusing renewalAnchorPayload (handlers_clock.go) as the
// wire shape so renewalReminder's existing Match/Plan/IdempotencyKey
// (unchanged by this seam) read it back via the identical renewalAnchor
// decoder they already have.
func buildDateFieldAnchorEvent(wsID ids.UUID, now time.Time, inst automationInstance, cand DateFieldAnchor) (workflow.Event, error) {
	payload, err := json.Marshal(renewalAnchorPayload{RenewalDate: cand.Anchor})
	if err != nil {
		return workflow.Event{}, fmt.Errorf("automation: encoding the renewal-date anchor: %w", err)
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
