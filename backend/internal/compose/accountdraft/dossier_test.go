// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package accountdraft

import (
	"encoding/json"
	"strings"
	"testing"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
)

// The Dossier field was declared on the input, advertised in the prompt as a
// citable reason kind, and populated by nothing. Both halves of that were dead:
// the field never reached the model, and a "dossier" reason coming back was
// dropped by the grounding filter because it cites no record.
//
// These pin both halves, because either one alone leaves the feature dead.
func TestASuppliedDossierReachesTheModel(t *testing.T) {
	in := Input{
		Company:   "Northwind Logistics",
		Recipient: RecipientIn{ID: "p1", Name: "Priya Raman", FirstName: "Priya"},
		Dossier: []string{
			"Runs its own dispatch software across three depots.",
			"Sells into mid-market freight forwarding.",
		},
	}

	payload, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("encoding the account input failed: %v", err)
	}
	if !strings.Contains(string(payload), "dispatch software") {
		t.Fatalf("the dossier did not reach the payload the prompt carries:\n%s", payload)
	}
}

// A dossier fact has no record to open, so it cites nothing — and the filter
// drops uncited reasons by design. It must survive when a dossier was supplied,
// and go back to being dropped the moment nothing feeds it, or the kind is
// reachable on an account the reader was told nothing about.
func TestADossierReasonSurvivesOnlyWhenADossierWasSupplied(t *testing.T) {
	reasons := []modelReason{{
		Kind:  string(crmcontracts.AccountDraftReasonKindDossier),
		Label: "runs its own dispatch software",
	}}

	fed := Input{
		Recipient: RecipientIn{ID: "p1", Name: "Priya Raman"},
		Dossier:   []string{"Runs its own dispatch software across three depots."},
	}
	if got := keepGroundedReasons(reasons, fed); len(got) != 1 {
		t.Errorf("a dossier reason should survive when a dossier was supplied, got %+v", got)
	}

	starved := Input{Recipient: RecipientIn{ID: "p1", Name: "Priya Raman"}}
	if got := keepGroundedReasons(reasons, starved); len(got) != 0 {
		t.Errorf("a dossier reason with no dossier behind it should be dropped, got %+v", got)
	}
}
