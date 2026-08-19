// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

import (
	"errors"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/values"
)

// The ordinary create/update write path may only place a lead in a
// workable status. The terminal states are reached through the governed
// promote/disqualify actions; letting a bare status edit set them lets a
// lead:update-only caller skip the person mint, consent hand-off and
// archival — and strands the lead in an unrecoverable dead state (F-003).
func TestParseWritableLeadStatusRefusesTerminalStates(t *testing.T) {
	for _, workable := range []string{"new", "contacted", "engaged"} {
		if _, err := parseWritableLeadStatus(workable); err != nil {
			t.Fatalf("workable status %q must parse on the write path, got %v", workable, err)
		}
	}

	for _, terminal := range []string{"promoted", "disqualified"} {
		_, err := parseWritableLeadStatus(terminal)
		if err == nil {
			t.Fatalf("terminal status %q must be refused on the write path", terminal)
		}
		var pe *values.ParseError
		if !errors.As(err, &pe) || pe.Code != "terminal_lead_status" {
			t.Fatalf("terminal status %q must fault as terminal_lead_status, got %v", terminal, err)
		}
	}

	if _, err := parseWritableLeadStatus("bogus"); err == nil {
		t.Fatal("an unknown status must still be refused on the write path")
	}
}

// The system only ever climbs the ladder: each open step advances to the
// steps above it and to nothing else, and the terminal pair is off the
// ladder in both directions.
func TestLeadStatusLadderOnlyAdvancesUpwards(t *testing.T) {
	cases := []struct {
		from, to LeadStatus
		want     bool
	}{
		{LeadStatusNew, LeadStatusContacted, true},
		{LeadStatusNew, LeadStatusEngaged, true},
		{LeadStatusContacted, LeadStatusEngaged, true},
		{LeadStatusContacted, LeadStatusNew, false},
		{LeadStatusEngaged, LeadStatusContacted, false},
		{LeadStatusEngaged, LeadStatusEngaged, false},
		{LeadStatusPromoted, LeadStatusEngaged, false},
		{LeadStatusDisqualified, LeadStatusContacted, false},
		{LeadStatusNew, LeadStatusPromoted, false},
	}
	for _, tc := range cases {
		if got := tc.from.Advances(tc.to); got != tc.want {
			t.Errorf("%s → %s advances = %v, want %v", tc.from, tc.to, got, tc.want)
		}
	}
	for _, s := range []LeadStatus{LeadStatusNew, LeadStatusContacted, LeadStatusEngaged} {
		if !s.Open() {
			t.Errorf("%s should be open", s)
		}
	}
	for _, s := range []LeadStatus{LeadStatusPromoted, LeadStatusDisqualified, LeadStatus("working")} {
		if s.Open() {
			t.Errorf("%s should not be open", s)
		}
	}
}
