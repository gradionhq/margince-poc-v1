// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

import "testing"

// LinkedIn's company field is a member-edited headline, not a company name.
// These are real strings from a 5,064-row export: every one of them failed to
// reach an account that exists, because the tagline or the styling made the
// key something nobody stores a customer under.
func TestLinkedInCompanyHeadlinesReachTheAccount(t *testing.T) {
	for _, tc := range []struct{ headline, want string }{
		{".NFQ | Digital Creatives", "nfq"},
		{"tagtu | Result-Driven Business Travel", "tagtu"},
		{"basecom GmbH & Co. KG", "basecom"},
		{"CONRADFILM GmbH", "conradfilm"},
		{"MediaMarktSaturn", "mediamarktsaturn"},
		// A plain name survives untouched.
		{"Acme", "acme"},
	} {
		if got := NormalizeOrgName(cleanLinkedInCompany(tc.headline)); got != tc.want {
			t.Errorf("%q → %q, want %q", tc.headline, got, tc.want)
		}
	}
}
