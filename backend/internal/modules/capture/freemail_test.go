// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package capture

import "testing"

func TestFreemailGateAnswersWhatTheTierLadderAsks(t *testing.T) {
	// The extra/never pair is positional, and swapping it would silently invert
	// the gate — a domain the operator carved out would start suppressing and
	// the one they added would not. Both are exercised here for that reason.
	l := NewFreemailList([]string{" Corp-Mailbox.Example ", ""}, []string{"gmx.de"})

	cases := map[string]bool{
		"gmail.com":            true,
		"GMAIL.COM":            true,
		"web.de":               true,
		"live.fr":              true,  // the shipped dataset, not the old hand-pinned map
		"mail.gmx.net":         true,  // a subdomain of a provider is the same mailbox
		"corp-mailbox.example": true,  // workspace addition, trimmed and folded
		"gmx.de":               false, // workspace carve-out beats the baseline
		"herpertz.net":         false, // a person's own domain is not a mailbox vendor
		"acme.example":         false,
		"gmail.com.example":    false, // suffix tricks do not match
		"":                     false,
	}
	for domain, want := range cases {
		if got := l.IsFreemail(domain); got != want {
			t.Errorf("IsFreemail(%q) = %v, want %v", domain, got, want)
		}
	}
}
