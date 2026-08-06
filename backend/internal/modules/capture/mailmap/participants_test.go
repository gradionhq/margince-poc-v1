// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package mailmap

import (
	"fmt"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// roleOf finds the role a participant list gives an address, so a test can
// state the claim it cares about rather than an index into a slice.
func roleOf(list []connector.MessageParticipant, address string) (string, bool) {
	for _, p := range list {
		if p.Email == address {
			return p.Role, true
		}
	}
	return "", false
}

// The two ends of the exchange already have their own participant rows,
// stamped from the connection rather than from a header. A second row for
// either would record the same human twice under two roles.
func TestParseExcludesTheOwnerAndTheCounterpartyFromFurtherParties(t *testing.T) {
	raw := crlf(
		"From: bob@target.com",
		"To: me@myco.com, colleague@myco.com",
		"Cc: Sam Second <sam@target.com>, bob@target.com",
		"Subject: Pricing",
		"Date: Wed, 04 Jun 2026 09:00:00 +0000",
		"Message-ID: <m1@target.com>",
		"Content-Type: text/plain; charset=utf-8",
		"",
		"Body.",
		"",
	)
	msg, err := Parse(raw, "me@myco.com")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if _, found := roleOf(msg.participants, "me@myco.com"); found {
		t.Error("the mailbox owner was recorded as a further participant; their own row already names them")
	}
	if _, found := roleOf(msg.participants, "bob@target.com"); found {
		t.Error("the counterparty was recorded as a further participant; their own row already names them")
	}
	role, found := roleOf(msg.participants, "colleague@myco.com")
	if !found {
		t.Fatal("a second recipient was dropped — recovering them is the point of the participant rows")
	}
	if role != connector.ParticipantRoleTo {
		t.Errorf("a To recipient got role %q, want %q", role, connector.ParticipantRoleTo)
	}
	if role, _ := roleOf(msg.participants, "sam@target.com"); role != connector.ParticipantRoleCC {
		t.Errorf("a Cc recipient got role %q, want %q", role, connector.ParticipantRoleCC)
	}
}

// An address on both To and Cc was addressed directly, and that is the
// stronger claim about their part in the conversation.
func TestParseGivesADirectRecipientTheToRoleEvenWhenAlsoCopied(t *testing.T) {
	raw := crlf(
		"From: bob@target.com",
		"To: me@myco.com, dual@target.com",
		"Cc: dual@target.com",
		"Subject: Pricing",
		"Date: Wed, 04 Jun 2026 09:00:00 +0000",
		"Message-ID: <m2@target.com>",
		"Content-Type: text/plain; charset=utf-8",
		"",
		"Body.",
		"",
	)
	msg, err := Parse(raw, "me@myco.com")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	var seen int
	for _, p := range msg.participants {
		if p.Email == "dual@target.com" {
			seen++
			if p.Role != connector.ParticipantRoleTo {
				t.Errorf("role = %q, want %q — To outranks Cc for the same address", p.Role, connector.ParticipantRoleTo)
			}
		}
	}
	if seen != 1 {
		t.Errorf("the same address produced %d participant rows, want 1", seen)
	}
}

// Past the cap the addresses are a distribution list, and every name on one is
// evidence of a list membership rather than of a conversation. Folding those
// into the interaction graph would report a relationship with everybody who
// received the same newsletter.
func TestParseDropsTheFurtherPartiesOfABroadcast(t *testing.T) {
	var recipients []string
	for i := range connector.MaxParticipants + 1 {
		recipients = append(recipients, fmt.Sprintf("person%d@list.example", i))
	}
	raw := crlf(
		"From: bob@target.com",
		"To: me@myco.com",
		"Cc: "+strings.Join(recipients, ", "),
		"Subject: Newsletter",
		"Date: Wed, 04 Jun 2026 09:00:00 +0000",
		"Message-ID: <m3@target.com>",
		"Content-Type: text/plain; charset=utf-8",
		"",
		"Body.",
		"",
	)
	msg, err := Parse(raw, "me@myco.com")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(msg.participants) != 0 {
		t.Errorf("a %d-address broadcast contributed %d participants, want none",
			len(recipients), len(msg.participants))
	}
}

// The replay pass reads stored originals through this seam, so what it
// recovers has to match what live capture would have stamped.
func TestParticipantsOfReadsTheSamePartiesAsCapture(t *testing.T) {
	raw := crlf(
		"From: bob@target.com",
		"To: me@myco.com",
		"Cc: sam@target.com",
		"Subject: Pricing",
		"Date: Wed, 04 Jun 2026 09:00:00 +0000",
		"Message-ID: <m4@target.com>",
		"Content-Type: text/plain; charset=utf-8",
		"",
		"Body.",
		"",
	)
	replayed, err := ParticipantsOf(raw, "me@myco.com")
	if err != nil {
		t.Fatalf("ParticipantsOf: %v", err)
	}
	msg, err := Parse(raw, "me@myco.com")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	captured := msg.ToRecord("gmail", raw).Participants
	if len(replayed) != len(captured) {
		t.Fatalf("replay found %d participants, live capture stamps %d", len(replayed), len(captured))
	}
	for i := range replayed {
		if replayed[i] != captured[i] {
			t.Errorf("participant %d: replay %+v, capture %+v", i, replayed[i], captured[i])
		}
	}
}

// Bcc survives only on the sender's own copy, and where it survives it is a
// real party: a colleague who blind-copied a customer wrote to a customer, and
// the internal-vs-external decision has to see that (ADR-0082/A127 §3).
func TestParseRecordsABlindCopiedPartyUnderItsOwnRole(t *testing.T) {
	raw := crlf(
		"From: me@myco.com",
		"To: bob@target.com",
		"Bcc: quiet@partner.example",
		"Subject: Pricing",
		"Date: Wed, 04 Jun 2026 09:00:00 +0000",
		"Message-ID: <bcc1@myco.com>",
		"Content-Type: text/plain; charset=utf-8",
		"",
		"Body.",
		"",
	)
	msg, err := Parse(raw, "me@myco.com")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	role, found := roleOf(msg.participants, "quiet@partner.example")
	if !found {
		t.Fatal("a blind-copied party must be recorded — dropping them hides a party from the internal decision")
	}
	if role != connector.ParticipantRoleBCC {
		t.Errorf("role = %q, want %q: recording them as an open recipient misstates who was addressed", role, connector.ParticipantRoleBCC)
	}
}

// The internal decision is about the WHOLE message, so the address set includes
// both ends of the exchange — unlike the participant rows, which deliberately
// exclude them.
func TestAddressesNamesEveryPartyIncludingBothEnds(t *testing.T) {
	raw := crlf(
		"From: bob@target.com",
		"To: me@myco.com, colleague@myco.com",
		"Cc: sam@target.com",
		"Bcc: quiet@partner.example",
		"Subject: Pricing",
		"Date: Wed, 04 Jun 2026 09:00:00 +0000",
		"Message-ID: <addr1@target.com>",
		"Content-Type: text/plain; charset=utf-8",
		"",
		"Body.",
		"",
	)
	msg, err := Parse(raw, "me@myco.com")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want := []string{
		"bob@target.com", "me@myco.com", "colleague@myco.com",
		"sam@target.com", "quiet@partner.example",
	}
	got := msg.Addresses()
	if len(got) != len(want) {
		t.Fatalf("Addresses = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Addresses = %v, want %v", got, want)
		}
	}
}

// A message the parser could read but whose parties it could not is reported
// with the addresses it did find. Reporting none would read to the writer as
// "nobody", and the writer keeps a message it cannot judge.
func TestAddressesOfAMessageWithNoReadableRecipientsIsNotSilentlyEmpty(t *testing.T) {
	raw := crlf(
		"From: bob@target.com",
		"To: undisclosed-recipients:;",
		"Subject: Announcement",
		"Date: Wed, 04 Jun 2026 09:00:00 +0000",
		"Message-ID: <undisclosed@target.com>",
		"Content-Type: text/plain; charset=utf-8",
		"",
		"Body.",
		"",
	)
	msg, err := Parse(raw, "me@myco.com")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := msg.Addresses(); len(got) != 1 || got[0] != "bob@target.com" {
		t.Errorf("Addresses = %v, want just the readable sender", got)
	}
}
