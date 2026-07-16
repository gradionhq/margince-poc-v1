// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package automation

// The clock-triggered starters (Task 14a/14b, features/10 §1's
// no_activity_for_n_days and date_field_approaching / UC-E15-01's own
// worked example, "remind me if a deal I own has no activity for N
// days"). Unlike the event starters (workflows_starter.go,
// workflows_event_handlers.go), a clock handler has no event to read
// Match's decision off: TimeScanner (timescan.go) is a coarse SQL
// pre-filter, and Match here is the PRECISE re-check every clock handler
// owes (occurrence_test.go's convention) — re-deriving the same cutoff
// from ev.Params and confirming the anchor the scan carried still
// crosses it as of this event's own OccurredAt.
//
// Three handlers share this file because they share machinery, not
// because they are one concept: no_activity_reminder and
// check_in_cadence are both "quiet spell" triggers over the IDENTICAL
// ActivityScan read (activities/lasttouch.go's source='system'
// exclusion applies to both alike) and differ only in which params key
// names their own cadence and what their own reminder says — the shared
// anchor helpers below (touchAnchor, activityStaleMatch,
// anchorReminderTaskEffect, anchorIdempotencyKey) exist so that
// difference is the ONLY thing their Match/Plan/IdempotencyKey bodies
// spell out. renewal_reminder rides a different anchor (a custom
// renewal-date field's value, not a last-touch timestamp) and its own
// doc below explains why TimeScanner has no candidate source wired for
// it yet.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
	"github.com/gradionhq/margince/backend/internal/shared/ports/workflow"
)

// noActivityReminderName is the catalog key Task 6 seeds this starter
// under — CatalogEntry.Key must equal the backing handler's Spec().Name,
// one vocabulary across the catalog, the engine, and run records
// (automations_catalog.go's CatalogEntry doc).
const noActivityReminderName = "no_activity_reminder"

// noActivityScheduleMarker is Trigger.Schedule's value. RegisterWorkflow
// (workflows.go) only requires Schedule to be non-empty — that non-empty-
// ness is the marker isClockTrigger (workflows_run.go) routes on to reach
// the time-scan instead of the bus. The actual cadence is the River
// periodic job's own interval (compose/jobs.go's TimeScanArgs), so this
// string documents intent for a human reading the registry; it is never
// parsed as a cron expression.
const noActivityScheduleMarker = "clock:no_activity_scan"

// defaultNoActivityDays is the fallback "how many quiet days" threshold —
// UC-E15-01's own worked example sets N=7 when a user turns this on.
const defaultNoActivityDays = 7

// noActivityDays reads the "how many quiet days" knob off an automation
// instance's params. Both TimeScanner.Scan (timescan.go, which needs N to
// build its SQL cutoff for the coarse pre-filter) and this handler's own
// Match (the precise recheck) call this SAME function — one reader, so
// the coarse scan and the exact decision can never drift onto two
// different thresholds for the identical instance.
func noActivityDays(params json.RawMessage) (int, error) {
	if len(params) == 0 {
		return defaultNoActivityDays, nil
	}
	var decoded struct {
		NoActivityDays *int `json:"no_activity_days"`
	}
	if err := json.Unmarshal(params, &decoded); err != nil {
		return 0, fmt.Errorf("automation: no_activity_reminder params: %w", err)
	}
	if decoded.NoActivityDays == nil {
		return defaultNoActivityDays, nil
	}
	return *decoded.NoActivityDays, nil
}

// touchAnchorPayload is the wire shape the last-genuine-touch anchor
// rides in workflow.Event.Payload for one ActivityScan-driven clock pass
// (timescan.go's buildActivityAnchorEvent writes it; touchAnchor below
// reads it back) — shared by both no_activity_reminder and
// check_in_cadence, since both fire off the SAME "quiet since" moment.
// The anchor lives in idempotency_key, NOT trigger_event (workflows_run.go's
// claimRun doc: trigger_event is a fresh per-pass id, pure provenance) —
// this is what makes a firing re-arm exactly when the entity's last touch
// moves and stay quiet while it doesn't (Task 12's occurrence-key
// contract, occurrence_test.go).
type touchAnchorPayload struct {
	LastActivityAt time.Time `json:"last_activity_at"`
}

// errTouchAnchorMissing is a wiring bug, not a routine non-match: every
// event either ActivityScan-driven handler ever sees was built by
// timescan.go's buildActivityAnchorEvent, which always sets Payload. A
// caller that skipped that path (a hand-built event in a test, say) gets
// a loud error rather than a silently-false Match.
var errTouchAnchorMissing = errors.New("automation: clock handler: event carries no last-touch anchor payload")

// touchAnchor decodes the stale-since anchor a clock pass carried in
// ev.Payload — the one reader every ActivityScan-driven handler's Match,
// Plan, and IdempotencyKey share.
func touchAnchor(ev workflow.Event) (time.Time, error) {
	if len(ev.Payload) == 0 {
		return time.Time{}, errTouchAnchorMissing
	}
	var payload touchAnchorPayload
	if err := json.Unmarshal(ev.Payload, &payload); err != nil {
		return time.Time{}, fmt.Errorf("automation: decoding the last-touch anchor: %w", err)
	}
	return payload.LastActivityAt, nil
}

// clockDaysExtractor reads one clock automation instance's own "how many
// days" cadence knob off its params — the shape every N-days clock
// handler's reader (noActivityDays, checkInCadenceDays, renewalDaysBefore)
// implements. TimeScanner's dispatch (timescan.go's activityScanHandlers)
// keys its enumerator lookup on this same function value per handler
// name, so the coarse SQL pre-filter and each handler's own precise Match
// can never drift onto two different thresholds for one instance.
type clockDaysExtractor func(params json.RawMessage) (int, error)

// activityStaleMatch is the precise re-check both ActivityScan-driven
// clock handlers share: re-derive N from ev.Params via the caller's own
// days reader and confirm the anchor TimeScanner's coarse SQL cutoff
// carried is genuinely before now-N-days as of this event's own
// OccurredAt (the scan's captured-once "now" — occurrence_test.go's
// convention).
func activityStaleMatch(ev workflow.Event, days clockDaysExtractor) (bool, error) {
	anchor, err := touchAnchor(ev)
	if err != nil {
		return false, err
	}
	n, err := days(ev.Params)
	if err != nil {
		return false, err
	}
	cutoff := ev.OccurredAt.AddDate(0, 0, -n)
	return anchor.Before(cutoff), nil
}

// taskCreateEffect is the ONE create_task effect shape every task-minting
// starter plans — the clock reminders here AND the event starters in
// workflows_starter.go (stage_change_create_task, route_lead). A task of
// the given subject, due at dueAt, linked to whatever entity actually
// fired. Sharing the builder keeps the effect's JSON keys
// (task/subject/due_at/links/entity_type/entity_id) in exactly one place,
// so an editor-facing schema change lands once, not in three hand-copied
// maps that could drift.
func taskCreateEffect(ev workflow.Event, subject string, dueAt time.Time) (workflow.Effect, error) {
	args, err := json.Marshal(map[string]any{
		fieldKind: "task",
		"subject": subject,
		"due_at":  dueAt,
		"links": []map[string]any{{
			"entity_type": string(ev.Entity.Type), "entity_id": ev.Entity.ID,
		}},
	})
	if err != nil {
		return workflow.Effect{}, fmt.Errorf("automation: encoding the task: %w", err)
	}
	return workflow.Effect{Actions: []workflow.Action{{
		Kind: workflow.ActionCreateTask, Target: ev.Entity, Args: args,
	}}}, nil
}

// anchorReminderTaskEffect is the clock handlers' view of taskCreateEffect:
// a reminder due AT the anchor moment (ev.OccurredAt), anchored on the
// fired entity, with the caller's own wording — no_activity_reminder,
// check_in_cadence, and renewal_reminder all plan through it.
func anchorReminderTaskEffect(ev workflow.Event, subject string) (workflow.Effect, error) {
	return taskCreateEffect(ev, subject, ev.OccurredAt)
}

// anchorIdempotencyKey is the load-bearing occurrence key (Task 12) every
// anchor-derived clock handler derives its dedupe key from: keyed on the
// ANCHOR, never ev.ID — a fresh time-scan pass mints a new ev.ID every
// tick regardless of whether the anchor actually moved, so keying on it
// would refire the reminder every single pass while the condition stays
// true. Keying on the anchor means the SAME anchor value claims the SAME
// row (claimRun's ON CONFLICT DO NOTHING absorbs every redundant pass),
// and only a NEW anchor re-arms it. anchorErr folds a decode failure into
// the key itself — rather than a fixed placeholder — so a caller that
// skipped Match still can't silently collide two different failures onto
// one claimed row; workflow.Handler's IdempotencyKey has no error return
// (ports/workflow/workflow.go), and every real caller (runOne, reached
// only after Match already decoded this same payload successfully) never
// hits that branch in practice.
func anchorIdempotencyKey(name string, anchor time.Time, anchorErr error) string {
	if anchorErr != nil {
		return name + ":anchor-error:" + anchorErr.Error()
	}
	return name + ":anchor:" + anchor.UTC().Format(time.RFC3339Nano)
}

// noActivityReminder reminds an entity's owner once its most recent
// captured activity has gone quiet for N days — the clock counterpart of
// the event starters, converging on the identical runOne path
// (workflows_run.go) via TimeScanner (timescan.go) rather than the bus.
type noActivityReminder struct {
	ex Executors
}

func (noActivityReminder) Spec() workflow.Spec {
	return workflow.Spec{
		Name:    noActivityReminderName,
		Trigger: workflow.Trigger{Schedule: noActivityScheduleMarker},
		Tier:    mcp.TierGreen,
	}
}

// Match is the precise re-check TimeScanner's SQL cutoff only
// approximates as a coarse pre-filter — activityStaleMatch re-derives N
// from ev.Params via this handler's OWN reader (noActivityDays).
func (noActivityReminder) Match(_ context.Context, ev workflow.Event) (bool, error) {
	return activityStaleMatch(ev, noActivityDays)
}

// Plan mints one create_task reminder anchored on the stale entity,
// naming the anchor in the subject so the reminder is never a mystery
// number (P6).
func (noActivityReminder) Plan(_ context.Context, ev workflow.Event) (workflow.Effect, error) {
	anchor, err := touchAnchor(ev)
	if err != nil {
		return workflow.Effect{}, err
	}
	subject := fmt.Sprintf("Check in — no activity since %s", anchor.Format(time.DateOnly))
	return anchorReminderTaskEffect(ev, subject)
}

func (w noActivityReminder) Apply(ctx context.Context, _ workflow.Event, eff workflow.Effect, _ *workflow.ApprovalToken) (workflow.RunResult, error) {
	applied, err := ApplyActions(ctx, w.ex, eff)
	return workflow.RunResult{Applied: applied}, err
}

// IdempotencyKey is the load-bearing occurrence key (Task 12): derived
// from the touch anchor via anchorIdempotencyKey, never ev.ID — see that
// function's doc for why.
func (noActivityReminder) IdempotencyKey(ev workflow.Event) string {
	anchor, err := touchAnchor(ev)
	return anchorIdempotencyKey(noActivityReminderName, anchor, err)
}

// checkInCadenceName is the catalog key Task 6 seeds this starter under.
const checkInCadenceName = "check_in_cadence"

// checkInCadenceScheduleMarker is Trigger.Schedule's value, documenting
// intent only — see noActivityScheduleMarker's doc for why it is never
// parsed as a cron expression.
const checkInCadenceScheduleMarker = "clock:check_in_scan"

// defaultCheckInDays is check_in_cadence's OWN fallback cadence — longer
// than no_activity_reminder's 7-day default (defaultNoActivityDays) by
// design: "check in periodically regardless" is a longer-horizon habit
// than "flag genuine neglect".
const defaultCheckInDays = 30

// checkInCadenceDays is check_in_cadence's own days-knob reader — the
// same one-reader-for-both-cutoff-and-Match discipline noActivityDays'
// doc describes, keyed on this handler's OWN params field
// (check_in_days) so the two ActivityScan handlers can never read each
// other's cadence by accident.
func checkInCadenceDays(params json.RawMessage) (int, error) {
	if len(params) == 0 {
		return defaultCheckInDays, nil
	}
	var decoded struct {
		CheckInDays *int `json:"check_in_days"`
	}
	if err := json.Unmarshal(params, &decoded); err != nil {
		return 0, fmt.Errorf("automation: check_in_cadence params: %w", err)
	}
	if decoded.CheckInDays == nil {
		return defaultCheckInDays, nil
	}
	return *decoded.CheckInDays, nil
}

// checkInCadence reminds an entity's owner to re-engage once it has gone
// quiet for the automation's OWN (typically longer) cadence — the
// IDENTICAL LastTouchBefore read no_activity_reminder shares
// (activities/lasttouch.go's source='system' exclusion means this
// reminder's own task never resets the very clock it fires off), so it
// fires once per quiet spell rather than nagging every pass. It is a
// second catalog entry over the SAME read, not no_activity_reminder
// wearing a second name: a workspace may enable either independently,
// each with its own N and its own wording ("time for a check-in", not
// "no activity").
type checkInCadence struct {
	ex Executors
}

func (checkInCadence) Spec() workflow.Spec {
	return workflow.Spec{
		Name:    checkInCadenceName,
		Trigger: workflow.Trigger{Schedule: checkInCadenceScheduleMarker},
		Tier:    mcp.TierGreen,
	}
}

func (checkInCadence) Match(_ context.Context, ev workflow.Event) (bool, error) {
	return activityStaleMatch(ev, checkInCadenceDays)
}

func (checkInCadence) Plan(_ context.Context, ev workflow.Event) (workflow.Effect, error) {
	anchor, err := touchAnchor(ev)
	if err != nil {
		return workflow.Effect{}, err
	}
	subject := fmt.Sprintf("Time for a check-in — last touched %s", anchor.Format(time.DateOnly))
	return anchorReminderTaskEffect(ev, subject)
}

func (w checkInCadence) Apply(ctx context.Context, _ workflow.Event, eff workflow.Effect, _ *workflow.ApprovalToken) (workflow.RunResult, error) {
	applied, err := ApplyActions(ctx, w.ex, eff)
	return workflow.RunResult{Applied: applied}, err
}

func (checkInCadence) IdempotencyKey(ev workflow.Event) string {
	anchor, err := touchAnchor(ev)
	return anchorIdempotencyKey(checkInCadenceName, anchor, err)
}

// renewalReminderName is the catalog key Task 6 seeds this starter under.
const renewalReminderName = "renewal_reminder"

// renewalScheduleMarker is Trigger.Schedule's value, documenting intent
// only — see noActivityScheduleMarker's doc for why it is never parsed
// as a cron expression.
const renewalScheduleMarker = "clock:renewal_scan"

// defaultRenewalDaysBefore is the fallback "how many days ahead of the
// renewal date to remind" threshold.
const defaultRenewalDaysBefore = 30

// renewalDaysBefore reads the "how far ahead" knob off an automation
// instance's params — the same one-reader discipline noActivityDays'
// doc describes, so a future candidate enumeration and this handler's
// own Match can never drift onto two different horizons for the same
// instance.
func renewalDaysBefore(params json.RawMessage) (int, error) {
	if len(params) == 0 {
		return defaultRenewalDaysBefore, nil
	}
	var decoded struct {
		DaysBefore *int `json:"days_before"`
	}
	if err := json.Unmarshal(params, &decoded); err != nil {
		return 0, fmt.Errorf("automation: renewal_reminder params: %w", err)
	}
	if decoded.DaysBefore == nil {
		return defaultRenewalDaysBefore, nil
	}
	return *decoded.DaysBefore, nil
}

// renewalAnchorPayload carries the renewal date itself — the anchor
// renewalReminder.IdempotencyKey derives its key from, so the firing
// re-arms exactly when the configured renewal-date field's VALUE changes
// (a contract renewed to a new date is a fresh occurrence to remind
// about), mirroring touchAnchorPayload's role for the two
// ActivityScan-driven handlers above.
type renewalAnchorPayload struct {
	RenewalDate time.Time `json:"renewal_date"`
}

// errRenewalAnchorMissing mirrors errTouchAnchorMissing: a wiring bug,
// not a routine non-match, for whichever caller eventually builds this
// handler's events.
var errRenewalAnchorMissing = errors.New("automation: renewal_reminder: event carries no renewal-date anchor payload")

// renewalAnchor decodes the renewal-date anchor a clock pass carried in
// ev.Payload — the one reader Match, Plan, and IdempotencyKey share.
func renewalAnchor(ev workflow.Event) (time.Time, error) {
	if len(ev.Payload) == 0 {
		return time.Time{}, errRenewalAnchorMissing
	}
	var payload renewalAnchorPayload
	if err := json.Unmarshal(ev.Payload, &payload); err != nil {
		return time.Time{}, fmt.Errorf("automation: decoding the renewal-date anchor: %w", err)
	}
	return payload.RenewalDate, nil
}

// renewalReminder fires when a record's workspace-configured custom
// renewal-date field is approaching — TriggerDateFieldApproaching
// (catalog_triggers.go), over a CUSTOM field (features/10 §3.5, A50)
// rather than a first-class column, in [now, now+N days]: a date already
// past is overdue (task_overdue's own trigger, not this one's), and a
// date further out than N days is not yet "approaching".
//
// Deferred candidate source: TimeScanner has no enumerator wired for
// this handler today (timescan.go's activityScanHandlers carries no
// renewalReminderName entry). Sourcing "which records have this custom
// field's value inside a date range" needs a range query over an
// arbitrary cf_* column, and neither existing cross-module read seam
// reaches that:
//
//   - fieldcatalog.Reader (shared/ports/fieldcatalog) answers CATALOG
//     METADATA only — a column's name and type, never a row's value
//     (its own doc: "a record store has no business with" more than that).
//   - The one facility that DOES run typed range predicates over a
//     record table (collections.SegmentEngine / storekit.Query, the
//     engine compose/filteredexport.go drives) is deliberately closed to
//     a static, compile-time field vocabulary per resource
//     (collections/vocab.go's segmentEngines doc: "Only expressions from
//     this map ever reach the query text") — cf_* columns are excluded
//     by design. Widening that to a per-workspace dynamic column is real
//     customfields/collections engineering, not a thin adapter over an
//     existing seam.
//   - datasource.SystemOfRecordProvider is FROZEN at V1 (its own doc: a
//     new verb needs a versioned V2 interface plus a capability probe),
//     so growing it here is not a smaller lift either.
//
// This is the SAME honest-out-of-scope posture ApplyActions' notify case
// already carries for a workspace with no channel wired
// (ErrNoNotificationTransport, seams.go): the template is seeded
// regardless of whether any workspace has configured a renewal-date
// field (spec §3.5), and a workspace that hasn't simply never surfaces a
// candidate — never a bug, never a fabricated read. Match, Plan, and
// IdempotencyKey below are fully correct against whatever anchor a real
// enumeration would eventually carry, proven directly against hand-built
// payloads exactly like the two ActivityScan handlers' own unit tests.
type renewalReminder struct {
	ex Executors
}

func (renewalReminder) Spec() workflow.Spec {
	return workflow.Spec{
		Name:    renewalReminderName,
		Trigger: workflow.Trigger{Schedule: renewalScheduleMarker},
		Tier:    mcp.TierGreen,
	}
}

func (renewalReminder) Match(_ context.Context, ev workflow.Event) (bool, error) {
	anchor, err := renewalAnchor(ev)
	if err != nil {
		return false, err
	}
	days, err := renewalDaysBefore(ev.Params)
	if err != nil {
		return false, err
	}
	horizon := ev.OccurredAt.AddDate(0, 0, days)
	return !anchor.Before(ev.OccurredAt) && !anchor.After(horizon), nil
}

func (renewalReminder) Plan(_ context.Context, ev workflow.Event) (workflow.Effect, error) {
	anchor, err := renewalAnchor(ev)
	if err != nil {
		return workflow.Effect{}, err
	}
	subject := fmt.Sprintf("Renewal coming up — %s", anchor.Format(time.DateOnly))
	return anchorReminderTaskEffect(ev, subject)
}

func (w renewalReminder) Apply(ctx context.Context, _ workflow.Event, eff workflow.Effect, _ *workflow.ApprovalToken) (workflow.RunResult, error) {
	applied, err := ApplyActions(ctx, w.ex, eff)
	return workflow.RunResult{Applied: applied}, err
}

func (renewalReminder) IdempotencyKey(ev workflow.Event) string {
	anchor, err := renewalAnchor(ev)
	return anchorIdempotencyKey(renewalReminderName, anchor, err)
}
