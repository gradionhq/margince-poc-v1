// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

import (
	"strings"
	"testing"
	"time"
)

// The name shapes that put one company in a workspace twice, each pinned above
// the review threshold. They are the premise the organization-name lock rests
// on: if one stopped colliding, the lock would be serializing writers over a
// pair the matcher no longer files.
func TestNearDuplicateOrgNamesScoreAboveTheReviewThreshold(t *testing.T) {
	pairs := []struct {
		name        string
		left, right string
	}{
		{"legal suffix stripped", "Baqend", "Baqend GmbH"},
		{"trailing qualifier", "Baqend", "Baqend Solutions"},
		{"word split inside the prefix", "Microsoft Corp", "Micro Soft Corp"},
		{"hyphenated qualifier", "Acme Ltd", "ACME-Group Ltd"},
		{"shared brand, different lines", "Deutsche Bahn", "Deutsche Post"},
		{"leading article", "The Boring Company", "The Home Depot"},
		{"transposed words", "IBM Deutschland", "Deutschland IBM"},
	}
	for _, p := range pairs {
		t.Run(p.name, func(t *testing.T) {
			score := nameSimilarity(NormalizeOrgName(p.left), NormalizeOrgName(p.right))
			if score < dedupeReviewThreshold {
				t.Errorf("%q vs %q scores %.4f, below the %.2f threshold — this pair no longer needs serializing",
					p.left, p.right, score, dedupeReviewThreshold)
			}
		})
	}

	// And the control: an unrelated pair must stay below, or the threshold is
	// matching everything and the guard's cost buys nothing.
	if score := nameSimilarity(NormalizeOrgName("Baqend"), NormalizeOrgName("Zorbatron Heavy Industry")); score >= dedupeReviewThreshold {
		t.Errorf("unrelated names score %.4f, at or above the threshold", score)
	}
}

// The similarity metric is quadratic in the longer name and runs inside the
// writing transaction while the organization-name lock is held, so an unbounded
// name would pin a pool connection and every organization-name writer in the
// workspace behind it. `display_name` is `text` with no maxLength in the
// contract, so the bound lives in the metric.
func TestNameScoringIsBoundedAgainstAnAbsurdName(t *testing.T) {
	left := "Acme " + strings.Repeat("x", 400_000)
	right := "Acme " + strings.Repeat("y", 400_000)

	start := time.Now()
	score := nameSimilarity(normalizeName(left), normalizeName(right))
	// Unbounded this pair is minutes of CPU; bounded it is microseconds. A
	// second is a ceiling generous enough that a loaded machine cannot trip it
	// and tight enough that losing the bound cannot pass.
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("scoring two 400k-rune names took %v — the quadratic bound is gone", elapsed)
	}
	// The bound caps the work, not the contract: the answer is still a score.
	if score < 0 || score > 1 {
		t.Errorf("score = %v, want a value in [0,1]", score)
	}
}

// The bound belongs to the metric alone. normalizeName also produces
// exact-match and grouping keys, and truncating there would make two distinct
// long names compare EQUAL as keys — a match rather than a capped score, which
// is not a bound's business.
func TestNormalizingANameDoesNotTruncateIt(t *testing.T) {
	long := "Acme " + strings.Repeat("x", 400_000)
	if got := len([]rune(normalizeName(long))); got < 400_000 {
		t.Fatalf("normalizeName truncated to %d runes — keys derived from it would collide", got)
	}
	// Two names that differ only past the bound: one score, still two keys.
	a := strings.Repeat("a", nameScoringMaxRunes) + "left"
	b := strings.Repeat("a", nameScoringMaxRunes) + "right"
	if score := nameSimilarity(a, b); score != 1 {
		t.Errorf("score = %.4f, want 1.0 — past the bound the metric sees one name", score)
	}
	if normalizeName(a) == normalizeName(b) {
		t.Error("the two names produced one key — the bound leaked out of the metric")
	}
}

// A realistic name must never reach the bound at all.
func TestAPlausibleNameIsFarInsideTheScoringBound(t *testing.T) {
	plausible := "Internationale Handelsgesellschaft für Bauwesen und Anlagenbau GmbH & Co. KG"
	normalized := normalizeName(plausible)
	if len([]rune(normalized)) >= nameScoringMaxRunes {
		t.Fatalf("a realistic name normalized to %d runes, at or past the %d bound — the bound is too tight",
			len([]rune(normalized)), nameScoringMaxRunes)
	}
	if !strings.HasSuffix(normalized, "kg") {
		t.Errorf("normalized %q lost its tail", normalized)
	}
}

// The name lock is owed whenever an evidence apply COULD write a name, and the
// loop that writes it keys off the field being present, not off its value
// carrying one. Clearing a legal name to "" still writes the row and still ends
// in the re-check, so gating the lock on the value would let that case take the
// row lock first and reach for the name lock second — the ordering that
// deadlocks against a human rename.
func TestAnEvidenceApplyOwesTheNameLockWheneverItCarriesANameField(t *testing.T) {
	cases := []struct {
		name   string
		fields []ColdStartFieldInput
		owes   bool
	}{
		{"a stated legal name", []ColdStartFieldInput{{Field: "legal_name", Value: "Baqend GmbH"}}, true},
		{"a CLEARED legal name", []ColdStartFieldInput{{Field: "legal_name", Value: ""}}, true},
		{"legal name among others", []ColdStartFieldInput{
			{Field: "industry", Value: "software"}, {Field: "legal_name", Value: "Acme AG"},
		}, true},
		{"no name field at all", []ColdStartFieldInput{
			{Field: "industry", Value: "software"}, {Field: "registered_address", Value: "Hamburg"},
		}, false},
		{"nothing applied", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := carriesOrgName(tc.fields); got != tc.owes {
				t.Errorf("carriesOrgName = %v, want %v", got, tc.owes)
			}
		})
	}
}
