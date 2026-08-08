// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package orgdossier

// The floor is what a deployment with no model configured actually reads, so it
// is held to the same bar as the model path: every sentence cites a row the
// reader can open, and nothing it writes is an inference.

import (
	"strings"
	"testing"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/gradionhq/margince/backend/internal/compose/claims"
	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func field(name, value string, id *openapi_types.UUID) crmcontracts.CompanyProfileField {
	return crmcontracts.CompanyProfileField{
		Id:    id,
		Field: crmcontracts.CompanyProfileFieldField(name),
		Value: value,
	}
}

func rowID() *openapi_types.UUID {
	u := openapi_types.UUID(ids.NewV7())
	return &u
}

func populatedInput(t *testing.T) Input {
	t.Helper()
	return Input{
		OrganizationID: ids.NewV7().String(),
		ProfileFields: []crmcontracts.CompanyProfileField{
			field("icp", "Energy-intensive manufacturers", rowID()),
			field("offer_summary", "Load-shifting software for industrial sites", rowID()),
			field("legal_name", "Voltaq Systems GmbH", rowID()),
		},
	}
}

// The floor and the shared grounding filter have to AGREE. If the floor writes
// a sentence the filter would drop, a deployment with no model shows content
// that the model path would have refused — the two lanes would disagree about
// what counts as checkable, which is the whole thing being guaranteed.
func TestEveryFloorSentenceSurvivesTheSharedGroundingFilter(t *testing.T) {
	in := populatedInput(t)
	known := KnownRecords(in)

	sections := Deterministic(in)
	if len(sections) == 0 {
		t.Fatal("the floor produced nothing from three populated fields")
	}
	for _, section := range sections {
		for _, sentence := range section.Sentences {
			if !claims.Grounded(sentence, known) {
				t.Errorf("section %s: the filter would drop the floor's own sentence %q", section.Kind, sentence.Text)
			}
		}
	}
}

// A citation is the reader's way to check the claim. A sentence that cited the
// company record instead would tell them where to look but not at what, and the
// filter would drop it — so the floor skips the field rather than producing a
// sentence the filter and the floor disagree about.
func TestAFieldWithNoRowIDIsSkippedRatherThanCitedAgainstTheCompany(t *testing.T) {
	in := Input{
		OrganizationID: ids.NewV7().String(),
		ProfileFields: []crmcontracts.CompanyProfileField{
			field("icp", "Energy-intensive manufacturers", nil),
		},
	}
	if sections := Deterministic(in); len(sections) != 0 {
		t.Errorf("sections = %+v, want none — the one field cannot be cited", sections)
	}
}

func TestAnEmptyFieldWritesNoSentence(t *testing.T) {
	in := Input{
		OrganizationID: ids.NewV7().String(),
		ProfileFields: []crmcontracts.CompanyProfileField{
			field("icp", "   ", rowID()),
			field("legal_name", "Voltaq Systems GmbH", rowID()),
		},
	}
	sections := Deterministic(in)
	for _, section := range sections {
		for _, sentence := range section.Sentences {
			// The blank field is named, so a sentence built around it is caught
			// however it was worded — matching one exact rendering would pass
			// the moment the phrasing changed.
			if strings.Contains(sentence.Text, "Ideal customer") {
				t.Errorf("an empty field produced a sentence: %q", sentence.Text)
			}
		}
	}
	if len(sections) != 1 {
		t.Fatalf("sections = %d, want only the one the populated field lands in", len(sections))
	}
}

// A heading over silence reads as a finding of nothing, which is a different
// claim from having nothing to say.
func TestASectionWithNothingToSayIsAbsentRatherThanEmpty(t *testing.T) {
	in := Input{
		OrganizationID: ids.NewV7().String(),
		ProfileFields:  []crmcontracts.CompanyProfileField{field("legal_name", "Voltaq Systems GmbH", rowID())},
	}
	sections := Deterministic(in)
	if len(sections) != 1 {
		t.Fatalf("sections = %d, want exactly the firmographics one", len(sections))
	}
	if sections[0].Kind != sectionFirmographics {
		t.Errorf("kind = %q, want firmographics", sections[0].Kind)
	}
	for _, section := range sections {
		if len(section.Sentences) == 0 {
			t.Errorf("section %s rendered with no sentences", section.Kind)
		}
	}
}

// The order is the order a reader asks the questions. A dossier whose sections
// reshuffled between reads would read as a different answer to the same
// question, and map iteration would do exactly that.
func TestSectionsRenderInReadingOrder(t *testing.T) {
	in := populatedInput(t)
	first := Deterministic(in)
	for range 8 {
		again := Deterministic(in)
		if len(again) != len(first) {
			t.Fatalf("section count moved between reads: %d then %d", len(first), len(again))
		}
		for i := range again {
			if again[i].Kind != first[i].Kind {
				t.Fatalf("section order moved between reads: %q then %q", first[i].Kind, again[i].Kind)
			}
		}
	}
	// Reading order specifically, not merely a stable one.
	want := []string{sectionProductsService, sectionMarkets, sectionFirmographics}
	for i, kind := range want {
		if first[i].Kind != kind {
			t.Errorf("section %d = %q, want %q", i, first[i].Kind, kind)
		}
	}
}

// The floor restates recorded values and draws no conclusions, so labelling one
// an assessment would claim a judgment nobody made.
func TestTheFloorWritesOnlyFacts(t *testing.T) {
	for _, section := range Deterministic(populatedInput(t)) {
		for _, sentence := range section.Sentences {
			if sentence.Nature != natureFact {
				t.Errorf("floor sentence %q carries nature %q, want fact", sentence.Text, sentence.Nature)
			}
		}
	}
}

// No answer may hand the reader a record id in its prose, whichever writer
// produced it.
func TestNoFloorSentenceSpellsAnIDAtTheReader(t *testing.T) {
	for _, section := range Deterministic(populatedInput(t)) {
		for _, sentence := range section.Sentences {
			if claims.SpellsRecordID(sentence.Text) {
				t.Errorf("the floor spelled an id at the reader: %q", sentence.Text)
			}
		}
	}
}
