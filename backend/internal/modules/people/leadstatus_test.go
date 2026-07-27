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
	for _, workable := range []string{"new", "working"} {
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
