// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The published ingress record and the core's own capture envelope are two
// hand-written type sets describing one thing, and nothing compiles them
// together: pkg/extension is stdlib-only, so it cannot alias or embed the core's
// shapes, and the adapter that bridges them names each field by hand.
//
// So a field ADDED to the core envelope is silent here. It compiles, it lands
// through every other connector, and a unit simply cannot supply it — which is
// how the first version of this seam shipped with no Addresses at all, meaning
// the internal-colleague gate it described could never fire (an empty address
// set reads as "this connector could not enumerate the parties"). Nothing
// failed; the drop just never happened.
//
// This walk is the answer: every field of the core's envelope is either mirrored
// on the published surface, or waived HERE with the reason it is absent. A new
// field arrives as a failing test naming itself.
//
// It lives in COMPOSE rather than beside the published types, and rather than
// in the root fitness package. pkg/extension may import nothing under internal,
// in test files and external test files alike, so it cannot see the envelope;
// and the root package is not permitted to depend on a module (go-arch-lint),
// so it cannot see capture's activity shape. Compose depends on both already —
// it is where the conversion under test lives.

import (
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/shared/gatekit"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
	"github.com/gradionhq/margince/backend/pkg/extension"
)

// waivedEnvelopeFields are the core envelope's fields a unit deliberately does
// not supply, each with the reason it is absent. A waiver is a DECISION, so it
// is spelled out rather than a name in a list.
var waivedEnvelopeFields = gatekit.Waive(map[string]string{
	"EntityType": "the port stamps it: one kind is landable, and a Record has one payload",
	"Source":     "core-stamped from the invoking unit and its DECLARED source, so a unit cannot attribute its records to another unit or to a core connector",
	"CapturedBy": "core-stamped, and equal to the acting principal's id — which is what makes capture's own 'a connector cannot claim to be another one' check pass by construction",
	"NaturalKey": "half core-derived: the published Record carries the provider's own Key and the port pairs it with the derived source system",
	"Links": "deliberately absent — a record naming the core rows it attaches to would make the link-visibility probe a per-row existence oracle over the scope the ingest runs under. " +
		"What a message is about is decided by the core's counterparty resolution",
	"Participants": "the further parties beyond the two ends. Addresses already carries every party the internal-only gate needs, and a unit reporting attendee structure has no consumer today",
	"Parts":        "attachments. A unit's provider bytes never reach the file keeper: the bounded, renamed, content-typed parser is mail capture's, and there is no published path to it",
	"PartDrops":    "the breadcrumb for refused attachments, which cannot exist while Parts does not",
	"Fields":       "the envelope's `any`. The published surface is typed instead — Record.Activity — so a unit cannot hand the sink a shape it does not switch on",
})

// TestThePublishedRecordMirrorsTheCaptureEnvelope walks the core envelope and
// requires each field to be published or waived.
func TestThePublishedRecordMirrorsTheCaptureEnvelope(t *testing.T) {
	published := fieldsOf(reflect.TypeOf(extension.Record{}))
	envelope := reflect.TypeOf(connector.NormalizedRecord{})
	if envelope.NumField() == 0 {
		t.Fatal("the capture envelope reflected as empty — this walk would then pass over anything")
	}
	for i := range envelope.NumField() {
		name := envelope.Field(i).Name
		if published[name] || waivedEnvelopeFields.Waived(t, name) {
			continue
		}
		t.Errorf("connector.NormalizedRecord.%s has no counterpart on extension.Record and no waiver — a unit cannot supply it, "+
			"and a landed record therefore carries whatever the zero value means. Publish it, or waive it in this file with the reason", name)
	}
	// A waiver naming a field that is gone certifies nothing while reading as
	// though it does — and it is also a hole the next field can arrive through,
	// under a name a reader would take as already decided.
	waivedEnvelopeFields.AssertAllMatched(t)
}

// waivedActivityFields are the core activity shape's fields a unit deliberately
// cannot state. The bar is higher here than on the envelope: the envelope's
// waivers are mostly "core stamps it", whereas an unpublished field on THIS
// shape means a unit's message carries the zero value for something the timeline
// stores — so the reason has to say why that zero value is the right answer, not
// merely why the unit is not asked.
var waivedActivityFields = gatekit.Waive(map[string]string{
	"ChannelProvider": "the messaging transport a record arrived on, and a reference into the channel_provider registry. " +
		"A unit supplies no channel transport, so it has no provider to name and the empty value is the accurate answer rather than a dropped one — " +
		"the same reason Counterparty.ChannelIdentity is waived below. A unit that does supply one publishes this alongside the declaration that makes it true",
})

// TestThePublishedActivityMirrorsTheCoreOne is the same rule one level in. The
// activity shape is the payload the sink switches on, and a field added there
// is one a unit's captured message would silently never carry.
func TestThePublishedActivityMirrorsTheCoreOne(t *testing.T) {
	published := fieldsOf(reflect.TypeOf(extension.ActivityFields{}))
	core := reflect.TypeOf(capture.ActivityFields{})
	if core.NumField() == 0 {
		t.Fatal("the core activity shape reflected as empty")
	}
	for i := range core.NumField() {
		field := core.Field(i)
		if !published[field.Name] && !waivedActivityFields.Waived(t, field.Name) {
			t.Errorf("capture.ActivityFields.%s is not on extension.ActivityFields — a unit cannot state it, so every ingested activity takes the zero value. "+
				"Publish it, or waive it in this file with the reason the zero value is correct", field.Name)
		}
	}
	waivedActivityFields.AssertAllMatched(t)
	// And the other way, which is the failure mode the mirror alone misses: a
	// published field the port has nothing to map onto is a promise to a unit
	// that the core drops on the floor.
	coreFields := fieldsOf(core)
	for name := range published {
		if !coreFields[name] {
			t.Errorf("extension.ActivityFields.%s has no counterpart in capture.ActivityFields — a unit sets it and the core never sees it", name)
		}
	}
}

// TestThePublishedCounterpartyCarriesWhatAUnitMaySay walks the core
// counterparty, where the interesting fields are the ones a unit must NOT be
// able to state.
var waivedCounterpartyFields = gatekit.Waive(map[string]string{
	"ChannelIdentity": "keys person_channel_identity, whose provider vocabulary is a foreign key into the channel_provider registry — a unit reaching it would be naming a transport it does not supply",
	"ListUnsubscribe": "the bulk-mail corroboration a mail connector reads off an RFC 2369 header. A chat record has no such header, and a unit asserting one would be evidence for a suppression decision that nothing produced",
})

func TestThePublishedCounterpartyCarriesWhatAUnitMaySay(t *testing.T) {
	published := fieldsOf(reflect.TypeOf(extension.Counterparty{}))
	core := reflect.TypeOf(connector.Counterparty{})
	for i := range core.NumField() {
		field := core.Field(i)
		// An unexported field is already unstatable by anything outside its
		// package — sentByOwner is the T1 attestation, kept that way on
		// purpose — so it needs no waiver here.
		if !field.IsExported() || published[field.Name] {
			continue
		}
		if !waivedCounterpartyFields.Waived(t, field.Name) {
			t.Errorf("connector.Counterparty.%s is neither published nor waived — decide whether a unit may state it, and record which", field.Name)
		}
	}
	waivedCounterpartyFields.AssertAllMatched(t)
}

// TestTheDirectionVocabulariesAgree: the published constants are restated
// rather than imported (a stdlib-only surface may not reach the core's port),
// and two spellings of one vocabulary is exactly the kind of copy that drifts —
// a unit stating "inbound" against a core that had moved to "in" would have
// every record accepted and mis-directed.
func TestTheDirectionVocabulariesAgree(t *testing.T) {
	if extension.DirectionInbound != connector.DirectionInbound {
		t.Errorf("inbound: published %q, core %q", extension.DirectionInbound, connector.DirectionInbound)
	}
	if extension.DirectionOutbound != connector.DirectionOutbound {
		t.Errorf("outbound: published %q, core %q", extension.DirectionOutbound, connector.DirectionOutbound)
	}
}

// TestThePublishedActivityTypesMatchTheCoresA published field whose TYPE
// diverged would compile on both sides and lose meaning in the middle — a
// string where the core wants a time is the obvious one, and it is the one a
// hand-written bridge makes possible.
func TestThePublishedActivityTypesMatchTheCores(t *testing.T) {
	core := reflect.TypeOf(capture.ActivityFields{})
	published := reflect.TypeOf(extension.ActivityFields{})
	for i := range core.NumField() {
		field := core.Field(i)
		mirrored, ok := published.FieldByName(field.Name)
		if !ok {
			continue // reported by the mirror test above
		}
		if mirrored.Type != field.Type {
			t.Errorf("ActivityFields.%s is %s on the published surface and %s in the core", field.Name, mirrored.Type, field.Type)
		}
	}
	// The one that would hurt most, asserted by name as well as by the walk:
	// an occurred-at that stopped being a time would leave the timeline
	// ordered by whatever a unit's string sorted as.
	if occurred, ok := published.FieldByName("OccurredAt"); !ok || occurred.Type != reflect.TypeOf(time.Time{}) {
		t.Error("extension.ActivityFields.OccurredAt is not a time.Time")
	}
}

func fieldsOf(t reflect.Type) map[string]bool {
	out := make(map[string]bool, t.NumField())
	for i := range t.NumField() {
		out[t.Field(i).Name] = true
	}
	return out
}

// A unit has TWO doors it can write an activity through, and the rule that it
// may not claim a messaging transport is about the unit rather than about a
// door. This walks both against the same refusal, because the failure mode is
// asymmetric and only one half is loud: capture ingress carries no provider
// field, so a message there dies on the CHECK as an unattributable 500 — while
// the core-write door DOES carry channel_provider, so a unit could name a core
// connector's transport and mint a row that is a valid SEND ANCHOR for a
// conversation it does not own.
//
// Two halves, because the rule and its reach can fail separately: the block
// below pins what the refusal DOES, and the source assertion after it pins that
// both doors actually call it — which is the half that regresses, since a door
// can be added or rewritten without anyone noticing the gate went missing.
func TestAUnitMayNotFileAMessageThroughEitherDoor(t *testing.T) {
	if err := refuseUnitMessageKind(activities.KindMessage); err == nil {
		t.Fatalf("a unit filing %q was permitted; it would mint a send anchor for a transport the unit does not supply", activities.KindMessage)
	} else if !errors.Is(err, extension.ErrInvalid) {
		t.Errorf("the refusal is %v, want extension.ErrInvalid so the unit reads it as a bad record rather than a core fault", err)
	}

	// And it refuses ONLY that: a unit's ordinary records must still land, or
	// the guard has quietly closed the ingress surface it was meant to bound.
	for _, kind := range []string{"note", "email", "call", "meeting", "task"} {
		if err := refuseUnitMessageKind(kind); err != nil {
			t.Errorf("a unit filing %q was refused (%v); only the message kind is withheld", kind, err)
		}
	}

	// Both doors reach it. A behavioural walk of the core-write door needs a
	// live pool and transaction, so this is a source assertion instead — the
	// same shape as the repo's other whole-tree greps, and it fails loudly when
	// a door stops calling the gate rather than when someone happens to test
	// that door.
	for _, door := range []string{"extingress.go", "extcore.go"} {
		src, err := os.ReadFile(door)
		if err != nil {
			t.Fatalf("reading %s: %v", door, err)
		}
		if !strings.Contains(string(src), "refuseUnitMessageKind(") {
			t.Errorf("%s writes an activity for a unit and does not call refuseUnitMessageKind; that door can mint a message a unit has no transport for", door)
		}
	}
}
