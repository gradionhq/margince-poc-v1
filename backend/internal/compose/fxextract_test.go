// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import "testing"

func TestFxAnchor(t *testing.T) {
	cases := []struct {
		name            string
		base            string
		pair            extractedFxPair
		wantCur         string
		wantInv, wantOK bool
	}{
		// base on the to-side: "1 USD = 0.92 EUR" is USD->EUR directly.
		{
			"base_as_to_direct", "EUR",
			extractedFxPair{FromCurrency: "USD", ToCurrency: "EUR"},
			"USD", false, true,
		},
		// base on the from-side: "1 EUR = 1.08 USD" must be inverted for USD->EUR.
		{
			"base_as_from_inverts", "EUR",
			extractedFxPair{FromCurrency: "EUR", ToCurrency: "USD"},
			"USD", true, true,
		},
		// neither side is the base — not anchorable.
		{
			"cross_pair", "EUR",
			extractedFxPair{FromCurrency: "USD", ToCurrency: "GBP"},
			"", false, false,
		},
		// degenerate from==to.
		{
			"same_currency", "EUR",
			extractedFxPair{FromCurrency: "EUR", ToCurrency: "EUR"},
			"", false, false,
		},
		// lowercase codes are normalized.
		{
			"lowercase_ok", "EUR",
			extractedFxPair{FromCurrency: "usd", ToCurrency: "eur"},
			"USD", false, true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cur, inv, ok := fxAnchor(tc.base, tc.pair)
			if ok != tc.wantOK || cur != tc.wantCur || inv != tc.wantInv {
				t.Fatalf("fxAnchor = (%q,%v,%v), want (%q,%v,%v)",
					cur, inv, ok, tc.wantCur, tc.wantInv, tc.wantOK)
			}
		})
	}
}

func TestFxRateString(t *testing.T) {
	cases := []struct {
		name    string
		dec     string
		invert  bool
		want    string
		wantErr bool
	}{
		{"direct_pads_to_precision", "0.92", false, "0.9200000000", false},
		{"invert_1_08", "1.08", true, "0.9259259259", false}, // 1/1.08 at 10dp
		{"invert_0_86", "0.86", true, "1.1627906977", false}, // 1/0.86 at 10dp
		{"zero_rejected", "0", false, "", true},
		{"negative_rejected", "-1.2", false, "", true},
		{"garbage_rejected", "abc", false, "", true},
		{"over_ten_integer_digits_rejected", "12345678901", false, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := fxRateString(tc.dec, tc.invert)
			if (err != nil) != tc.wantErr {
				t.Fatalf("fxRateString(%q,%v) err = %v, wantErr %v", tc.dec, tc.invert, err, tc.wantErr)
			}
			if !tc.wantErr && got != tc.want {
				t.Fatalf("fxRateString(%q,%v) = %q, want %q", tc.dec, tc.invert, got, tc.want)
			}
		})
	}
}

func TestFxPairAccepted(t *testing.T) {
	good := extractedFxPair{FromCurrency: "USD", ToCurrency: "EUR", Rate: "0.92", Evidence: "s0", Confidence: "0.8"}
	if !fxPairAccepted(good) {
		t.Fatal("a grounded, confident pair must be accepted")
	}
	for _, bad := range []extractedFxPair{
		{Evidence: "", Confidence: "0.9"},         // ungrounded
		{Evidence: "s0", Confidence: "0.4"},       // below floor
		{Evidence: "s0", Confidence: "1.5"},       // out of range
		{Evidence: "s0", Confidence: "not-a-num"}, // unparseable
	} {
		if fxPairAccepted(bad) {
			t.Errorf("pair %+v must be rejected", bad)
		}
	}
}

func TestParseFxExtractionUnfences(t *testing.T) {
	raw := "```json\n{\"pairs\":[{\"from_currency\":\"EUR\",\"to_currency\":\"USD\",\"rate\":\"1.08\",\"evidence\":\"s0\",\"confidence\":\"0.9\"}]}\n```"
	got, err := parseFxExtraction(raw)
	if err != nil {
		t.Fatalf("parseFxExtraction: %v", err)
	}
	if len(got) != 1 || got[0].FromCurrency != "EUR" || got[0].Rate != "1.08" {
		t.Fatalf("parsed %+v, want one EUR->USD 1.08 pair", got)
	}
}
