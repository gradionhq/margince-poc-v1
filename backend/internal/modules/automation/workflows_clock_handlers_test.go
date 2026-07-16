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

// noActivityEvent builds one no_activity_reminder clock event the way
// timescan.go's buildNoActivityEvent would, for a fixed "now" and anchor —
// the shape Match/Plan/IdempotencyKey all decode.
func noActivityEvent(t *testing.T, now, anchor time.Time, entity datasource.EntityRef) workflow.Event {
	t.Helper()
	payload, err := json.Marshal(noActivityAnchorPayload{LastActivityAt: anchor})
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

// TestNoActivityReminderMatchFlipsAtTheCutoff proves Match is the exact
// re-check the coarse scan only approximates: an anchor strictly before
// now-N-days matches; an anchor at or after it does not — the precise
// decision no_activity_reminder's own Match doc promises, over the
// default 7-day threshold (no params, defaultNoActivityDays).
func TestNoActivityReminderMatchFlipsAtTheCutoff(t *testing.T) {
	now := time.Date(2026, 7, 16, 9, 0, 0, 0, time.UTC)
	entity := datasource.EntityRef{Type: datasource.EntityDeal, ID: ids.NewV7()}

	stale := now.AddDate(0, 0, -defaultNoActivityDays-1)
	ev := noActivityEvent(t, now, stale, entity)
	matched, err := (noActivityReminder{}).Match(context.Background(), ev)
	if err != nil {
		t.Fatalf("Match on a stale anchor: %v", err)
	}
	if !matched {
		t.Errorf("Match(anchor=%s, now=%s) = false, want true — the anchor is older than the %d-day default", stale, now, defaultNoActivityDays)
	}

	fresh := now.AddDate(0, 0, -defaultNoActivityDays+1)
	ev = noActivityEvent(t, now, fresh, entity)
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
	ev := noActivityEvent(t, now, anchor, datasource.EntityRef{Type: datasource.EntityDeal, ID: ids.NewV7()})
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
	ev := noActivityEvent(t, now, anchor, entity)

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

	first := noActivityEvent(t, time.Date(2026, 7, 16, 9, 0, 0, 0, time.UTC), anchor, entity)
	second := noActivityEvent(t, time.Date(2026, 7, 17, 9, 0, 0, 0, time.UTC), anchor, entity) // a later pass, same anchor
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
	third := noActivityEvent(t, time.Date(2026, 7, 22, 9, 0, 0, 0, time.UTC), movedAnchor, entity)
	thirdKey := h.IdempotencyKey(third)
	if thirdKey == firstKey {
		t.Error("IdempotencyKey did not change when the anchor moved — the trigger would never re-arm after the entity goes quiet a second time")
	}
}
