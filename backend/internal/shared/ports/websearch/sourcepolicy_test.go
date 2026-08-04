// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package websearch

import "testing"

// The deny-list is the seam's load-bearing promise, so it is asserted rather
// than trusted: a regression here is the difference between a product that
// cites LinkedIn and one that scrapes it.
func TestMayFetchRefusesTheAuthWalledPlatforms(t *testing.T) {
	refused := []string{
		"https://www.linkedin.com/in/anna-weber",
		"https://linkedin.com/in/anna-weber",
		"https://de.linkedin.com/in/anna-weber",
		"https://www.xing.com/profile/Anna_Weber",
		"http://facebook.com/someone",
		// The fully-qualified spellings. DNS resolves these to the same
		// servers, so a policy that reads them as different hosts is a
		// bypass rather than a nicety.
		"https://linkedin.com./in/anna-weber",
		"https://WWW.LinkedIn.COM./in/anna-weber",
		"https://de.linkedin.com../in/anna-weber",
	}
	for _, raw := range refused {
		if d := MayFetch(raw); d.Allowed {
			t.Errorf("MayFetch(%q) allowed the fetch; the platform is deny-listed", raw)
		}
	}
}

func TestMayFetchAllowsAnOrdinaryPublicPage(t *testing.T) {
	allowed := []string{
		"https://scalecommerce.example/team",
		"https://www.neuland-bfi.de/de/ueber-uns/partner",
		"http://example.de/impressum",
	}
	for _, raw := range allowed {
		d := MayFetch(raw)
		if !d.Allowed {
			t.Errorf("MayFetch(%q) refused a public page: %s", raw, d.Reason)
		}
	}
}

// A URL the policy cannot decompose is refused rather than passed through:
// guessing here fetches the thing the policy exists to avoid.
func TestMayFetchRefusesWhatItCannotParse(t *testing.T) {
	for _, raw := range []string{"", "   ", "not a url", "ftp://example.com/x", "mailto:a@b.c"} {
		if d := MayFetch(raw); d.Allowed {
			t.Errorf("MayFetch(%q) allowed an address it could not apply policy to", raw)
		}
	}
}

// Denied for fetching and citable are different questions. A LinkedIn URL in
// a search result is where the claim lives, and saying so costs nobody
// anything — throwing it away would discard the metadata that makes
// discovery useful without a fetch.
func TestADeniedHostIsStillCitable(t *testing.T) {
	r := Result{URL: "https://www.linkedin.com/in/anna-weber", Title: "Anna Weber — Head of Procurement"}
	if MayFetch(r.URL).Allowed {
		t.Fatal("the fixture is wrong: this host must be deny-listed for fetching")
	}
	if !Citable(r) {
		t.Error("a deny-listed host must still be citable — the URL is evidence of where the claim appears")
	}
}
