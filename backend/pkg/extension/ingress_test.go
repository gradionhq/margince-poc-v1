// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package extension_test

// The published ingress grammar: what a unit may declare, and what it may hand
// the core. Everything here is decidable without knowing which unit is calling,
// which is exactly the split — the caller-dependent half (did YOU declare this
// source, may you ingest at all) is the core's, and lives beside the port.

import (
	"strings"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

func aValidRecord() extension.Record {
	return extension.Record{
		System: "dispact",
		Key:    "7:1042",
		Activity: extension.ActivityFields{
			Kind:       "note",
			Subject:    "a mention",
			Body:       "the preview the provider returned",
			OccurredAt: time.Date(2026, 8, 13, 9, 30, 0, 0, time.UTC),
			Direction:  extension.DirectionInbound,
		},
		ThreadKey:    "dispact:7:88",
		Counterparty: extension.Counterparty{Email: "outside@example.com", DisplayName: "A Sender", Domain: "example.com", Direction: extension.DirectionInbound},
		Addresses:    []string{"outside@example.com", "member@installation.test"},
		Raw:          []byte(`{"id":1042}`),
	}
}

// The fixture has to pass, or every refusal below could be passing for the
// wrong reason.
func TestAWellFormedRecordIsAccepted(t *testing.T) {
	if err := aValidRecord().Validate(); err != nil {
		t.Fatalf("a well-formed record was refused: %v", err)
	}
}

// Each of these is a way a record could be accepted-and-dropped, or accepted
// and stored at a size a remote party chose. A bound that is not checked is a
// bound that is not there.
func TestTheRecordGrammarRefusesWhatCannotBeLandedHonestly(t *testing.T) {
	for name, damage := range map[string]func(*extension.Record){
		"no system named":    func(r *extension.Record) { r.System = " " },
		"no key at all":      func(r *extension.Record) { r.Key = "" },
		"a key over the cap": func(r *extension.Record) { r.Key = strings.Repeat("k", extension.MaxKeyLength+1) },
		"no activity kind":   func(r *extension.Record) { r.Activity.Kind = "" },
		"no occurred-at":     func(r *extension.Record) { r.Activity.OccurredAt = time.Time{} },
		"a subject over the cap": func(r *extension.Record) {
			r.Activity.Subject = strings.Repeat("s", extension.MaxSubjectRunes+1)
		},
		"a body over the cap": func(r *extension.Record) { r.Activity.Body = strings.Repeat("b", extension.MaxBodyRunes+1) },
		"a direction that is not one": func(r *extension.Record) {
			r.Activity.Direction = "sideways"
		},
		"a counterparty direction that is not one": func(r *extension.Record) {
			r.Counterparty.Direction = "sideways"
		},
		"a counterparty address over the cap": func(r *extension.Record) {
			r.Counterparty.Email = strings.Repeat("a", extension.MaxAddressLength+1)
		},
		"a display name over the cap": func(r *extension.Record) {
			r.Counterparty.DisplayName = strings.Repeat("n", extension.MaxDisplayNameRunes+1)
		},
		"more addresses than the cap": func(r *extension.Record) {
			r.Addresses = make([]string, extension.MaxAddresses+1)
		},
		"an address over the cap": func(r *extension.Record) {
			r.Addresses = []string{strings.Repeat("a", extension.MaxAddressLength+1)}
		},
		// The two that DISABLE the internal-message gate rather than failing
		// it: over an empty set the gate answers "not internal" and keeps the
		// record, and a blank element is a party it skips.
		"no addresses on a record that names one": func(r *extension.Record) { r.Addresses = nil },
		"a blank address among real ones": func(r *extension.Record) {
			r.Addresses = []string{"sender@acme.test", "  "}
		},
		"a thread key over the cap": func(r *extension.Record) {
			r.ThreadKey = strings.Repeat("t", extension.MaxThreadKeyLength+1)
		},
		"a raw record over the cap": func(r *extension.Record) {
			r.Raw = make([]byte, extension.MaxRawBytes+1)
		},
		// A HALF-stated channel identity, either way round. It is the shape that
		// looks populated and routes nowhere: the core keys the binding on the
		// pair, so half of it binds nothing — and the record would still land,
		// read as ordinary, and carry no reply address.
		//
		// Each of these clears the ADDRESS, or the record would be refused for
		// naming its human twice and every one of them would pass for a reason
		// it is not testing.
		"a channel account with no provider": namedByAccount(extension.ChannelIdentity{ChannelUserID: "G-1c7u1r29"}),
		"a channel provider with no account": namedByAccount(extension.ChannelIdentity{Provider: "dispact"}),
		// The provider is a channel_provider row and has to satisfy that column's
		// own grammar, which is snake and not kebab — the difference DESIGN-SP5 §9
		// got wrong once already.
		"a channel provider the registry grammar refuses": namedByAccount(
			extension.ChannelIdentity{Provider: "deal-room", ChannelUserID: "G-1"}),
		"a channel account id over the cap": namedByAccount(extension.ChannelIdentity{
			Provider: "dispact", ChannelUserID: strings.Repeat("g", extension.MaxChannelUserIDLength+1),
		}),
		"a channel display name over the cap": namedByAccount(extension.ChannelIdentity{
			Provider: "dispact", ChannelUserID: "G-1", DisplayName: strings.Repeat("n", extension.MaxDisplayNameRunes+1),
		}),
		// Named TWICE — the address KEPT this time. The core refuses it
		// (ErrCounterpartyNamedTwice) because an address and a channel account
		// resolve through different ladders, and the published grammar refuses it
		// here so a unit reads the reason rather than an unattributable "the core
		// could not land this record".
		"a counterparty named by an address and by an account": func(r *extension.Record) {
			r.Counterparty.ChannelIdentity = extension.ChannelIdentity{Provider: "dispact", ChannelUserID: "G-1"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			rec := aValidRecord()
			damage(&rec)
			if err := rec.Validate(); err == nil {
				t.Fatalf("the grammar accepted %s", name)
			}
		})
	}
}

// namedByAccount rewrites the fixture into the channel shape: the address
// dropped, the account named. It is the one legal way to state a channel
// counterparty, so every damage to that account has to start from it.
func namedByAccount(identity extension.ChannelIdentity) func(*extension.Record) {
	return func(r *extension.Record) {
		r.Counterparty.Email = ""
		r.Counterparty.Domain = ""
		r.Counterparty.ChannelIdentity = identity
	}
}

// A record that names no address ANYWHERE may name no addresses, and this is
// the case a channel-only provider is made of: opaque account ids, a display
// name, and no mail in the message at all.
//
// Refusing it would have turned such a record away at the published grammar,
// before it reached a core that accepts it — connector.NormalizedRecord reads an
// empty set as "I cannot enumerate the parties", which the internal-message gate
// answers as not-internal and keeps.
func TestARecordThatNamesNoAddressMayNameNoAddresses(t *testing.T) {
	rec := aValidRecord()
	rec.Activity.Kind = extension.ActivityKindMessage
	rec.Activity.ChannelProvider = "dispact"
	namedByAccount(extension.ChannelIdentity{Provider: "dispact", ChannelUserID: "G-1"})(&rec)
	rec.Addresses = nil

	if err := rec.Validate(); err != nil {
		t.Fatalf("a channel-only record with no addresses was refused: %v", err)
	}

	// And the mirror, which is what keeps the exemption from swallowing the
	// rule: a record that DOES name an address still owes the whole party set,
	// or the gate it belongs to is silently disabled.
	mail := aValidRecord()
	mail.Addresses = nil
	if err := mail.Validate(); err == nil {
		t.Error("a mail-shaped record with no addresses was accepted; the internal-colleague gate reads that set and keeps everything over an empty one")
	}
}

// A record identifying its counterparty by ADDRESS binds no channel identity,
// and that empty pair must stay legal — every mail record in the product is
// this shape, and a rule that refused it would close the ingress surface it was
// meant to bound.
func TestARecordMayIdentifyItsCounterpartyByAddressAlone(t *testing.T) {
	rec := aValidRecord()
	rec.Counterparty.ChannelIdentity = extension.ChannelIdentity{}
	if err := rec.Validate(); err != nil {
		t.Fatalf("a record with no channel identity was refused: %v", err)
	}
}

// A channel record states BOTH halves of the identity, and the pair is what
// makes the message repliable at all.
func TestAChannelRecordMayNameTheAccountItCanBeAnsweredAt(t *testing.T) {
	rec := aValidRecord()
	rec.Activity.Kind = extension.ActivityKindMessage
	rec.Activity.ChannelProvider = "dispact"
	namedByAccount(extension.ChannelIdentity{
		Provider: "dispact", ChannelUserID: "G-1c7u1r29", DisplayName: "A Sender",
	})(&rec)
	if err := rec.Validate(); err != nil {
		t.Fatalf("a well-formed channel record was refused: %v", err)
	}
}

// The runes-versus-bytes distinction is not decoration: a subject of 500
// two-byte characters is a subject a human wrote, and a cap counting bytes
// would refuse it for being in the wrong alphabet.
func TestTheTextCapsCountRunesNotBytes(t *testing.T) {
	rec := aValidRecord()
	rec.Activity.Subject = strings.Repeat("é", extension.MaxSubjectRunes)
	rec.Counterparty.DisplayName = strings.Repeat("é", extension.MaxDisplayNameRunes)
	if err := rec.Validate(); err != nil {
		t.Fatalf("text at exactly the cap was refused: %v", err)
	}
}

// An empty direction is a record with no honest direction, which is different
// from a wrong one — a unit that cannot tell must be able to say so rather than
// pick.
func TestAnEmptyDirectionIsLegal(t *testing.T) {
	rec := aValidRecord()
	rec.Activity.Direction, rec.Counterparty.Direction = "", ""
	if err := rec.Validate(); err != nil {
		t.Fatalf("a record with no stated direction was refused: %v", err)
	}
}

// A declaration an operator reads has to say what it does, and it becomes half
// of every landed record's provenance — so the grammar is the same shape a unit
// name takes, and it is bounded.
func TestTheDeclarationGrammar(t *testing.T) {
	valid := extension.IngressSource{System: "dispact-chat", Lands: []extension.RecordKind{extension.KindActivity}}
	if err := valid.Validate(); err != nil {
		t.Fatalf("a well-formed source was refused: %v", err)
	}
	for name, source := range map[string]extension.IngressSource{
		"an empty system":      {System: "  ", Lands: []extension.RecordKind{extension.KindActivity}},
		"an upper-case system": {System: "Dispact", Lands: []extension.RecordKind{extension.KindActivity}},
		"a system with a space": {
			System: "dispact chat", Lands: []extension.RecordKind{extension.KindActivity},
		},
		"a system with a double hyphen": {
			System: "dispact--chat", Lands: []extension.RecordKind{extension.KindActivity},
		},
		"a system over the cap": {
			System: strings.Repeat("s", 33), Lands: []extension.RecordKind{extension.KindActivity},
		},
		"no kinds at all": {System: "dispact"},
		"a kind the core cannot land": {
			System: "dispact", Lands: []extension.RecordKind{"lead"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := source.Validate(); err == nil {
				t.Fatalf("the declaration grammar accepted %s", name)
			}
		})
	}
}

// Both dispositions are successes and both advance a cursor. The distinction
// this type does NOT draw is the point of it: a replay answers Accepted like
// any other landing, because the pipeline reports no difference and a
// "replayed" value would be a promise this side cannot keep.
func TestTheDispositionsAreTheTwoTheCoreCanHonestlyReport(t *testing.T) {
	if extension.DispositionAccepted == extension.DispositionSkipped {
		t.Fatal("the two dispositions are one value")
	}
	for _, d := range []extension.Disposition{extension.DispositionAccepted, extension.DispositionSkipped} {
		if strings.TrimSpace(string(d)) == "" {
			t.Errorf("a disposition renders as empty, which a unit cannot log or branch on")
		}
	}
}
