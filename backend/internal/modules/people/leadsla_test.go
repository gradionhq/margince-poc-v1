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
	routed := now.Add(-FirstResponseTarget + 10*time.Minute)
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
		"past the deadline":            {created: now.Add(-FirstResponseTarget - time.Minute), want: state(crmcontracts.LeadSlaStateBreached)},
		"answered leads owe nothing":   {created: now.Add(-48 * time.Hour), firstResponse: &responded, want: nil},
		"closed leads owe nothing":     {created: now.Add(-48 * time.Hour), archived: &closed, want: nil},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var routedAt *time.Time
			if !tc.routed.IsZero() {
				routedAt = &tc.routed
			}
			deadline, got := leadSLAFields(routedAt, tc.created, tc.firstResponse, tc.archived)
			if (got == nil) != (tc.want == nil) || (got != nil && *got != *tc.want) {
				t.Fatalf("sla_state = %v, want %v", got, tc.want)
			}
			if tc.archived == nil && deadline == nil {
				t.Fatal("an open lead always carries its deadline")
			}
		})
	}
}

// What counts as a first response (§18.1): a human's outbound always; an
// agent's only when the lead had already written in — a cold touch with
// nothing to respond to is not a response.
func TestIsFirstResponseActivity(t *testing.T) {
	cases := map[string]struct {
		direction, by string
		hadInbound    bool
		want          bool
	}{
		"human outbound":                  {"outbound", "human:u1", false, true},
		"agent reply to an inbound":       {"outbound", "agent:sdr", true, true},
		"agent cold outbound":             {"outbound", "agent:sdr", false, false},
		"inbound is the lead's, not ours": {"inbound", "human:u1", false, false},
	}
	for name, tc := range cases {
		if got := isFirstResponseActivity(tc.direction, tc.by, tc.hadInbound); got != tc.want {
			t.Errorf("%s: got %t, want %t", name, got, tc.want)
		}
	}
}

func state(s crmcontracts.LeadSlaState) *crmcontracts.LeadSlaState { return &s }
