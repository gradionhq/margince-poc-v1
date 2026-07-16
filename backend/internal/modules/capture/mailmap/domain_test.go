// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package mailmap

import "testing"

func TestDomainOfSplitsAtLastAtSign(t *testing.T) {
	cases := map[string]string{
		`"weird@local"@Example.COM`: "example.com", // quoted local part with an @ — split at the LAST @
		"alice@acme.com":            "acme.com",
		"  Bob@Work.Example  ":      "work.example", // trimmed + lowercased
		"no-at-sign":                "",
		"":                          "",
	}
	for in, want := range cases {
		if got := domainOf(in); got != want {
			t.Errorf("domainOf(%q) = %q, want %q", in, got, want)
		}
	}
}
