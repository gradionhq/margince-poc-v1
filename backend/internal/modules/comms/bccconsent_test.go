// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package comms

import (
	"slices"
	"testing"
)

// The dispatch-time consent recheck asks about every person the delivery
// reaches, and a blind copy reaches one.
//
// This is the check that runs at TRANSMIT rather than at staging, so it is the
// one that sees a withdrawal made in between — a one-click unsubscribe, an
// erasure. Omitting blind copies here would leave them the only recipients
// whose suppression changed nothing about the message they received, and the
// invisibility is exactly why nobody would notice.
func TestTheConsentRecheckAsksAboutBlindCopies(t *testing.T) {
	got := addressees(Delivery{
		Recipients: []string{"buyer@example.test"},
		Cc:         []string{"boss@example.test"},
		Bcc:        []string{"Quiet@Example.test "},
	})

	for _, addr := range []string{"buyer@example.test", "boss@example.test", "quiet@example.test"} {
		if !slices.Contains(got, addr) {
			t.Errorf("%q receives the message and is not asked about: %v", addr, got)
		}
	}
}

// The same address in two lists is one person to a mail server, so it is one
// question to the gate — and the normalized spelling is what travels, because
// the gate cannot resolve a padded one.
func TestAnAddressInTwoListsIsAskedAboutOnce(t *testing.T) {
	got := addressees(Delivery{
		Recipients: []string{"one@example.test"},
		Bcc:        []string{" One@Example.test "},
	})

	if len(got) != 1 || got[0] != "one@example.test" {
		t.Fatalf("addressees = %v, want the one normalized address", got)
	}
}
