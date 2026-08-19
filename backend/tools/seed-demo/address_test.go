// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

import (
	"strings"
	"testing"
)

// TestParseAddressOnTheShapesTheDatasetHolds pins the real cases. Every value
// here was taken from a company's accepted.json, so a change that breaks one
// breaks a company's address rather than a hypothetical.
func TestParseAddressOnTheShapesTheDatasetHolds(t *testing.T) {
	for _, tc := range []struct {
		name    string
		printed string
		want    address
	}{
		{
			name:    "the plain German form",
			printed: "Adessoplatz 1 44269 Dortmund",
			want:    address{Line1: "Adessoplatz 1", PostalCode: "44269", City: "Dortmund"},
		},
		{
			name:    "a country prefix with spaced dashes",
			printed: "Ainmillerstrasse 22 D – 80801 München",
			want:    address{Line1: "Ainmillerstrasse 22", PostalCode: "80801", City: "München", Country: "DE"},
		},
		{
			name:    "a Swiss four-digit postcode and its prefix",
			printed: "Bundesplatz 16 CH-6300 Zug Switzerland",
			want:    address{Line1: "Bundesplatz 16", PostalCode: "6300", City: "Zug", Country: "CH"},
		},
		{
			name:    "an Austrian one",
			printed: "Passaustrasse 26-28 A-4030 Linz Austria",
			want:    address{Line1: "Passaustrasse 26-28", PostalCode: "4030", City: "Linz", Country: "AT"},
		},
		{
			name:    "a floor between the street and the postcode",
			printed: "Charlottenstraße 4 12th Floor 10969 Berlin Germany",
			want:    address{Line1: "Charlottenstraße 4 12th Floor", PostalCode: "10969", City: "Berlin", Country: "DE"},
		},
		{
			name:    "a city with a slash in its name",
			printed: "Neue Rothofstrasse 13-19 60313 Frankfurt/Main Germany",
			want:    address{Line1: "Neue Rothofstrasse 13-19", PostalCode: "60313", City: "Frankfurt/Main", Country: "DE"},
		},
		{
			name:    "a city of three words",
			printed: "Richard-Wagner-Straße 14c 84453 Mühldorf am Inn",
			want:    address{Line1: "Richard-Wagner-Straße 14c", PostalCode: "84453", City: "Mühldorf am Inn"},
		},
		{
			name:    "a building name before the street",
			printed: "Hamburg Business Center Poststraße 33 20354 Hamburg",
			want:    address{Line1: "Hamburg Business Center Poststraße 33", PostalCode: "20354", City: "Hamburg"},
		},
		{
			name:    "commas between every part",
			printed: "Domstraße 20, D-50668 Cologne, Germany",
			want:    address{Line1: "Domstraße 20", PostalCode: "50668", City: "Cologne", Country: "DE"},
		},
		{
			name:    "the German word for the country",
			printed: "Industriestrasse 50a 8304 Wallisellen Schweiz",
			want:    address{Line1: "Industriestrasse 50a", PostalCode: "8304", City: "Wallisellen", Country: "CH"},
		},
		{
			// One company's page really does carry a word joiner here, and it
			// sits exactly where the postcode match has to begin.
			name:    "an invisible word joiner before the postcode",
			printed: "Viktoriastraße 3b ⁠ 86150 Augsburg",
			want:    address{Line1: "Viktoriastraße 3b", PostalCode: "86150", City: "Augsburg"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := parseAddress(tc.printed)
			if got != tc.want {
				t.Errorf("parseAddress(%q)\n got %+v\nwant %+v", tc.printed, got, tc.want)
			}
		})
	}
}

// TestParseAddressKeepsWhatItCannotSplit is the honest-degradation rule. A
// company whose street is right and whose city is empty is a partial record;
// one whose city was invented is a wrong record, and the dataset's whole
// no-guess posture says which of those is acceptable.
func TestParseAddressKeepsWhatItCannotSplit(t *testing.T) {
	for _, tc := range []struct {
		printed   string
		wantLine1 string
		wantCC    string
	}{
		{
			printed:   "354 Oyster Point Boulevard South San Francisco, California, 94080, USA",
			wantLine1: "354 Oyster Point Boulevard South San Francisco, California, 94080",
			wantCC:    "US",
		},
		{
			printed:   "55 Baker Street, London, W1U 8EW, United Kingdom",
			wantLine1: "55 Baker Street, London, W1U 8EW",
			wantCC:    "GB",
		},
	} {
		got := parseAddress(tc.printed)
		if got.Line1 != tc.wantLine1 {
			t.Errorf("line1 = %q, want %q", got.Line1, tc.wantLine1)
		}
		if got.Country != tc.wantCC {
			t.Errorf("country = %q, want %q", got.Country, tc.wantCC)
		}
		if got.City != "" || got.PostalCode != "" {
			t.Errorf("a shape this cannot read must not produce a city or postcode, got %+v", got)
		}
	}
}

// TestParseAddressNeverInventsACountry — the country is stated by the page or
// it is absent. A postcode range is not a country, and deriving one would be
// the same guessing the crawl's own gates exist to refuse.
func TestParseAddressNeverInventsACountry(t *testing.T) {
	for _, printed := range []string{
		"Adessoplatz 1 44269 Dortmund",
		"Wipplingerstraße 23 1010 Wien",
		"Schönhauser Allee 124 10437 Berlin",
	} {
		if got := parseAddress(printed); got.Country != "" {
			t.Errorf("parseAddress(%q) invented country %q", printed, got.Country)
		}
	}
}

// TestAddressBodySendsOnlyWhatWasPrinted keeps a partial address partial: an
// absent city is left out of the request rather than sent as an empty string,
// which the API would store as a city nobody named.
func TestAddressBodySendsOnlyWhatWasPrinted(t *testing.T) {
	if body := addressBody(address{}); body != nil {
		t.Errorf("an empty address produced a body: %v", body)
	}
	body := addressBody(parseAddress("55 Baker Street, London, W1U 8EW, United Kingdom"))
	if _, ok := body["city"]; ok {
		t.Error("an unparsed city was sent anyway")
	}
	if body["country"] != "GB" {
		t.Errorf("country = %v, want GB", body["country"])
	}
	if body["line1"] == "" {
		t.Error("the printed address was not sent at all")
	}
}

// TestParseAddressRefusesACountryOnValueWithNoAddress — a value that is
// nothing but a country word describes no address, and sending the country
// alone would file a company in Germany with no street, city or postcode.
// That reads as an address on every screen that shows one.
func TestParseAddressRefusesACountryOnValueWithNoAddress(t *testing.T) {
	for _, printed := range []string{"Germany", "Deutschland", " , Austria ", ""} {
		got := parseAddress(printed)
		if !got.empty() {
			t.Errorf("parseAddress(%q) = %+v, want nothing", printed, got)
		}
		if addressBody(got) != nil {
			t.Errorf("parseAddress(%q) produced a request body", printed)
		}
	}
}

// TestParseAddressTakesTheLastPostcodeNotTheFirst — a postcode ends a DACH
// address, and an earlier digit run is something that came before the street:
// a suite, a building number, a PO box. Reading the first match turned
// "Suite 1200 Hauptstrasse 5 80331 München" into postcode 1200 in a city
// called Hauptstrasse.
func TestParseAddressTakesTheLastPostcodeNotTheFirst(t *testing.T) {
	for _, tc := range []struct {
		printed string
		want    address
	}{
		{
			printed: "Suite 1200 Hauptstrasse 5 80331 München",
			want:    address{Line1: "Suite 1200 Hauptstrasse 5", PostalCode: "80331", City: "München"},
		},
		{
			printed: "Gebäude 4000 Nord Musterweg 2 12345 Ort",
			want:    address{Line1: "Gebäude 4000 Nord Musterweg 2", PostalCode: "12345", City: "Ort"},
		},
	} {
		if got := parseAddress(tc.printed); got != tc.want {
			t.Errorf("parseAddress(%q)\n got %+v\nwant %+v", tc.printed, got, tc.want)
		}
	}
}

// TestCleanPrintedAddressKeepsJoinersThatAreSpelling — U+200C and U+200D look
// like the same class of invisible character as a word joiner and are not: in
// Persian and the Indic scripts they control joining and are part of the
// spelling. Dropping them would corrupt an address rather than clean it.
func TestCleanPrintedAddressKeepsJoinersThatAreSpelling(t *testing.T) {
	withZWNJ := "خیابان‌ولیعصر 12"
	if got := cleanPrintedAddress(withZWNJ); !strings.Contains(got, "‌") {
		t.Errorf("a zero-width non-joiner was stripped from %q -> %q", withZWNJ, got)
	}
	// The word joiner one real page prints IS removed.
	if got := cleanPrintedAddress("Viktoriastraße 3b⁠ 86150"); strings.Contains(got, "⁠") {
		t.Errorf("the word joiner survived: %q", got)
	}
}
