// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package signals

// The raw-ref parser is the resolver's front door: it decides what a
// captured source pointer can be attributed to (email, domain, name) —
// or nothing, in which case the signal is dropped, never guessed. The
// SQL matching around it has its own integration suite; here the pure
// classification is the spec.

import (
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func mustID(t *testing.T, s string) ids.OrganizationID {
	t.Helper()
	id, err := ids.ParseAs[ids.OrganizationKind](s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return id
}

func TestParseRawRefClassifiesAttributableAndDropsNoise(t *testing.T) {
	cases := []struct {
		name              string
		raw               string
		email, domain, nm string
	}{
		{"namespaced email", "inbound:Sam@Acme.Example", "sam@acme.example", "acme.example", ""},
		{"bare email strips www off the host", "hi@www.acme.example", "hi@www.acme.example", "acme.example", ""},
		{"https url is a domain only", "web:https://www.acme.example/pricing", "", "acme.example", ""},
		{"bare domain", "acme.example", "", "acme.example", ""},
		{"free-text mention is a name", "Acme GmbH", "", "", "Acme GmbH"},
		{"empty payload attributes nothing", "inbound:", "", "", ""},
		{"whitespace attributes nothing", "   ", "", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseRawRef(tc.raw)
			if got.Email != tc.email || got.Domain != tc.domain || got.Name != tc.nm {
				t.Fatalf("parseRawRef(%q) = {email:%q domain:%q name:%q}, want {email:%q domain:%q name:%q}",
					tc.raw, got.Email, got.Domain, got.Name, tc.email, tc.domain, tc.nm)
			}
		})
	}
}

func TestSortCandidatesIsDeterministicByConfidenceThenID(t *testing.T) {
	// Two candidates at the same confidence must order by id, so the same
	// evidence always reports the same top candidate (no flapping).
	low := candidate{OrgID: mustID(t, "00000000-0000-7000-8000-000000000002"), Confidence: 0.90}
	highA := candidate{OrgID: mustID(t, "00000000-0000-7000-8000-000000000001"), Confidence: 0.95}
	highB := candidate{OrgID: mustID(t, "00000000-0000-7000-8000-000000000003"), Confidence: 0.95}
	cs := []candidate{low, highB, highA}
	sortCandidates(cs)
	if cs[0] != highA || cs[1] != highB || cs[2] != low {
		t.Fatalf("sort order = %v, want [highA highB low] (confidence desc, id asc)", cs)
	}
}
