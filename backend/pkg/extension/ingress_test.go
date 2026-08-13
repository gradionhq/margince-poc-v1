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
		"a thread key over the cap": func(r *extension.Record) {
			r.ThreadKey = strings.Repeat("t", extension.MaxThreadKeyLength+1)
		},
		"a raw record over the cap": func(r *extension.Record) {
			r.Raw = make([]byte, extension.MaxRawBytes+1)
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
