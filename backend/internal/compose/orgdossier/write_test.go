// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package orgdossier

// The dossier's model lane. What it may add is prose; what it may not add is
// knowledge, so the tests are mostly about sentences that get dropped.

import (
	"context"
	"testing"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
)

// describing renders a reply whose one sentence cites a row of this input.
func describing(kind, text string, in Input) string {
	id := in.ProfileFields[0].Id.String()
	return `{"sections":[{"kind":"` + kind + `","sentences":[
		{"text":"` + text + `","nature":"fact",
		 "evidence":[{"entity_type":"profile_field","entity_id":"` + id + `"}]}]}]}`
}

// The floor is a real answer here, unlike the growth fit's, so a failed lane
// costs the reader prose rather than the surface.
func TestEveryDossierModelFailureFallsBackToADescribedCompany(t *testing.T) {
	in := fourOfSeven()
	for name, lane := range map[string]Completer{
		"no lane configured":       nil,
		"lane over budget":         failingLane{},
		"lane answering prose":     proseLane{},
		"lane citing nothing":      scriptedLane{reply: `{"sections":[]}`},
		"lane citing a stranger":   scriptedLane{reply: describing("summary", "They do things.", fourOfSeven())},
		"lane inventing a section": scriptedLane{reply: describing("pipeline", "They are close to buying.", in)},
	} {
		t.Run(name, func(t *testing.T) {
			got, by, _ := WriteDossier(context.Background(), lane, in)

			if by != crmcontracts.Deterministic {
				t.Errorf("generated_by = %q, want deterministic — the model did not produce this", by)
			}
			if len(got) == 0 {
				t.Fatal("no sections: the floor must still describe a company it has facts for")
			}
			for _, section := range got {
				for _, sentence := range section.Sentences {
					if len(sentence.Evidence) == 0 {
						t.Error("a floor sentence carries no evidence")
					}
				}
			}
		})
	}
}

// The happy path: grounded prose is served as the model's.
func TestAGroundedDossierIsServedAsTheModels(t *testing.T) {
	in := fourOfSeven()
	lane := scriptedLane{
		reply: describing("summary", "They build load-shifting software for industrial sites.", in),
	}

	got, by, _ := WriteDossier(context.Background(), lane, in)

	if by != crmcontracts.Model {
		t.Fatalf("generated_by = %q, want model", by)
	}
	if len(got) != 1 || got[0].Kind != sectionSummary {
		t.Fatalf("sections = %+v, want the one summary section the model wrote", got)
	}
	if got[0].Sentences[0].Text != "They build load-shifting software for industrial sites." {
		t.Errorf("text = %q, want the model's own sentence", got[0].Sentences[0].Text)
	}
}

// A section kind the contract does not declare has no heading to render under,
// so its sentences have nowhere to go — and a model asked for six kinds will
// eventually offer a seventh.
func TestASectionKindTheContractDoesNotDeclareIsDropped(t *testing.T) {
	in := fourOfSeven()
	reply := `{"sections":[
		` + sectionEntry("summary", "They build load-shifting software.", in) + `,
		` + sectionEntry("pipeline", "They are close to buying.", in) + `]}`

	got, err := ParseDossier(reply, in)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	for _, section := range got {
		if section.Kind == "pipeline" {
			t.Error("a section kind the contract does not declare survived the parse")
		}
	}
	if len(got) != 1 {
		t.Errorf("sections = %+v, want only the declared one", got)
	}
}

func sectionEntry(kind, text string, in Input) string {
	id := in.ProfileFields[0].Id.String()
	return `{"kind":"` + kind + `","sentences":[
		{"text":"` + text + `","nature":"fact",
		 "evidence":[{"entity_type":"profile_field","entity_id":"` + id + `"}]}]}`
}

// The dossier describes THEM. Our own offering must not reach the request at
// all — a writer given it starts comparing, which is the growth fit's job and
// the reason the two surfaces are separate.
func TestTheDossierRequestDoesNotAskForOurOwnCompanyContext(t *testing.T) {
	if req := DossierRequest(fourOfSeven()); req.IncludeCompanyContext {
		t.Error("the dossier asked for our own offering; that is the growth fit's input, not this one")
	}
}
