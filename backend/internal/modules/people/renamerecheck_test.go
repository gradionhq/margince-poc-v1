// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

import "testing"

// The lock key has to be coarse enough that two names which SCORE against each
// other take the same key — otherwise the guard serializes only exact-equal
// names while the tier it protects matches on similarity, and every near-match
// still races. Each case below pairs a lock-key expectation with the score the
// fuzzy tier would give, so the two cannot drift apart silently.
func TestOrganizationNameLockKeyCoversWhatTheFuzzyTierMatches(t *testing.T) {
	cases := []struct {
		name         string
		left, right  string
		sameKey      bool
		wouldCollide bool
	}{
		{"legal suffix stripped", "Baqend", "Baqend GmbH", true, true},
		{"trailing qualifier", "Baqend", "Baqend Solutions", true, true},
		// Two genuinely different companies, yet the tier scores them 0.76 —
		// above the threshold. They are a review pair in their own right, so
		// sharing a lock key costs nothing they were not already going to file.
		{"different companies sharing a first word", "Acme Logistics", "Acme Bakery", true, true},
		{"unrelated names", "Baqend", "Zorbatron Heavy Industry", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			leftKey, rightKey := orgNameLockKey(tc.left), orgNameLockKey(tc.right)
			if got := leftKey == rightKey; got != tc.sameKey {
				t.Errorf("same lock key = %v (%q vs %q), want %v", got, leftKey, rightKey, tc.sameKey)
			}
			score := nameSimilarity(NormalizeOrgName(tc.left), NormalizeOrgName(tc.right))
			collides := score >= dedupeReviewThreshold
			if collides != tc.wouldCollide {
				t.Errorf("fuzzy score %.4f collides = %v, want %v", score, collides, tc.wouldCollide)
			}
			// The invariant the guard rests on: anything the tier would file
			// must have been serialized first.
			if collides && leftKey != rightKey {
				t.Errorf("%q and %q score %.4f (>= %.2f) but take different lock keys %q/%q — "+
					"the pair can race and both land unfiled",
					tc.left, tc.right, score, dedupeReviewThreshold, leftKey, rightKey)
			}
		})
	}
}

// A name that normalizes to nothing takes no lock rather than an empty one:
// every such create would otherwise serialize on one shared key. A name that
// is ONLY a legal form keeps it — NormalizeOrgName strips a trailing suffix
// only when something else remains, so "GmbH" is a name like any other.
func TestOrganizationNameLockKeyIsEmptyWhenThereIsNoName(t *testing.T) {
	for _, name := range []string{"", "   "} {
		if key := orgNameLockKey(name); key != "" {
			t.Errorf("orgNameLockKey(%q) = %q, want empty", name, key)
		}
	}
}
