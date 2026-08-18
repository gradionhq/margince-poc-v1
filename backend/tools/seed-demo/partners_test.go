// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

import "testing"

// TestPartnerEmailFoldsDiacritics pins the address built for a partner's own
// staff. Two of the three partners are Vietnamese and Korean, so a name with
// diacritics is the normal case rather than the edge one — and a local part
// that dropped the accented letters entirely ("nguyn") would be a plausible
// wrong answer nobody would notice in a demo.
func TestPartnerEmailFoldsDiacritics(t *testing.T) {
	for _, tc := range []struct {
		name   string
		person string
		domain string
		want   string
	}{
		{"plain ascii", "Katrin Berger", "dachpartner.example", "katrin.berger@dachpartner.example"},
		{"vietnamese", "Nguyễn Thanh Tùng", "vietnampartner.example", "nguyen.tung@vietnampartner.example"},
		{"vietnamese with a given name in the middle", "Lê Thị Hồng Nhung", "vietnampartner.example", "le.nhung@vietnampartner.example"},
		{"hyphenated korean", "Ji-woo Han", "koreapartner.example", "jiwoo.han@koreapartner.example"},
		{"german umlaut", "Jürgen Groß", "dachpartner.example", "jurgen.gro@dachpartner.example"},
		{"mononym", "Prince", "dachpartner.example", "prince@dachpartner.example"},
		{"nothing addressable", "李", "dachpartner.example", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := partnerEmail(tc.person, tc.domain); got != tc.want {
				t.Errorf("partnerEmail(%q, %q) = %q, want %q", tc.person, tc.domain, got, tc.want)
			}
		})
	}
}

// TestPartnerEmailStaysOnAnUndeliverableDomain is the rule the dataset's whole
// defang pass exists to hold: nothing the seeder writes may be mailable. The
// partners sit on .example, which RFC 2606 reserves, so this checks the
// address inherits that rather than acquiring a real domain along the way.
func TestPartnerEmailStaysOnAnUndeliverableDomain(t *testing.T) {
	got := partnerEmail("Seung-min Park", "koreapartner.example")
	if want := "seungmin.park@koreapartner.example"; got != want {
		t.Fatalf("partnerEmail = %q, want %q", got, want)
	}
}
