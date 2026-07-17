// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package automation

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
	"github.com/gradionhq/margince/backend/internal/shared/ports/workflow"
)

// touchEvent builds one ActivityScan-driven clock event the way
// timescan.go's buildActivityAnchorEvent would, for a fixed "now" and
// last-touch anchor — the shape both no_activity_reminder's and
// check_in_cadence's Match/Plan/IdempotencyKey decode identically (they
// differ only in which params key names their own cadence).
func touchEvent(t *testing.T, now, anchor time.Time, entity datasource.EntityRef) workflow.Event {
	t.Helper()
	payload, err := json.Marshal(touchAnchorPayload{LastActivityAt: anchor})
	if err != nil {
		t.Fatalf("encoding anchor payload: %v", err)
	}
	return workflow.Event{
		ID:          ids.NewV7(),
		WorkspaceID: ids.NewV7(),
		OccurredAt:  now,
		Entity:      entity,
		Payload:     payload,
	}
}

// renewalEvent builds one renewal_reminder clock event carrying a
// renewal-date anchor — the shape Match/Plan/IdempotencyKey decode.
// Nothing in production builds this yet (handlers_clock.go's
// renewalReminder doc explains why); this is the same "prove the
// contract directly" posture touchEvent already exercises for the two
// ActivityScan handlers.
func renewalEvent(t *testing.T, now, renewalDate time.Time, entity datasource.EntityRef) workflow.Event {
	t.Helper()
	payload, err := json.Marshal(renewalAnchorPayload{RenewalDate: renewalDate})
	if err != nil {
		t.Fatalf("encoding renewal anchor payload: %v", err)
	}
	return workflow.Event{
		ID:          ids.NewV7(),
		WorkspaceID: ids.NewV7(),
		OccurredAt:  now,
		Entity:      entity,
		Payload:     payload,
	}
}

// TestNoActivityReminderMatchFlipsAtTheCutoff proves Match is the exact
// re-check the coarse scan only approximates: an anchor strictly before
// now-N-days matches; an anchor at or after it does not — the precise
// decision no_activity_reminder's own Match doc promises, over the
// default 7-day threshold (no params, defaultNoActivityDays).
func TestNoActivityReminderMatchFlipsAtTheCutoff(t *testing.T) {
	now := time.Date(2026, 7, 16, 9, 0, 0, 0, time.UTC)
	entity := datasource.EntityRef{Type: datasource.EntityDeal, ID: ids.NewV7()}

	stale := now.AddDate(0, 0, -defaultNoActivityDays-1)
	ev := touchEvent(t, now, stale, entity)
	matched, err := (noActivityReminder{}).Match(context.Background(), ev)
	if err != nil {
		t.Fatalf("Match on a stale anchor: %v", err)
	}
	if !matched {
		t.Errorf("Match(anchor=%s, now=%s) = false, want true — the anchor is older than the %d-day default", stale, now, defaultNoActivityDays)
	}

	fresh := now.AddDate(0, 0, -defaultNoActivityDays+1)
	ev = touchEvent(t, now, fresh, entity)
	matched, err = (noActivityReminder{}).Match(context.Background(), ev)
	if err != nil {
		t.Fatalf("Match on a fresh anchor: %v", err)
	}
	if matched {
		t.Errorf("Match(anchor=%s, now=%s) = true, want false — the anchor is inside the %d-day default", fresh, now, defaultNoActivityDays)
	}
}

// TestNoActivityReminderMatchHonorsInstanceParams proves Match re-derives
// N from ev.Params rather than a hardcoded default: an anchor that is
// stale under the default 7 days but fresh under an instance's own
// explicit 30-day setting must not match.
func TestNoActivityReminderMatchHonorsInstanceParams(t *testing.T) {
	now := time.Date(2026, 7, 16, 9, 0, 0, 0, time.UTC)
	anchor := now.AddDate(0, 0, -10) // stale by the 7-day default, fresh under 30
	ev := touchEvent(t, now, anchor, datasource.EntityRef{Type: datasource.EntityDeal, ID: ids.NewV7()})
	ev.Params = json.RawMessage(`{"no_activity_days": 30}`)

	matched, err := (noActivityReminder{}).Match(context.Background(), ev)
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	if matched {
		t.Error("Match honored the default 7 days instead of the instance's own 30-day param")
	}
}

// TestNoActivityReminderMatchWithNoAnchorErrors proves a hand-built event
// with no anchor payload is a wiring bug surfaced loudly, never a silent
// false — every real caller (timescan.go) always sets Payload.
func TestNoActivityReminderMatchWithNoAnchorErrors(t *testing.T) {
	ev := workflow.Event{OccurredAt: time.Now()}
	if _, err := (noActivityReminder{}).Match(context.Background(), ev); err == nil {
		t.Error("Match on an event with no anchor payload returned no error, want errNoActivityAnchorMissing")
	}
}

// TestNoActivityReminderPlanEmitsOneCreateTask proves Plan emits exactly
// one create_task action anchored on the fired entity, naming the anchor
// in the subject (P6: no mystery number) and carrying the entity's own
// type/id in the links payload rather than a hardcoded "deal".
func TestNoActivityReminderPlanEmitsOneCreateTask(t *testing.T) {
	now := time.Date(2026, 7, 16, 9, 0, 0, 0, time.UTC)
	anchor := now.AddDate(0, 0, -10)
	entity := datasource.EntityRef{Type: datasource.EntityLead, ID: ids.NewV7()}
	ev := touchEvent(t, now, anchor, entity)

	eff, err := (noActivityReminder{}).Plan(context.Background(), ev)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(eff.Actions) != 1 {
		t.Fatalf("Plan emitted %d actions, want exactly 1", len(eff.Actions))
	}
	action := eff.Actions[0]
	if action.Kind != workflow.ActionCreateTask {
		t.Errorf("action kind = %q, want %q", action.Kind, workflow.ActionCreateTask)
	}
	if action.Target != entity {
		t.Errorf("action target = %+v, want the fired entity %+v", action.Target, entity)
	}

	var args struct {
		Kind    string `json:"kind"`
		Subject string `json:"subject"`
		Links   []struct {
			EntityType string   `json:"entity_type"`
			EntityID   ids.UUID `json:"entity_id"`
		} `json:"links"`
	}
	if err := json.Unmarshal(action.Args, &args); err != nil {
		t.Fatalf("decoding action args: %v", err)
	}
	if args.Kind != "task" {
		t.Errorf("args.kind = %q, want task", args.Kind)
	}
	wantAnchor := anchor.Format(time.DateOnly)
	if !strings.Contains(args.Subject, wantAnchor) {
		t.Errorf("subject %q does not name the anchor date %q — the reminder must not be a mystery number", args.Subject, wantAnchor)
	}
	if len(args.Links) != 1 || args.Links[0].EntityType != string(datasource.EntityLead) || args.Links[0].EntityID != entity.ID {
		t.Errorf("args.links = %+v, want one link to %s %s", args.Links, datasource.EntityLead, entity.ID)
	}
}

// TestNoActivityReminderIdempotencyKeyIsAnchorDerived is the occurrence-key
// proof at the handler level (Task 12): two events sharing the SAME anchor
// produce the SAME key even though every other field differs (fresh
// ev.ID, different OccurredAt) — the redundant pass must claim the exact
// same row — while a DIFFERENT anchor produces a DIFFERENT key, so the
// firing re-arms once the entity is touched again and goes quiet a second
// time.
func TestNoActivityReminderIdempotencyKeyIsAnchorDerived(t *testing.T) {
	entity := datasource.EntityRef{Type: datasource.EntityDeal, ID: ids.NewV7()}
	anchor := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)

	first := touchEvent(t, time.Date(2026, 7, 16, 9, 0, 0, 0, time.UTC), anchor, entity)
	second := touchEvent(t, time.Date(2026, 7, 17, 9, 0, 0, 0, time.UTC), anchor, entity) // a later pass, same anchor
	if first.ID == second.ID {
		t.Fatal("the two synthesized events share an ev.ID — this test would not exercise ev.ID-independence")
	}

	h := noActivityReminder{}
	firstKey := h.IdempotencyKey(first)
	secondKey := h.IdempotencyKey(second)
	if firstKey != secondKey {
		t.Errorf("IdempotencyKey differs across two passes over the SAME anchor: %q vs %q — a redelivered pass would mint a new claim instead of hitting the same row", firstKey, secondKey)
	}

	movedAnchor := anchor.AddDate(0, 0, 5)
	third := touchEvent(t, time.Date(2026, 7, 22, 9, 0, 0, 0, time.UTC), movedAnchor, entity)
	thirdKey := h.IdempotencyKey(third)
	if thirdKey == firstKey {
		t.Error("IdempotencyKey did not change when the anchor moved — the trigger would never re-arm after the entity goes quiet a second time")
	}
}

// TestCheckInCadenceMatchFlipsAtTheCutoff proves check_in_cadence's Match
// is the same precise re-check no_activity_reminder's is, over its OWN
// default cadence (defaultCheckInDays, longer than no_activity's 7 days) —
// activityStaleMatch's shared body, exercised through the second handler.
func TestCheckInCadenceMatchFlipsAtTheCutoff(t *testing.T) {
	now := time.Date(2026, 7, 16, 9, 0, 0, 0, time.UTC)
	entity := datasource.EntityRef{Type: datasource.EntityDeal, ID: ids.NewV7()}

	stale := now.AddDate(0, 0, -defaultCheckInDays-1)
	ev := touchEvent(t, now, stale, entity)
	matched, err := (checkInCadence{}).Match(context.Background(), ev)
	if err != nil {
		t.Fatalf("Match on a stale anchor: %v", err)
	}
	if !matched {
		t.Errorf("Match(anchor=%s, now=%s) = false, want true — the anchor is older than the %d-day default", stale, now, defaultCheckInDays)
	}

	fresh := now.AddDate(0, 0, -defaultCheckInDays+1)
	ev = touchEvent(t, now, fresh, entity)
	matched, err = (checkInCadence{}).Match(context.Background(), ev)
	if err != nil {
		t.Fatalf("Match on a fresh anchor: %v", err)
	}
	if matched {
		t.Errorf("Match(anchor=%s, now=%s) = true, want false — the anchor is inside the %d-day default", fresh, now, defaultCheckInDays)
	}
}

// TestCheckInCadenceMatchHonorsItsOwnParamsKey proves check_in_cadence
// reads "check_in_days", NOT no_activity_reminder's "no_activity_days" —
// the two handlers must never read each other's cadence knob by
// accident, even though they share every other piece of machinery.
func TestCheckInCadenceMatchHonorsItsOwnParamsKey(t *testing.T) {
	now := time.Date(2026, 7, 16, 9, 0, 0, 0, time.UTC)
	anchor := now.AddDate(0, 0, -10) // stale under a 7-day cadence, fresh under 30
	entity := datasource.EntityRef{Type: datasource.EntityDeal, ID: ids.NewV7()}

	// no_activity_reminder's own params key does nothing for this handler:
	// check_in_cadence must fall back to its own 30-day default, under
	// which this anchor is still fresh.
	ev := touchEvent(t, now, anchor, entity)
	ev.Params = json.RawMessage(`{"no_activity_days": 1}`)
	matched, err := (checkInCadence{}).Match(context.Background(), ev)
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	if matched {
		t.Error("check_in_cadence honored no_activity_reminder's params key instead of falling back to its own default")
	}

	ev.Params = json.RawMessage(`{"check_in_days": 5}`)
	matched, err = (checkInCadence{}).Match(context.Background(), ev)
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	if !matched {
		t.Error("check_in_cadence did not honor its own check_in_days param")
	}
}

// TestCheckInCadencePlanSaysCheckInNotNoActivity proves the two handlers'
// Plan bodies diverge in exactly the one place they should — the wording —
// while sharing the identical create_task/links shape
// (anchorReminderTaskEffect).
func TestCheckInCadencePlanSaysCheckInNotNoActivity(t *testing.T) {
	now := time.Date(2026, 7, 16, 9, 0, 0, 0, time.UTC)
	anchor := now.AddDate(0, 0, -10)
	entity := datasource.EntityRef{Type: datasource.EntityDeal, ID: ids.NewV7()}
	ev := touchEvent(t, now, anchor, entity)

	eff, err := (checkInCadence{}).Plan(context.Background(), ev)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	var args struct {
		Subject string `json:"subject"`
	}
	if err := json.Unmarshal(eff.Actions[0].Args, &args); err != nil {
		t.Fatalf("decoding action args: %v", err)
	}
	if strings.Contains(args.Subject, "no activity") {
		t.Errorf("subject %q reads like no_activity_reminder's wording, want check_in_cadence's own", args.Subject)
	}
	wantAnchor := anchor.Format(time.DateOnly)
	if !strings.Contains(args.Subject, wantAnchor) {
		t.Errorf("subject %q does not name the anchor date %q", args.Subject, wantAnchor)
	}
}

// TestCheckInCadenceIdempotencyKeyDoesNotCollideWithNoActivityReminder
// proves the two ActivityScan handlers keying off the IDENTICAL anchor
// for the IDENTICAL entity still claim DIFFERENT workflow_run rows — two
// catalog entries over one read, never one shared claim.
func TestCheckInCadenceIdempotencyKeyDoesNotCollideWithNoActivityReminder(t *testing.T) {
	entity := datasource.EntityRef{Type: datasource.EntityDeal, ID: ids.NewV7()}
	anchor := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	ev := touchEvent(t, time.Date(2026, 7, 16, 9, 0, 0, 0, time.UTC), anchor, entity)

	noActivityKey := (noActivityReminder{}).IdempotencyKey(ev)
	checkInKey := (checkInCadence{}).IdempotencyKey(ev)
	if noActivityKey == checkInKey {
		t.Errorf("no_activity_reminder and check_in_cadence produced the SAME IdempotencyKey (%q) over the same anchor and entity — they would collide onto one workflow_run row", noActivityKey)
	}
}

// TestCheckInCadenceIdempotencyKeyIsAnchorDerived is check_in_cadence's own
// occurrence-key proof, mirroring no_activity_reminder's.
func TestCheckInCadenceIdempotencyKeyIsAnchorDerived(t *testing.T) {
	entity := datasource.EntityRef{Type: datasource.EntityDeal, ID: ids.NewV7()}
	anchor := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)

	first := touchEvent(t, time.Date(2026, 7, 16, 9, 0, 0, 0, time.UTC), anchor, entity)
	second := touchEvent(t, time.Date(2026, 7, 17, 9, 0, 0, 0, time.UTC), anchor, entity)

	h := checkInCadence{}
	if h.IdempotencyKey(first) != h.IdempotencyKey(second) {
		t.Errorf("IdempotencyKey differs across two passes over the SAME anchor: %q vs %q", h.IdempotencyKey(first), h.IdempotencyKey(second))
	}

	movedAnchor := anchor.AddDate(0, 0, 5)
	third := touchEvent(t, time.Date(2026, 7, 22, 9, 0, 0, 0, time.UTC), movedAnchor, entity)
	if h.IdempotencyKey(third) == h.IdempotencyKey(first) {
		t.Error("IdempotencyKey did not change when the anchor moved — the trigger would never re-arm after the entity goes quiet a second time")
	}
}

// TestRenewalReminderMatchWithinTheApproachingWindow proves the [now,
// now+N] window: a renewal date already past (overdue) does not match —
// that is task_overdue's trigger, not this one's — and a date further out
// than N days is not yet "approaching"; only a date inside the window
// matches.
func TestRenewalReminderMatchWithinTheApproachingWindow(t *testing.T) {
	now := time.Date(2026, 7, 16, 9, 0, 0, 0, time.UTC)
	entity := datasource.EntityRef{Type: datasource.EntityDeal, ID: ids.NewV7()}

	cases := []struct {
		name        string
		renewalDate time.Time
		wantMatch   bool
	}{
		{"already overdue", now.AddDate(0, 0, -1), false},
		{"exactly now", now, true},
		{"inside the default 30-day horizon", now.AddDate(0, 0, defaultRenewalDaysBefore-1), true},
		{"exactly at the horizon", now.AddDate(0, 0, defaultRenewalDaysBefore), true},
		{"beyond the horizon", now.AddDate(0, 0, defaultRenewalDaysBefore+1), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ev := renewalEvent(t, now, tc.renewalDate, entity)
			matched, err := (renewalReminder{}).Match(context.Background(), ev)
			if err != nil {
				t.Fatalf("Match: %v", err)
			}
			if matched != tc.wantMatch {
				t.Errorf("Match(renewal=%s, now=%s) = %v, want %v", tc.renewalDate, now, matched, tc.wantMatch)
			}
		})
	}
}

// TestRenewalReminderMatchHonorsInstanceParams proves Match re-derives
// its horizon from ev.Params's own "days_before" key rather than a
// hardcoded default.
func TestRenewalReminderMatchHonorsInstanceParams(t *testing.T) {
	now := time.Date(2026, 7, 16, 9, 0, 0, 0, time.UTC)
	renewalDate := now.AddDate(0, 0, 10) // inside the 30-day default, outside a 5-day setting
	entity := datasource.EntityRef{Type: datasource.EntityDeal, ID: ids.NewV7()}
	ev := renewalEvent(t, now, renewalDate, entity)
	ev.Params = json.RawMessage(`{"days_before": 5}`)

	matched, err := (renewalReminder{}).Match(context.Background(), ev)
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	if matched {
		t.Error("Match honored the 30-day default instead of the instance's own 5-day days_before param")
	}
}

// TestRenewalReminderMatchWithNoAnchorErrors mirrors
// TestNoActivityReminderMatchWithNoAnchorErrors: a hand-built event with
// no renewal-date payload is a wiring bug, never a silent false.
func TestRenewalReminderMatchWithNoAnchorErrors(t *testing.T) {
	ev := workflow.Event{OccurredAt: time.Now()}
	if _, err := (renewalReminder{}).Match(context.Background(), ev); err == nil {
		t.Error("Match on an event with no renewal-date anchor payload returned no error, want errRenewalAnchorMissing")
	}
}

// TestRenewalReminderPlanNamesTheRenewalDate proves Plan emits one
// create_task action naming the renewal date in the subject (P6: no
// mystery number).
func TestRenewalReminderPlanNamesTheRenewalDate(t *testing.T) {
	now := time.Date(2026, 7, 16, 9, 0, 0, 0, time.UTC)
	renewalDate := now.AddDate(0, 0, 10)
	entity := datasource.EntityRef{Type: datasource.EntityDeal, ID: ids.NewV7()}
	ev := renewalEvent(t, now, renewalDate, entity)

	eff, err := (renewalReminder{}).Plan(context.Background(), ev)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(eff.Actions) != 1 {
		t.Fatalf("Plan emitted %d actions, want exactly 1", len(eff.Actions))
	}
	action := eff.Actions[0]
	if action.Kind != workflow.ActionCreateTask {
		t.Errorf("action kind = %q, want %q", action.Kind, workflow.ActionCreateTask)
	}
	if action.Target != entity {
		t.Errorf("action target = %+v, want the fired entity %+v", action.Target, entity)
	}
	var args struct {
		Subject string `json:"subject"`
	}
	if err := json.Unmarshal(action.Args, &args); err != nil {
		t.Fatalf("decoding action args: %v", err)
	}
	wantDate := renewalDate.Format(time.DateOnly)
	if !strings.Contains(args.Subject, wantDate) {
		t.Errorf("subject %q does not name the renewal date %q — the reminder must not be a mystery number", args.Subject, wantDate)
	}
}

// TestRenewalReminderIdempotencyKeyIsAnchorDerived proves the occurrence-key
// contract at the renewal-date anchor: the SAME renewal date claims the
// SAME row across passes, and a CHANGED renewal date (the record was
// renewed to a new date) re-arms the trigger.
func TestRenewalReminderIdempotencyKeyIsAnchorDerived(t *testing.T) {
	entity := datasource.EntityRef{Type: datasource.EntityDeal, ID: ids.NewV7()}
	renewalDate := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)

	first := renewalEvent(t, time.Date(2026, 7, 16, 9, 0, 0, 0, time.UTC), renewalDate, entity)
	second := renewalEvent(t, time.Date(2026, 7, 17, 9, 0, 0, 0, time.UTC), renewalDate, entity)
	if first.ID == second.ID {
		t.Fatal("the two synthesized events share an ev.ID — this test would not exercise ev.ID-independence")
	}

	h := renewalReminder{}
	firstKey := h.IdempotencyKey(first)
	secondKey := h.IdempotencyKey(second)
	if firstKey != secondKey {
		t.Errorf("IdempotencyKey differs across two passes over the SAME renewal date: %q vs %q", firstKey, secondKey)
	}

	movedDate := renewalDate.AddDate(1, 0, 0) // renewed a year out
	third := renewalEvent(t, time.Date(2027, 7, 1, 9, 0, 0, 0, time.UTC), movedDate, entity)
	thirdKey := h.IdempotencyKey(third)
	if thirdKey == firstKey {
		t.Error("IdempotencyKey did not change when the renewal date moved — a re-renewed record would never re-arm the trigger")
	}
}
