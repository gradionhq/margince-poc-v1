// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

import (
	"math"
	"testing"
)

// closeEnough compares against the 4-decimal figures the spec prints.
func closeEnough(got, want float64) bool { return math.Abs(got-want) < 0.0001 }

// closeToPrinted compares against a figure the spec rounds to 3 decimals
// (e.g. it prints 0.982 for 0.981666…), so the tolerance is the
// rounding, not slack in the formula.
func closeToPrinted(got, want float64) bool { return math.Abs(got-want) < 0.0005 }

// The spec pins Jaro-Winkler so its worked examples reproduce against
// this code; these assert the exact arithmetic it shows, not a range.
func TestNameSimilarityReproducesTheSpecWorkedExample(t *testing.T) {
	// PO-F-1 worked example: Jaro("jon doe","john doe") = (7/7 + 7/8 + 7/7)/3
	// = 0.9583; Winkler prefix l=2, p=0.1 → name_sim = 0.9667.
	got := nameSimilarity("Jon Doe", "John Doe")
	if !closeEnough(got, 0.9667) {
		t.Fatalf("name_sim(Jon Doe, John Doe) = %.4f, spec pins 0.9667", got)
	}
	if raw := jaro("jon doe", "john doe"); !closeEnough(raw, 0.9583) {
		t.Fatalf("jaro(jon doe, john doe) = %.4f, spec pins 0.9583", raw)
	}
}

func TestNameSimilarityIsCaseAndAccentInsensitive(t *testing.T) {
	// PO-PARAM-JW-2 pins casefold + unaccent preprocessing: the German
	// market's daily friction is that "Jürgen Müller" and "Jurgen Muller"
	// are the same person.
	if got := nameSimilarity("Jürgen Müller", "Jurgen Muller"); got != 1 {
		t.Fatalf("accented and unaccented spellings scored %.4f, want an exact 1", got)
	}
	if got := nameSimilarity("ACME Corp", "acme corp"); got != 1 {
		t.Fatalf("case variants scored %.4f, want an exact 1", got)
	}
}

func TestNameSimilarityBounds(t *testing.T) {
	if got := nameSimilarity("", ""); got != 1 {
		t.Fatalf("two empty names scored %.4f, want 1", got)
	}
	if got := nameSimilarity("Jane Doe", ""); got != 0 {
		t.Fatalf("empty against non-empty scored %.4f, want 0", got)
	}
	if got := nameSimilarity("Jane Doe", "Zbigniew Xu"); got >= dedupeReviewThreshold {
		t.Fatalf("unrelated names scored %.4f, at or above the %.2f review threshold", got, dedupeReviewThreshold)
	}
}

func TestNameSimilarityIsSymmetric(t *testing.T) {
	// An asymmetric metric would make the decision depend on which row
	// happened to be the candidate — the same pair must score alike from
	// either side.
	forward := nameSimilarity("Jon Doe", "John Doe")
	reverse := nameSimilarity("John Doe", "Jon Doe")
	if !closeEnough(forward, reverse) {
		t.Fatalf("asymmetric: %.4f forward vs %.4f reverse", forward, reverse)
	}
}

func TestJaroCountsTranspositions(t *testing.T) {
	// The reference pair: 6 matches with one transposed adjacent pair →
	// (6/6 + 6/6 + 5/6)/3 = 0.9444. Names shorter than 4 runes have a
	// zero-width match window, so a transposition in them is invisible to
	// Jaro by construction — this pair is long enough to show the term.
	if got := jaro("martha", "marhta"); !closeEnough(got, 0.9444) {
		t.Fatalf("jaro(martha, marhta) = %.4f, want 0.9444", got)
	}
}

func TestNormalizeOrgNameStripsTrailingLegalSuffixes(t *testing.T) {
	// PO-F-2 worked example: "Acme Inc" and "Acme GmbH" normalize to
	// "acme" and meet at name_sim = 1.0 → 🟡 review, because different
	// legal entities are a human's call.
	for _, spelling := range []string{"Acme Inc", "Acme GmbH", "Acme, Inc.", "ACME AG"} {
		if got := normalizeOrgName(spelling); got != "acme" {
			t.Fatalf("normalizeOrgName(%q) = %q, want %q", spelling, got, "acme")
		}
	}
	if got := nameSimilarity(normalizeOrgName("Acme Inc"), normalizeOrgName("Acme GmbH")); got != 1 {
		t.Fatalf("Acme Inc vs Acme GmbH scored %.4f, spec pins an exact 1.0", got)
	}
}

func TestNormalizeOrgNameKeepsASuffixThatIsTheName(t *testing.T) {
	// Stripping every occurrence rather than the trailing token would
	// erase the company: a firm called "Co" or "AG Systems" is not a
	// suffix.
	if got := normalizeOrgName("AG Systems"); got != "ag systems" {
		t.Fatalf("normalizeOrgName(AG Systems) = %q, want %q", got, "ag systems")
	}
	if got := normalizeOrgName("Co"); got != "co" {
		t.Fatalf("a lone suffix-shaped name was stripped to %q, want %q", got, "co")
	}
}
