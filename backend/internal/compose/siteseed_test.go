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

// A host that is already a subdomain is not a www convention. Stripping its
// label would point at a different host, which may not be this company at all.
func TestSeedFallbacksNeverInventsAHostForASubdomainSeed(t *testing.T) {
	for _, seed := range []string{"https://careers.acme.com", "https://acme.co.uk"} {
		for _, candidate := range seedFallbacks(seed) {
			if candidate == "https://acme.com" || candidate == "https://www.acme.com" {
				t.Errorf("seed %q produced %q — a different host", seed, candidate)
			}
		}
	}
	// acme.co.uk has two dots, so no www is guessed; only the scheme varies.
	if got, want := seedFallbacks("https://acme.co.uk"), []string{"http://acme.co.uk"}; !slices.Equal(got, want) {
		t.Errorf("seedFallbacks = %v, want %v", got, want)
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
