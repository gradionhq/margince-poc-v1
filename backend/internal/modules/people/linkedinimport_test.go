// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

import (
	"slices"
	"testing"
)

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

// The company strings that reached no account on a real 5,064-row export, and
// the key each one needs. Every case here is a company that EXISTS in the CRM
// and was missed because a member wrote their employer the way people do.
func TestOrgMatchKeysReachTheAccountsThatWereMissed(t *testing.T) {
	for _, tc := range []struct{ company, account string }{
		{"Wortfilter.de", "wortfilter"},
		{"The Sentry", "thesentry"},
		{"pinops consumer research", "pinops"},
		{"brickfox Multichannel eCommerce", "brickfox"},
		{"SIMIO GmbH & Co. KG | Geschäftsführender Gesellschafter", "simio"},
		{"100 DAYS software projects GmbH", "100 days software projects"},
		{"marcos software", "marcossoftware"},
	} {
		if !slices.Contains(orgMatchKeys(tc.company), tc.account) {
			t.Errorf("%q → %v, missing the key %q that reaches its account",
				tc.company, orgMatchKeys(tc.company), tc.account)
		}
	}
}

// The exact key is always first, so a company whose full name IS an account
// can never be resolved by a looser fallback to a different one.
func TestOrgMatchKeysPutTheExactNameFirst(t *testing.T) {
	keys := orgMatchKeys("Acme Consulting GmbH")
	if len(keys) == 0 || keys[0] != "acme consulting" {
		t.Fatalf("keys = %v, want the exact name first", keys)
	}
}

// A string that names no company must produce nothing an account can match.
// These are real values from the same export: a member between jobs, a
// placeholder, a description. Matching them would attach a colleague's whole
// network to whichever account happened to share a word.
func TestOrgMatchKeysRefuseWhatIsNotACompany(t *testing.T) {
	for _, notACompany := range []string{"", "   ", ".", "|", "..."} {
		if keys := orgMatchKeys(notACompany); len(keys) != 0 {
			t.Errorf("%q → %v, want no keys at all", notACompany, keys)
		}
	}
}
