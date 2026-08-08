// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package claims

// The grounding filter is the product's answer to "how do I know this is true",
// and it is one implementation precisely so it cannot be lenient in one place
// and strict in another. These cases are DOSS-FORM-1 read back as behaviour.

import "testing"

const (
	orgID  = "019fd000-0000-7000-8000-000000000001"
	dealID = "019fd000-0000-7000-8000-000000000002"
	ghost  = "019fd000-0000-7000-8000-0000000000ff"
)

func supplied() map[Evidence]bool {
	return map[Evidence]bool{
		{EntityType: "organization", EntityID: orgID}: true,
		{EntityType: "deal", EntityID: dealID}:        true,
	}
}

func TestASentenceCitingNothingIsDropped(t *testing.T) {
	// DOSS-AC-1. An unattributed sentence is the one thing this surface must
	// never render: the reader has no way to check it and no way to know that.
	if Grounded(Sentence{Text: "They operate in Germany."}, supplied()) {
		t.Error("a sentence with no citation was kept")
	}
	if Grounded(Sentence{Text: "", Evidence: []Evidence{{EntityType: "deal", EntityID: dealID}}}, supplied()) {
		t.Error("an empty sentence was kept")
	}
}

func TestASentenceCitingAnUnsuppliedRecordIsDroppedWhole(t *testing.T) {
	// DOSS-AC-2. Dropped WHOLE, not shown with the bad citation stripped: the
	// claim may rest on the invented half, so keeping it with the good citation
	// attached presents it as checked when it is not.
	sentence := Sentence{
		Text: "They renewed last quarter.",
		Evidence: []Evidence{
			{EntityType: "deal", EntityID: dealID},
			{EntityType: "deal", EntityID: ghost},
		},
	}
	if Grounded(sentence, supplied()) {
		t.Error("a sentence citing an unsupplied record was kept")
	}
	kept := Keep([]Sentence{sentence}, supplied(), map[string]bool{"fact": true}, "fact")
	if len(kept) != 0 {
		t.Errorf("Keep returned %d sentences, want the whole sentence dropped rather than trimmed", len(kept))
	}
}

func TestAValidIdentityOfTheWrongKindIsStillDropped(t *testing.T) {
	// The check is on the (kind, identity) PAIR. Keying on the id alone accepts
	// a real deal id cited as a person, and the chip then routes the reader to
	// the wrong screen — or to a record of a kind they were never shown.
	sentence := Sentence{
		Text:     "The buyer replied.",
		Evidence: []Evidence{{EntityType: "person", EntityID: dealID}},
	}
	if Grounded(sentence, supplied()) {
		t.Error("a real id cited under the wrong kind was kept")
	}
}

func TestASentenceSpellingARecordIDAtTheReaderIsDropped(t *testing.T) {
	sentence := Sentence{
		Text:     "See deal " + dealID + " for the renewal.",
		Evidence: []Evidence{{EntityType: "deal", EntityID: dealID}},
	}
	// Grounded in the citation sense, and still developer output. It is dropped
	// rather than stripped: the id sits mid-clause and cutting it leaves broken
	// grammar, while every id the sentence needed is already in its evidence.
	if Grounded(sentence, supplied()) {
		t.Error("a sentence spelling an id in its prose was kept")
	}
	if !SpellsRecordID(sentence.Text) {
		t.Error("SpellsRecordID missed an id the filter caught — the two must agree")
	}
}

func TestAnUndeclaredNatureReducesToFact(t *testing.T) {
	// DOSS-PARAM-1: fact is the only permitted compatibility default. Forwarding
	// the model's own string would render a label the contract does not define.
	kept := Keep(
		[]Sentence{{
			Text:     "They look like a strong fit.",
			Nature:   "hunch",
			Evidence: []Evidence{{EntityType: "organization", EntityID: orgID}},
		}},
		supplied(),
		map[string]bool{"fact": true, "assessment": true},
		"fact",
	)
	if len(kept) != 1 {
		t.Fatalf("kept %d sentences, want the grounded one", len(kept))
	}
	if kept[0].Nature != "fact" {
		t.Errorf("nature = %q, want it reduced to fact — the strictest reading", kept[0].Nature)
	}
}

func TestADeclaredNatureSurvives(t *testing.T) {
	// The reduction above must not flatten the distinction the vocabulary
	// exists for: a reader forgives a wrong opinion and not a wrong fact, so an
	// assessment that arrives labelled stays labelled.
	kept := Keep(
		[]Sentence{{
			Text:     "They look like a strong fit.",
			Nature:   "assessment",
			Evidence: []Evidence{{EntityType: "organization", EntityID: orgID}},
		}},
		supplied(),
		map[string]bool{"fact": true, "assessment": true},
		"fact",
	)
	if len(kept) != 1 || kept[0].Nature != "assessment" {
		t.Errorf("kept = %+v, want the assessment label preserved", kept)
	}
}

func TestRepeatedCitationsCollapse(t *testing.T) {
	kept := Dedupe([]Sentence{{
		Text: "Two mentions, one record.",
		Evidence: []Evidence{
			{EntityType: "deal", EntityID: dealID},
			{EntityType: "deal", EntityID: dealID},
			{EntityType: "organization", EntityID: orgID},
		},
	}})
	if len(kept[0].Evidence) != 2 {
		t.Fatalf("evidence = %+v, want the duplicate collapsed", kept[0].Evidence)
	}
	// First-seen order, so the chips do not reshuffle between reads.
	if kept[0].Evidence[0].EntityID != dealID {
		t.Errorf("evidence order = %+v, want first-seen order kept", kept[0].Evidence)
	}
}

func TestDedupeDoesNotMutateItsInput(t *testing.T) {
	// The brief hands the same slice to more than one writer, and a filter that
	// rewrote its caller's backing array would change what the OTHER writer saw
	// depending on which ran first.
	in := []Sentence{{
		Text: "Two mentions, one record.",
		Evidence: []Evidence{
			{EntityType: "deal", EntityID: dealID},
			{EntityType: "deal", EntityID: dealID},
		},
	}}
	Dedupe(in)
	if len(in[0].Evidence) != 2 {
		t.Errorf("Dedupe rewrote its caller's slice: %+v", in[0].Evidence)
	}
}
