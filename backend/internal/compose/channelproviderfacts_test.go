// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// What the transport directory publishes about a provider, built from what this
// binary composed.

import (
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// The directory must publish what a transport can carry, because the parking
// gate assumes the composer already warned. A provider registered with no sender
// composed carries nothing, and SAYS so — rather than omitting the fact and
// letting a client guess, which is the same silence the gate's own doc claims
// this endpoint has already broken.
func TestChannelProviderFactsPublishCarriage(t *testing.T) {
	carrying := connector.Carriage{Carries: true, MaxBytesPerFile: 20 << 20, MaxFiles: 10, MaxBodyWithFiles: 1024}
	facts := channelProviderFactsFor(
		[]string{"telegram", "whatsapp"},
		map[string]connector.Carriage{"telegram": carrying},
	)

	byName := map[string]channelProviderFacts{}
	for _, f := range facts {
		byName[f.provider] = f
	}
	if got := byName["telegram"].carriage; got != carrying {
		t.Errorf("telegram publishes %+v, want %+v", got, carrying)
	}
	if !byName["telegram"].suppliesTransport {
		t.Error("telegram composed a sender and must supply a transport")
	}
	if got := byName["whatsapp"].carriage; got.Carries {
		t.Errorf("whatsapp is registered with no sender composed, so it must carry nothing; got %+v", got)
	}
	if byName["whatsapp"].suppliesTransport {
		t.Error("whatsapp has no composed sender and must not report a transport")
	}
}

// A transport that sends but declared no carriage is present in the map with the
// ZERO descriptor, and that must read as "carries nothing" — not as "sends, so
// presumably carries". A unit transport is exactly this shape until a unit can
// declare its own carriage.
func TestASendingProviderThatDeclaredNoCarriageCarriesNothing(t *testing.T) {
	facts := channelProviderFactsFor([]string{"zalo_personal"},
		map[string]connector.Carriage{"zalo_personal": {}})
	if len(facts) != 1 {
		t.Fatalf("built %d facts for one provider", len(facts))
	}
	if !facts[0].suppliesTransport {
		t.Error("a provider in the sending map supplies a transport")
	}
	if facts[0].carriage.Carries {
		t.Error("a provider that declared no carriage was published as able to carry files")
	}
}

// The wire entry carries every bound, not just the bool. An entry that published
// carries=true with zero limits would tell a composer it may attach anything.
func TestThePublishedEntryCarriesEveryBound(t *testing.T) {
	published := publishedChannelProviders([]string{"telegram"},
		map[string]connector.Carriage{"telegram": {
			Carries: true, MaxBytesPerFile: 20 << 20, MaxFiles: 10, MaxBodyWithFiles: 1024,
		}})
	if len(published) != 1 {
		t.Fatalf("published %d entries for one provider", len(published))
	}
	got := published[0].Attachments
	if !got.Carries || got.MaxFiles != 10 || got.MaxBytesPerFile != 20<<20 || got.MaxBodyWithFiles != 1024 {
		t.Errorf("the entry publishes %+v; a composer reading it cannot warn before a rep presses send", got)
	}
}

// A transport a reply CAN leave on but whose connector declared no carriage
// carries nothing — and that is the answer every unit transport gets today.
//
// Asserted rather than explained: the reason a unit reports the zero descriptor
// is written where the map is built, and a comment is not what keeps a later
// change from defaulting an unknown provider to "carries, limits unknown", which
// would send a rep's files at a transport that drops them.
func TestSendableCarriageGivesAnUndeclaredTransportNothing(t *testing.T) {
	got := sendableCarriage(
		[]string{"telegram", "zalo_personal"},
		map[string]connector.Carriage{"telegram": {Carries: true, MaxFiles: 10}},
	)
	if len(got) != 2 {
		t.Fatalf("paired %d of 2 sendable transports; one missing from the map reads as unable to send at all", len(got))
	}
	if !got["telegram"].Carries || got["telegram"].MaxFiles != 10 {
		t.Errorf("telegram's declared carriage was not carried through: %+v", got["telegram"])
	}
	carriage, present := got["zalo_personal"]
	if !present {
		t.Fatal("a unit transport dropped out of the sendable map, so the directory would report it cannot send")
	}
	if carriage.Carries {
		t.Errorf("a transport whose connector declared no carriage was published as able to carry files: %+v", carriage)
	}
}
