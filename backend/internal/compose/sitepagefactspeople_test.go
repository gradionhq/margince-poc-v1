// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import "testing"

// A German imprint prints its whole board under one label, and gives the chair
// an extra title in the same breath. adesso.de:
//
//	Vorstand: Mark Lohweber (Vorstandsvorsitzender), Benedikt Bonnmann,
//	Kristina Gerwert, Michael Knopp, Andreas Prenneis.
//
// Every one of those five is a real, correctly-attributed officer. The chair's
// role differs from his colleagues' by design — that is what the parenthesis
// says — so a rule demanding declared companions carry an IDENTICAL role reads
// the page's own precision as a contradiction and refuses the claim.
//
// This is not hypothetical: it cost all six adesso.de people on a re-crawl,
// and the same shape appears on any German imprint with a chair.
func TestReachesPastAnother_SharedLabelAllowsAChairsExtraTitle(t *testing.T) {
	quote := "Vorstand: Mark Lohweber (Vorstandsvorsitzender), Benedikt Bonnmann, Kristina Gerwert"
	claims := []pageFactsPerson{
		{N: "Mark Lohweber", R: "Vorstandsvorsitzender"},
		{N: "Benedikt Bonnmann", R: "Vorstand"},
		{N: "Kristina Gerwert", R: "Vorstand"},
	}
	chair := pageFactsPerson{
		N: "Mark Lohweber",
		R: "Vorstandsvorsitzender",
		Q: quote,
		W: "Benedikt Bonnmann; Kristina Gerwert",
	}
	if reachesPastAnother(chair, quote, chair.N, chair.R, claims) {
		t.Fatal("the chair's claim was refused: his colleagues share the Vorstand label " +
			"and he carries the chair title the same sentence gives him")
	}

	// A plain member of the same board still passes, which it always did.
	member := pageFactsPerson{
		N: "Benedikt Bonnmann",
		R: "Vorstand",
		Q: quote,
		W: "Mark Lohweber; Kristina Gerwert",
	}
	if reachesPastAnother(member, quote, member.N, member.R, claims) {
		t.Fatal("a plain board member sharing the label was refused")
	}
}

// The rule this gate exists for has to keep working: a quote that reaches over
// somebody the REPLY ITSELF gives a different job is a misattribution.
//
// Anna is a Geschäftsführerin and Bernd a Prokurist. If Bernd's claim declares
// Anna as sharing his label, the reply now says two contradictory things about
// her, and the claim is refused.
func TestReachesPastAnother_StillRefusesAReachOverADifferentRole(t *testing.T) {
	quote := "Geschäftsführer Anna Muster Prokurist Bernd Vermaaten"
	claims := []pageFactsPerson{
		{N: "Anna Muster", R: "Geschäftsführerin"},
		{N: "Bernd Vermaaten", R: "Prokurist"},
	}
	// Bernd claiming the Geschäftsführer title, declaring Anna as sharing it —
	// which the reply's own entry for Anna contradicts.
	wrong := pageFactsPerson{
		N: "Bernd Vermaaten",
		R: "Geschäftsführer",
		Q: quote,
		W: "Anna Muster",
	}
	if !reachesPastAnother(wrong, quote, wrong.N, wrong.R, claims) {
		t.Fatal("a reach over somebody the reply gives a different role was allowed")
	}
}

// An UNDECLARED companion inside the quote is still refused, declared-role
// leniency or not: the model has to say who its quote reaches over.
func TestReachesPastAnother_StillRefusesAnUndeclaredCompanion(t *testing.T) {
	quote := "Vorstand: Mark Lohweber, Benedikt Bonnmann"
	claims := []pageFactsPerson{
		{N: "Mark Lohweber", R: "Vorstand"},
		{N: "Benedikt Bonnmann", R: "Vorstand"},
	}
	silent := pageFactsPerson{N: "Mark Lohweber", R: "Vorstand", Q: quote, W: ""}
	if !reachesPastAnother(silent, quote, silent.N, silent.R, claims) {
		t.Fatal("a companion the reply never declared was allowed")
	}
}

// sameOfficerLabel decides whether two printed titles are one label. The
// gendered pairs are the trap: "geschäftsführer" is a prefix of
// "geschäftsführerin", and treating that as a distinction would let a
// Prokurist claim a Geschäftsführerin's title through a feminine ending.
func TestSameOfficerLabel(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool
		why  string
	}{
		{"vorstand", "vorstandsvorsitzender", true, "the board and its chair"},
		{"partner", "partner & managing director", true, "a qualifier as its own words"},
		{"vorstand", "vorstand", true, "identical"},
		{"geschäftsführer", "geschäftsführerin", false, "the same job, feminine"},
		{"leiter", "leiterin", false, "the same job, feminine"},
		{"geschäftsführer", "prokurist", false, "two different offices"},
		{"vorstand", "aufsichtsrat", false, "the board and the body that supervises it"},
		{"", "vorstand", false, "an empty title claims nothing"},
	} {
		if got := sameOfficerLabel(tc.a, tc.b); got != tc.want {
			t.Errorf("sameOfficerLabel(%q, %q) = %v, want %v — %s", tc.a, tc.b, got, tc.want, tc.why)
		}
	}
}
