// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

import (
	"testing"
	"time"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
)

// The derived state follows the §18.1 arithmetic against a pinned clock, and
// an answered or closed lead owes nothing.
func TestLeadSLAFieldsFollowTheClock(t *testing.T) {
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	leadSLAClock = func() time.Time { return now }
	t.Cleanup(func() { leadSLAClock = time.Now })
	routed := now.Add(-DefaultFirstResponseTarget + 10*time.Minute)
	responded := now.Add(-time.Minute)
	closed := now

	cases := map[string]struct {
		routed, created         time.Time
		firstResponse, archived *time.Time
		want                    *crmcontracts.LeadSlaState
	}{
		"fresh, clock from created_at": {created: now.Add(-time.Hour), want: state(crmcontracts.LeadSlaStateWithinTarget)},
		"routing restarts the clock":   {created: now.Add(-48 * time.Hour), routed: now.Add(-time.Hour), want: state(crmcontracts.LeadSlaStateWithinTarget)},
		"inside the last quarter":      {created: routed, want: state(crmcontracts.LeadSlaStateAtRisk)},
		"past the deadline":            {created: now.Add(-DefaultFirstResponseTarget - time.Minute), want: state(crmcontracts.LeadSlaStateBreached)},
		"answered leads owe nothing":   {created: now.Add(-48 * time.Hour), firstResponse: &responded, want: nil},
		"closed leads owe nothing":     {created: now.Add(-48 * time.Hour), archived: &closed, want: nil},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var routedAt *time.Time
			if !tc.routed.IsZero() {
				routedAt = &tc.routed
			}
			deadline, got := leadSLAFields(leadSLAPolicy{enabled: true, target: DefaultFirstResponseTarget}, routedAt, tc.created, tc.firstResponse, tc.archived)
			if (got == nil) != (tc.want == nil) || (got != nil && *got != *tc.want) {
				t.Fatalf("sla_state = %v, want %v", got, tc.want)
			}
			if tc.archived == nil && deadline == nil {
				t.Fatal("an open lead always carries its deadline")
			}
			offDeadline, offState := leadSLAFields(leadSLAPolicy{target: DefaultFirstResponseTarget}, routedAt, tc.created, tc.firstResponse, tc.archived)
			if offDeadline != nil || offState != nil {
				t.Fatalf("with the target switched off a lead carries no SLA field, got deadline %v state %v", offDeadline, offState)
			}
		})
	}
}

// What counts as a first response (§18.1): a human's outbound always; an
// agent's only when the lead had already written in — a cold touch with
// nothing to respond to is not a response. A note counts exactly when the
// ladder counts it (humanLoggedNote): typed by a human, source manual.
func TestIsFirstResponseActivity(t *testing.T) {
	cases := map[string]struct {
		touch leadResponseTouch
		want  bool
	}{
		"human outbound":                  {leadResponseTouch{direction: "outbound", capturedBy: "human:u1"}, true},
		"agent reply to an inbound":       {leadResponseTouch{direction: "outbound", capturedBy: "agent:sdr", hadInbound: true}, true},
		"agent cold outbound":             {leadResponseTouch{direction: "outbound", capturedBy: "agent:sdr"}, false},
		"inbound is the lead's, not ours": {leadResponseTouch{direction: "inbound", capturedBy: "human:u1"}, false},
		"a rep's composer note":           {leadResponseTouch{kind: "note", source: "manual", capturedBy: "human:u1"}, true},
		"an imported note":                {leadResponseTouch{kind: "note", source: "flip:hubspot:a1", capturedBy: "human:op"}, false},
		"an agent's note":                 {leadResponseTouch{kind: "note", source: "manual", capturedBy: "agent:sdr"}, false},
	}
	for name, tc := range cases {
		if got := isFirstResponseActivity(tc.touch); got != tc.want {
			t.Errorf("%s: got %t, want %t", name, got, tc.want)
		}
	}
}

func state(s crmcontracts.LeadSlaState) *crmcontracts.LeadSlaState { return &s }
