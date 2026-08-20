// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package people

// The status ladder and the opt-in first-response target against Postgres:
// the system climbs and never descends, a terminal lead is never moved, a
// human's hand is recorded as such, and with the target switched off no lead
// carries an SLA field and the breach scan records nothing.

import (
	"errors"
	"testing"
	"time"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/platform/settings"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

func TestLadderClimbsFromActivityAndNeverDescends(t *testing.T) {
	e := setupPromoteConsent(t)
	now := time.Now().UTC()
	lead := e.seedLeadCreatedAt(t, "ladder@example.test", now.Add(-time.Hour))

	moved, err := e.store.AdvanceLeadStatus(e.ctx, lead, LeadStatusContacted, now)
	if err != nil || !moved {
		t.Fatalf("new → contacted: moved=%v err=%v", moved, err)
	}
	got, err := e.store.GetLead(e.ctx, lead, storekit.LiveOnly)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != crmcontracts.LeadStatusContacted || got.StatusSetBy == nil || *got.StatusSetBy != crmcontracts.LeadStatusSetBySystem {
		t.Fatalf("after outbound: status=%s set_by=%v, want contacted by system", got.Status, got.StatusSetBy)
	}

	// A replayed or late outbound never pulls the lead back, and a second
	// identical step is a no-op rather than a second audit row.
	if moved, err := e.store.AdvanceLeadStatus(e.ctx, lead, LeadStatusContacted, now); err != nil || moved {
		t.Errorf("contacted → contacted: moved=%v err=%v, want no move", moved, err)
	}
	if moved, err := e.store.AdvanceLeadStatus(e.ctx, lead, LeadStatusNew, now); err != nil || moved {
		t.Errorf("contacted → new: moved=%v err=%v, want no move", moved, err)
	}
	if moved, err := e.store.AdvanceLeadStatus(e.ctx, lead, LeadStatusEngaged, now); err != nil || !moved {
		t.Fatalf("contacted → engaged: moved=%v err=%v", moved, err)
	}

	// A human may step back down, and the row says a human did it.
	back := string(LeadStatusContacted)
	byHand, err := e.store.UpdateLead(e.ctx, lead, UpdateLeadInput{Status: &back})
	if err != nil {
		t.Fatalf("human step down: %v", err)
	}
	if byHand.Status != crmcontracts.LeadStatusContacted || byHand.StatusSetBy == nil || *byHand.StatusSetBy != crmcontracts.LeadStatusSetByHuman {
		t.Errorf("after the human's edit: status=%s set_by=%v, want contacted by human", byHand.Status, byHand.StatusSetBy)
	}

	// A terminal lead is never moved.
	if _, err := e.store.DisqualifyLead(e.ctx, lead, DisqualifyLeadInput{}); err != nil {
		t.Fatal(err)
	}
	if moved, err := e.store.AdvanceLeadStatus(e.ctx, lead, LeadStatusEngaged, now); err != nil || moved {
		t.Errorf("disqualified → engaged: moved=%v err=%v, want no move", moved, err)
	}
	closed, err := e.store.GetLead(e.ctx, lead, storekit.IncludeArchived)
	if err != nil {
		t.Fatal(err)
	}
	if closed.Status != crmcontracts.LeadStatusDisqualified {
		t.Errorf("terminal lead read back as %s", closed.Status)
	}
}

// Which step a captured activity earns: engagement from an inbound reply or
// a booked/held meeting, contact from any outbound touch or a note a human
// logged, nothing else.
func TestLadderStepForReadsTheTouch(t *testing.T) {
	cases := []struct {
		name string
		t    leadResponseTouch
		want LeadStatus
		ok   bool
	}{
		{"inbound email", leadResponseTouch{direction: "inbound", kind: "email"}, LeadStatusEngaged, true},
		{"booked meeting", leadResponseTouch{kind: "meeting", meetingStatus: "booked"}, LeadStatusEngaged, true},
		{"held meeting", leadResponseTouch{kind: "meeting", meetingStatus: "held"}, LeadStatusEngaged, true},
		{"cancelled meeting", leadResponseTouch{kind: "meeting", meetingStatus: "cancelled"}, "", false},
		{"outbound email", leadResponseTouch{direction: "outbound", kind: "email"}, LeadStatusContacted, true},
		{"outbound call by the system", leadResponseTouch{direction: "outbound", kind: "call", capturedBy: "connector:x"}, LeadStatusContacted, true},
		{"a note a human logged", leadResponseTouch{kind: "note", capturedBy: "human:u1"}, LeadStatusContacted, true},
		{"a note an agent wrote", leadResponseTouch{kind: "note", capturedBy: "agent:a1"}, "", false},
		{"a note with no author prefix", leadResponseTouch{kind: "note"}, "", false},
	}
	for _, tc := range cases {
		got, ok := ladderStepFor(tc.t)
		if ok != tc.ok || got != tc.want {
			t.Errorf("%s: step=%q ok=%v, want %q %v", tc.name, got, ok, tc.want, tc.ok)
		}
	}
}

// With the target off (the default) a lead carries no SLA field, the breach
// scan records nothing and the sla_state filter lists nothing; switching it
// on through the settings write turns all three on, and the target is the
// installation's number.
func TestFirstResponseTargetIsOptIn(t *testing.T) {
	e := setupPromoteConsent(t)
	now := time.Now().UTC()
	overdue := e.seedLeadCreatedAt(t, "overdue@example.test", now.Add(-DefaultFirstResponseTarget-time.Hour))

	off, err := e.store.GetLead(e.ctx, overdue, storekit.LiveOnly)
	if err != nil {
		t.Fatal(err)
	}
	if off.SlaDeadlineAt != nil || off.SlaState != nil {
		t.Errorf("with the target off the lead carries deadline %v state %v, want neither", off.SlaDeadlineAt, off.SlaState)
	}
	breaches, err := e.store.ScanLeadSLA(e.ctx, now)
	if err != nil || len(breaches) != 0 {
		t.Errorf("scan with the target off = %d breaches err=%v, want none", len(breaches), err)
	}
	breached := crmcontracts.ListLeadsParamsSlaState(crmcontracts.LeadSlaStateBreached)
	listed, _, err := e.store.ListLeads(e.ctx, ListLeadsInput{SLAState: &breached})
	if err != nil || len(listed) != 0 {
		t.Errorf("sla_state=breached with the target off lists %d leads err=%v, want none", len(listed), err)
	}

	current, err := e.store.GetLeadSettings(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if current.FirstResponseEnabled || current.FirstResponseTargetMinutes != int(DefaultFirstResponseTarget/time.Minute) {
		t.Errorf("default settings = %+v, want off with the 240-minute default", current)
	}
	tooShort := 5
	var invalid settings.InvalidValue
	if _, err := e.store.UpdateLeadSettings(e.ctx, UpdateLeadSettingsInput{FirstResponseTargetMinutes: &tooShort}); !errors.As(err, &invalid) {
		t.Errorf("a 5-minute target err = %v, want the setting's own validation refusal (422)", err)
	}
	on, minutes := true, 60
	updated, err := e.store.UpdateLeadSettings(e.ctx, UpdateLeadSettingsInput{FirstResponseEnabled: &on, FirstResponseTargetMinutes: &minutes})
	if err != nil {
		t.Fatalf("enable: %v", err)
	}
	if !updated.FirstResponseEnabled || updated.FirstResponseTargetMinutes != 60 {
		t.Errorf("after enable = %+v, want on at 60 minutes", updated)
	}

	onLead, err := e.store.GetLead(e.ctx, overdue, storekit.LiveOnly)
	if err != nil {
		t.Fatal(err)
	}
	if onLead.SlaState == nil || *onLead.SlaState != crmcontracts.LeadSlaStateBreached || onLead.SlaDeadlineAt == nil {
		t.Fatalf("with the target on the overdue lead reads %v / %v, want breached with a deadline", onLead.SlaState, onLead.SlaDeadlineAt)
	}
	if want := onLead.CreatedAt.Add(60 * time.Minute); !onLead.SlaDeadlineAt.Equal(want) {
		t.Errorf("deadline = %v, want created_at + the installation's 60 minutes (%v)", onLead.SlaDeadlineAt, want)
	}
	breaches, err = e.store.ScanLeadSLA(e.ctx, now)
	if err != nil || len(breaches) != 1 {
		t.Errorf("scan with the target on = %d breaches err=%v, want the one overdue lead", len(breaches), err)
	}
	listed, _, err = e.store.ListLeads(e.ctx, ListLeadsInput{SLAState: &breached})
	if err != nil || len(listed) != 1 {
		t.Errorf("sla_state=breached with the target on lists %d leads err=%v, want one", len(listed), err)
	}

	// A rep may read the settings (the list needs them) but not change them.
	rep := e.withGrants(map[string]principal.ObjectGrant{"lead": {Read: true}, "custom_field": {Read: true}})
	if _, err := e.store.GetLeadSettings(rep); err != nil {
		t.Errorf("rep read err = %v, want granted", err)
	}
	if _, err := e.store.UpdateLeadSettings(rep, UpdateLeadSettingsInput{FirstResponseEnabled: &on}); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Errorf("rep write err = %v, want ErrPermissionDenied", err)
	}
}
