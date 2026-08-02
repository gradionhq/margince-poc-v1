// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

import "testing"

func TestDomainLooksPersonalNeedsEveryKnownPersonToExplainTheLabel(t *testing.T) {
	cases := []struct {
		name    string
		label   string
		persons []DomainPerson
		want    bool
	}{
		{
			// kestner.example: the label is the sender's surname outright.
			name:    "the surname is the domain",
			label:   "kestner",
			persons: []DomainPerson{{FullName: "Sebastian Kestner", EmailLocal: "sebastian"}},
			want:    true,
		},
		{
			// rowanmarsh.example: the header display name is "Ines Marsh" and
			// explains only half of it. The local part explains the rest, which
			// is why the address and not just the name has to be looked at.
			name:    "the local part plus the surname is the domain",
			label:   "rowanmarsh",
			persons: []DomainPerson{{FullName: "Ines Marsh", EmailLocal: "rowan"}},
			want:    true,
		},
		{
			name:    "first and last run together",
			label:   "olivia-larsen",
			persons: []DomainPerson{{FullName: "Olivia Larsen", EmailLocal: "info"}},
			want:    true,
		},
		{
			name:    "last and first run together",
			label:   "hausen-kai",
			persons: []DomainPerson{{FullName: "Kai Hausen", EmailLocal: "kai"}},
			want:    true,
		},
		{
			name:    "a transliterated surname does not match the accented name",
			label:   "mueller",
			persons: []DomainPerson{{FullName: "Christina Müller", EmailLocal: "cm"}},
			// "mueller" is a TRANSLITERATION of Müller, not an unaccenting of
			// it — normalizeName yields "muller". Whether to fold German
			// digraphs is a question for the whole dedupe metric, not something
			// to answer once, here.
			want: false,
		},
		{
			name:    "a company domain nobody's name explains",
			label:   "basecom",
			persons: []DomainPerson{{FullName: "Martin Weiss", EmailLocal: "m.wortmann"}},
			want:    false,
		},
		{
			// Two unrelated family names on one domain is what a company IS.
			// The employer stapled onto each display name must not read as
			// their surname, or both would "match" and the company would be
			// refused its organization.
			name:  "a shared domain is a company, and a stapled employer is not a surname",
			label: "tvpartner",
			persons: []DomainPerson{
				{FullName: "Tomas Vidal - TVPartner", EmailLocal: "guido"},
				{FullName: "Karin Vogt - TVPartner", EmailLocal: "annett"},
			},
			want: false,
		},
		{
			// A mailbox named after the company is not a person, and it is the
			// one case where the local part alone equals the label.
			name:    "a role address on its own domain is not a person",
			label:   "tvpartner",
			persons: []DomainPerson{{FullName: "Tomas Vidal", EmailLocal: "tvpartner"}},
			want:    false,
		},
		{
			name:    "an employer in parentheses is not a surname either",
			label:   "cloud-atlas",
			persons: []DomainPerson{{FullName: "Nikos Adamos (Cloud Atlas)", EmailLocal: "stergios"}},
			want:    false,
		},
		{
			name:    "nobody known is not evidence of anything",
			label:   "kestner",
			persons: nil,
			want:    false,
		},
		{
			name:    "an empty label answers no rather than matching everything",
			label:   "",
			persons: []DomainPerson{{FullName: "Sebastian Kestner", EmailLocal: "sebastian"}},
			want:    false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := DomainLooksPersonal(tc.label, tc.persons); got != tc.want {
				t.Errorf("DomainLooksPersonal(%q, %v) = %v, want %v", tc.label, tc.persons, got, tc.want)
			}
		})
	}
}

func TestDomainLooksPersonalMisjudgesAnEponymousAgency(t *testing.T) {
	// baumert.example is Alexander Baumert's agency and the heuristic cannot tell
	// it from a personal domain — the label IS his surname. It answers yes, and
	// the organization is lost.
	//
	// That is why this heuristic runs ONLY when no site could be read: for
	// baumert.example a site loads, states a company, and the crawl answers first.
	// The test pins the limit so nobody later mistakes the heuristic for a
	// standalone rule.
	if !DomainLooksPersonal("baumert", []DomainPerson{{FullName: "Alexander Baumert", EmailLocal: "alexander"}}) {
		t.Fatal("the heuristic is expected to misjudge an eponymous agency; if it no longer does, the comment above is stale")
	}
}
