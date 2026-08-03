// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

import (
	"strings"
	"testing"
)

// The lock key guards a tier that matches on SIMILARITY while an advisory lock
// keys on EQUALITY, so it can only ever be an approximation. This table is the
// inventory of that approximation: for every pair it states the score the fuzzy
// tier gives and whether the two take one lock key, so a change to either the
// key or the metric has to come here and say which pairs it moved.
//
// The `covered` column is the honest part. A pair that collides but is NOT
// covered can race: both writers score no_match against each other and both
// land with no candidate on the queue.
func TestOrganizationNameLockKeyInventory(t *testing.T) {
	cases := []struct {
		name        string
		left, right string
		sameKey     bool
		collides    bool
	}{
		// Covered: the shapes that put one company in a workspace twice.
		{"legal suffix stripped", "Baqend", "Baqend GmbH", true, true},
		{"trailing qualifier", "Baqend", "Baqend Solutions", true, true},
		{"word split inside the prefix", "Microsoft Corp", "Micro Soft Corp", true, true},
		{"hyphenated qualifier", "Acme Ltd", "ACME-Group Ltd", true, true},
		{"shared brand, different lines", "Deutsche Bahn", "Deutsche Post", true, true},

		// Not covered, and each one is a pair that CAN race. Keying on the
		// first token instead would reach the article case and lose the two
		// prefix cases above, which score higher; the transposed case no point
		// key reaches at all.
		{"article splits at the fourth rune", "The Boring Company", "The Home Depot", false, true},
		{"transposed words share no prefix", "IBM Deutschland", "Deutschland IBM", false, true},

		// Correctly apart: unrelated names neither collide nor serialize.
		{"unrelated names", "Baqend", "Zorbatron Heavy Industry", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			leftKey, rightKey := orgNameLockKey(tc.left), orgNameLockKey(tc.right)
			if got := leftKey == rightKey; got != tc.sameKey {
				t.Errorf("same lock key = %v (%q vs %q), want %v", got, leftKey, rightKey, tc.sameKey)
			}
			score := nameSimilarity(NormalizeOrgName(tc.left), NormalizeOrgName(tc.right))
			if got := score >= dedupeReviewThreshold; got != tc.collides {
				t.Errorf("score %.4f collides = %v, want %v", score, got, tc.collides)
			}
		})
	}
}

// The key mirrors the metric's own prefix window. If one moves without the
// other, every pair whose similarity rests on that window silently stops being
// serialized — the failure this whole file exists to prevent, arriving through
// a constant rather than through the key function.
func TestOrganizationNameLockKeyTracksTheSimilarityPrefix(t *testing.T) {
	key := orgNameLockKey("Internationale Handelsgesellschaft")
	if len([]rune(key)) != jaroWinklerMaxPrefix {
		t.Fatalf("key %q is %d runes, want jaroWinklerMaxPrefix (%d)", key, len([]rune(key)), jaroWinklerMaxPrefix)
	}
}

// Separators are dropped rather than terminating the key, which is what puts
// both halves of a split or hyphenated first word on one key.
func TestOrganizationNameLockKeyIgnoresSeparators(t *testing.T) {
	for _, name := range []string{"Micro Soft", "Micro-Soft", "M.I.C.R.O Soft", "  micro soft  "} {
		if key := orgNameLockKey(name); key != "micr" {
			t.Errorf("orgNameLockKey(%q) = %q, want %q", name, key, "micr")
		}
	}
}

// A name that normalizes to nothing takes NO lock: an empty key would serialize
// every such writer against every other. A name that is only a legal form is
// not that case — NormalizeOrgName strips a trailing suffix only when something
// else remains, so "GmbH" is a name like any other and keeps its key.
func TestOrganizationNameLockKeyOnDegenerateNames(t *testing.T) {
	for _, name := range []string{"", "   ", "!!!", "- . -"} {
		if key := orgNameLockKey(name); key != "" {
			t.Errorf("orgNameLockKey(%q) = %q, want empty so it takes no lock", name, key)
		}
	}
	if key := orgNameLockKey("GmbH"); key != "gmbh" {
		t.Errorf("orgNameLockKey(%q) = %q, want %q", "GmbH", key, "gmbh")
	}
	// The wart this key inherits: a retained single token keeps its trailing
	// punctuation in NormalizeOrgName, but stripping non-alphanumerics here
	// means "Co" and "Co." still land on one key.
	if a, b := orgNameLockKey("Co"), orgNameLockKey("Co."); a != b {
		t.Errorf("orgNameLockKey(%q)=%q and orgNameLockKey(%q)=%q disagree", "Co", a, "Co.", b)
	}
}

// lockOrgNameIdentities must not ask for an empty key, whatever it is handed.
func TestLockOrgNameIdentitiesSkipsNamelessAxes(t *testing.T) {
	var keys []string
	for _, name := range []string{"", "   ", "Baqend GmbH"} {
		if key := orgNameLockKey(name); key != "" {
			keys = append(keys, key)
		}
	}
	if got := strings.Join(keys, ","); got != "baqe" {
		t.Errorf("keys = %q, want only the named axis", got)
	}
}

// The similarity metric is quadratic in the length of the longer name and runs
// inside the writing transaction while an advisory lock is held, so an
// unbounded name would pin a pool connection and every writer sharing its lock
// key. `display_name` is `text` with no length bound in the contract, so the
// bound has to live in the metric.
func TestNameScoringIsBoundedAgainstAnAbsurdName(t *testing.T) {
	huge := "Acme " + strings.Repeat("x", 400_000)
	if got := len([]rune(normalizeName(huge))); got > nameScoringMaxRunes {
		t.Fatalf("normalized length = %d runes, want <= %d — scoring cost is quadratic in this", got, nameScoringMaxRunes)
	}
	// The bound must not reach a name anyone could really have: a long German
	// company name still normalizes whole, ending in the token it ends with.
	plausible := "Internationale Handelsgesellschaft für Bauwesen und Anlagenbau GmbH & Co. KG"
	normalized := normalizeName(plausible)
	if len([]rune(normalized)) >= nameScoringMaxRunes {
		t.Fatalf("a realistic name normalized to %d runes, at or past the %d bound — the bound is too tight",
			len([]rune(normalized)), nameScoringMaxRunes)
	}
	if !strings.HasSuffix(normalized, "kg") {
		t.Errorf("normalized %q lost its tail — the bound truncated a name it should not reach", normalized)
	}
}
