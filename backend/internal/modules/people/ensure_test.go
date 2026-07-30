// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

import (
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// The fourth mail-domain gate of the capture ladder, and the one that is easy
// to miss because it lives here rather than in capture: both tells it reads are
// statements ABOUT the sender's mail domain, so a record that has none must
// pass it as a no-op. Without the floor the embedded-address tell compares
// against the empty string and flags every display name containing an "@" —
// suppressing a legitimate conversation for a reason that cannot apply to it.
func TestQuarantineSuspectNeedsADomainToJudge(t *testing.T) {
	cases := []struct {
		name        string
		displayName string
		domain      string
		want        bool
	}{
		{"a punycode domain is a homoglyph vector", "Acme Billing", "xn--80ak6aa92e.com", true},
		{"a punycode label below the apex counts too", "Acme Billing", "mail.xn--80ak6aa92e.com", true},
		{"an embedded address on a foreign domain is the spoof tell", "ceo@real-corp.example", "spoof.test", true},
		{"an embedded address on the sending domain is just a signature", "ceo@acme.test", "acme.test", false},
		{"an ordinary name on an ordinary domain", "Carol Example", "acme.test", false},
		{"a channel sender's name that happens to contain an address", "ceo@real-corp.example", "", false},
		{"a channel sender with a plain name", "Anna Schmidt", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := quarantineSuspect(tc.displayName, tc.domain); got != tc.want {
				t.Fatalf("quarantineSuspect(%q, %q) = %t, want %t", tc.displayName, tc.domain, got, tc.want)
			}
		})
	}
}

// The name a channel record stores when the provider gave a poor one. person
// pins full_name NOT NULL and there is no address whose local part could stand
// in, so the ladder ends at the identity key itself — never at an empty string.
func TestChannelCounterpartyNameFallsBackToTheHandleThenTheKey(t *testing.T) {
	ci := connector.ChannelIdentity{Provider: "telegram", ChannelUserID: "880001", Username: "annahandle"}
	if got := channelCounterpartyName("  Anna Schmidt  ", ci); got != "Anna Schmidt" {
		t.Fatalf("name = %q, want the trimmed provider name", got)
	}
	if got := channelCounterpartyName("   ", ci); got != "@annahandle" {
		t.Fatalf("name = %q, want the handle", got)
	}
	handleless := ci
	handleless.Username = ""
	if got := channelCounterpartyName("", handleless); got != "telegram:880001" {
		t.Fatalf("name = %q, want the identity key", got)
	}
}
