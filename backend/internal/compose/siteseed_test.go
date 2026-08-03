// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"slices"
	"testing"
)

func TestSeedFallbacksWalksTheSpellingsOfTheSameSite(t *testing.T) {
	got := seedFallbacks("https://acme.com")
	want := []string{"https://www.acme.com", "http://acme.com", "http://www.acme.com"}
	if !slices.Equal(got, want) {
		t.Errorf("seedFallbacks = %v, want %v", got, want)
	}
}

func TestSeedFallbacksOffersTheApexWhenTheSeedCarriesWWW(t *testing.T) {
	got := seedFallbacks("https://www.acme.com")
	want := []string{"https://acme.com", "http://www.acme.com", "http://acme.com"}
	if !slices.Equal(got, want) {
		t.Errorf("seedFallbacks = %v, want %v", got, want)
	}
}

// A label is never STRIPPED except the exact `www` prefix: dropping any other
// one points at a host that may not be this company at all.
func TestSeedFallbacksNeverStripsALabelThatIsNotWWW(t *testing.T) {
	for _, seed := range []string{"https://careers.acme.com", "https://shop.acme.co.uk"} {
		for _, candidate := range seedFallbacks(seed) {
			for _, forbidden := range []string{
				"https://acme.com", "https://www.acme.com",
				"https://acme.co.uk", "https://www.acme.co.uk",
			} {
				if candidate == forbidden {
					t.Errorf("seed %q produced %q — a different host", seed, candidate)
				}
			}
		}
	}
}

// A multi-label public suffix is not a subdomain. Counting dots called
// acme.co.uk one and skipped its www spelling, which is where a good share of
// UK and German companies actually publish.
func TestSeedFallbacksOffersWWWForAMultiLabelSuffix(t *testing.T) {
	got := seedFallbacks("https://acme.co.uk")
	if !slices.Contains(got, "https://www.acme.co.uk") {
		t.Errorf("seedFallbacks = %v, want the www spelling offered", got)
	}
}

func TestSeedFallbacksKeepsThePathAndNeverRepeatsTheSeed(t *testing.T) {
	for _, candidate := range seedFallbacks("https://acme.com/de") {
		if candidate == "https://acme.com/de" {
			t.Fatal("the seed itself must never be offered as its own fallback")
		}
		if !slices.Contains([]string{
			"https://www.acme.com/de", "http://acme.com/de", "http://www.acme.com/de",
		}, candidate) {
			t.Errorf("candidate %q dropped or changed the path", candidate)
		}
	}
}

func TestSeedFallbacksRefusesWhatItCannotParseOrShouldNotFetch(t *testing.T) {
	for _, seed := range []string{"", "://nope", "ftp://acme.com", "file:///etc/passwd", "not a url"} {
		if got := seedFallbacks(seed); len(got) != 0 {
			t.Errorf("seedFallbacks(%q) = %v, want none", seed, got)
		}
	}
}
