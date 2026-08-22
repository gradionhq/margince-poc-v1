// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package dealrooms

// Delivery state answers "did the invitation arrive?", which is a different
// question from "may this person in?" — and the whole reason the two are
// modelled apart is that a seller chasing silence has to tell a bounce from a
// deliberate removal.
//
// These cases are the ones where a wrong answer sends somebody down the wrong
// path: reporting `sent` for a bounce, or for a person who was revoked.

import (
	"errors"
	"testing"
	"time"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
)

func TestDeliveryStateReadsTheMostDecidedOutcome(t *testing.T) {
	at := func(day int) *time.Time {
		v := time.Date(2026, time.August, day, 12, 0, 0, 0, time.UTC)
		return &v
	}

	for _, tc := range []struct {
		name    string
		facts   deliveryFacts
		revoked bool
		want    crmcontracts.DealRoomDeliveryState
	}{
		{
			name:  "no credential at all",
			facts: deliveryFacts{},
			want:  crmcontracts.DealRoomDeliveryStateNone,
		},
		{
			// The installation has no outbound mail, or the seller is passing
			// the link on by hand. A credential exists; nothing carried it.
			name:  "minted but handed to no relay",
			facts: deliveryFacts{expiresAt: at(20)},
			want:  crmcontracts.DealRoomDeliveryStatePending,
		},
		{
			name:  "left the building",
			facts: deliveryFacts{expiresAt: at(20), sentAt: at(1)},
			want:  crmcontracts.DealRoomDeliveryStateSent,
		},
		{
			name:  "accepted by their server",
			facts: deliveryFacts{expiresAt: at(20), sentAt: at(1), deliveredAt: at(2)},
			want:  crmcontracts.DealRoomDeliveryStateDelivered,
		},
		{
			// The case that most needs to be right: a bounce outranks the send
			// that preceded it. "We sent it" is not what a seller needs to hear
			// when it came straight back.
			name:  "bounced after being sent",
			facts: deliveryFacts{expiresAt: at(20), sentAt: at(1), failedAt: at(2)},
			want:  crmcontracts.DealRoomDeliveryStateFailed,
		},
		{
			// A relay can accept a message and bounce it afterwards, so both
			// stamps land on one attempt. The later fact wins — and this is the
			// case that pins the ORDER rather than merely the outcome: with
			// `delivered` checked first it reads as arrived, and the seller
			// never learns their contact is unreachable.
			name:  "accepted, then bounced",
			facts: deliveryFacts{expiresAt: at(20), sentAt: at(1), deliveredAt: at(2), failedAt: at(3)},
			want:  crmcontracts.DealRoomDeliveryStateFailed,
		},
		{
			// Consumed ends the story whatever happened on the way — including
			// a delivery failure recorded for an earlier attempt.
			name:  "signed in despite an earlier failure",
			facts: deliveryFacts{expiresAt: at(20), sentAt: at(1), failedAt: at(2), consumedAt: at(3)},
			want:  crmcontracts.DealRoomDeliveryStateConsumed,
		},
		{
			// Revocation answers first. Reporting `sent` for somebody who was
			// removed would read as though their invitation were still on its
			// way to them.
			name:    "revoked while an invitation was in flight",
			facts:   deliveryFacts{expiresAt: at(20), sentAt: at(1), deliveredAt: at(2)},
			revoked: true,
			want:    crmcontracts.DealRoomDeliveryStateNone,
		},
		{
			// Even a consumed credential reads as none once access is gone: the
			// person is not in the room, whatever they once did.
			name:    "revoked after signing in",
			facts:   deliveryFacts{expiresAt: at(20), consumedAt: at(3)},
			revoked: true,
			want:    crmcontracts.DealRoomDeliveryStateNone,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.facts.state(tc.revoked); got != tc.want {
				t.Errorf("state = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestOnlyTheThreeNamedCapabilitiesAreAccepted(t *testing.T) {
	// The schema CHECK would refuse an unknown one too, but as a constraint
	// violation: a 500 carrying a table name, telling the caller nothing about
	// which values are legal.
	for _, capability := range []string{capabilityView, capabilityComment, capabilityReviewer} {
		if err := refuseUnknownCapability(capability); err != nil {
			t.Errorf("%q must be accepted: %v", capability, err)
		}
	}
	for _, capability := range []string{"", "admin", "owner", "View", "read"} {
		err := refuseUnknownCapability(capability)
		if err == nil {
			t.Errorf("%q must be refused", capability)
			continue
		}
		var fault interface {
			FieldFault() (string, string, string)
		}
		if !errors.As(err, &fault) {
			t.Errorf("%q must be refused as a field fault so the caller learns which field is wrong; got %T", capability, err)
			continue
		}
		field, _, message := fault.FieldFault()
		if field != "capability" {
			t.Errorf("the refusal must name the capability field, got %q", field)
		}
		if message == "" {
			t.Error("the refusal must say which values are legal")
		}
	}
}
