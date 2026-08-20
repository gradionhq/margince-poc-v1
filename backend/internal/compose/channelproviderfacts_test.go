// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// What the transport directory publishes about a provider, built from what this
// binary composed.

import (
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
	"github.com/gradionhq/margince/backend/pkg/extension"
)

// A name a human reads, from an id a machine keys on.
//
// Both separators are exercised because the two ids that reach this function use
// one each: a provider id is snake and an ingress system key is kebab, and a
// label that title-cased only one of them would print `Zalo-oa` beside `Zalo Oa`
// for what is one transport seen from two sides.
//
// The doubled separator is the case with a second author. Migration 0247 seeds a
// fresh installation's labels with `initcap(replace(provider,'_',' '))`, which
// renders an empty segment as a space — so collapsing the run here would make a
// fresh install and a booted one disagree about that provider's name, and the
// doubled underscore is legal under the column's own CHECK.
func TestAnIdBecomesWordsWhicheverSeparatorItUses(t *testing.T) {
	for id, want := range map[string]string{
		"dispact":      "Dispact",
		"zalo_oa":      "Zalo Oa",
		"zalo-oa":      "Zalo Oa",
		"dispact-mail": "Dispact Mail",
		"deal_room":    "Deal Room",
		"deal__room":   "Deal  Room",
		"a":            "A",
	} {
		if got := titleCasedID(id); got != want {
			t.Errorf("titleCasedID(%q) = %q, want %q", id, got, want)
		}
	}
}

// One transport seen from two sides reads as ONE name. A unit that captures from
// a system the core also carries publishes the core's own compiled-in spelling,
// not a title-cased guess at it: "Whatsapp" beside `data`'s "WhatsApp" in one
// JSON document is the defect this seam exists to remove.
//
// It is not refused, which is the difference from a unit's CHANNEL declaration:
// ingesting Telegram exports and naming the source `telegram` is true, and the
// shared label is then correct rather than a collision.
func TestACaptureSourceTakesTheCoreSpellingOfAName(t *testing.T) {
	facts := captureSourceFactsFor([]extension.Extension{{
		Name: "probe-unit",
		Ingress: []extension.IngressSource{
			{System: "whatsapp", Lands: []extension.RecordKind{extension.KindActivity}},
			{System: "probe-system", Lands: []extension.RecordKind{extension.KindActivity}},
		},
	}})

	labels := map[string]string{}
	for _, f := range facts {
		labels[f.source] = f.label
	}
	if got := labels["ext:probe-unit:whatsapp"]; got != providerLabel("whatsapp") {
		t.Errorf("a unit capturing from a core transport published %q, and `data` publishes %q for the same transport", got, providerLabel("whatsapp"))
	}
	// A system key the core has no spelling for still gets a name — the map is a
	// preference, not a gate, or a unit's own system would publish a raw id.
	if got := labels["ext:probe-unit:probe-system"]; got != "Probe System" {
		t.Errorf("a unit-only system key published %q, want %q", got, "Probe System")
	}
}

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
