// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

import "testing"

// companyHost is the ONE reducer the organization_domain index is keyed on: the
// write path stores what it returns, and the resolve read looks up what it
// returns. Every spelling of one company therefore has to come out the same, or
// a read and a write disagree about which row they are holding — and two
// spellings on the write side alone are two organizations for one company,
// which is the duplicate this module exists to prevent.
func TestCompanyHostReducesEverySpellingOfOneCompanyToOneKey(t *testing.T) {
	for _, website := range []string{
		"acme.example",
		"ACME.example",
		"www.acme.example",
		"https://acme.example",
		"https://www.acme.example/about",
		"http://www.ACME.example/careers?ref=x",
		// The FQDN root form, at the end of the string and inside a URL. The
		// second is the one a string trim never reaches.
		"acme.example.",
		"www.acme.example.",
		"https://www.acme.example./about",
	} {
		t.Run(website, func(t *testing.T) {
			host, err := companyHost(website)
			if err != nil {
				t.Fatalf("companyHost(%q): %v", website, err)
			}
			if host != "acme.example" {
				t.Errorf("companyHost(%q) = %q, want acme.example", website, host)
			}
		})
	}
}

// A website with no host is refused rather than stored. A bare dot reduces to
// nothing, and an empty key would match the first row with an empty domain.
func TestCompanyHostRefusesAWebsiteWithNoHost(t *testing.T) {
	for _, website := range []string{"", ".", "https://", "https:///path"} {
		t.Run(website, func(t *testing.T) {
			if host, err := companyHost(website); err == nil {
				t.Errorf("companyHost(%q) = %q, want a refusal", website, host)
			}
		})
	}
}
