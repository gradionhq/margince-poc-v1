// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package capture

import "testing"

func TestFreemailMatchesBaselineAndExtras(t *testing.T) {
	l := NewFreemailList([]string{" Corp-Internal.Example ", ""})

	cases := map[string]bool{
		"gmail.com":             true,
		"GMAIL.COM":             true,
		"web.de":                true,
		"corp-internal.example": true, // configured extra, trimmed + folded
		"acme.example":          false,
		"":                      false,
		"gmail.com.example":     false, // suffix tricks don't match
	}
	for domain, want := range cases {
		if got := l.IsFreemail(domain); got != want {
			t.Errorf("IsFreemail(%q) = %v, want %v", domain, got, want)
		}
	}
}
