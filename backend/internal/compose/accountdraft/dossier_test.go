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

// The check reads the dossier's WORDS, not merely whether one was supplied.
// Keying on presence would let the model tag any claim as "grounded in the
// dossier" as long as some unrelated sentence was there — provenance that says
// the opposite of the truth, which is worse than none, because a reader trusts
// a chip.
func TestADossierReasonMustActuallyComeFromTheDossier(t *testing.T) {
	supplied := Input{
		Recipient: RecipientIn{ID: "p1", Name: "Priya Raman"},
		Dossier:   []string{"Runs its own dispatch software across three depots."},
	}

	cases := []struct {
		name  string
		label string
		keep  bool
	}{
		{name: "the label is about the supplied sentence", label: "their own dispatch software", keep: true},
		{name: "the label is written in the reader's words", label: "dispatch software across depots", keep: true},
		{name: "a claim the dossier never made", label: "recently raised a Series B", keep: false},
		{name: "one shared word is not grounding", label: "software licensing renewal", keep: false},
		{name: "too short to tie to anything", label: "growth", keep: false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			reasons := []modelReason{{
				Kind:  string(crmcontracts.AccountDraftReasonKindDossier),
				Label: c.label,
			}}
			got := keepGroundedReasons(reasons, supplied)
			if (len(got) == 1) != c.keep {
				t.Errorf("label %q: kept=%v, want %v", c.label, len(got) == 1, c.keep)
			}
		})
	}
}
