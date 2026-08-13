// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package extension

// What the published ledger surface refuses before any core sees it. Every case
// here is a value a unit could reasonably build and the core would otherwise
// discover inside a transaction, where the honest report is a SQLSTATE.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

const testRowID = "018f3a2b-6c7d-7e8f-9a0b-1c2d3e4f5061"

func TestChangeValidateAcceptsAWellFormedWrite(t *testing.T) {
	ch := Change{
		Action: AuditUpdate,
		Entity: "ext_notes_note",
		ID:     testRowID,
		Before: json.RawMessage(`{"filed_activity_id":"a"}`),
		After:  json.RawMessage(`{"filed_activity_id":null}`),
		Detail: json.RawMessage(`{"cause":"activity.archived"}`),
	}
	if err := ch.Validate(); err != nil {
		t.Fatalf("a well-formed change was refused: %v", err)
	}

	// A create has no before-image and an erase has no after-image; neither is
	// a malformed change, and refusing either would make the honest write
	// impossible to describe.
	for _, ch := range []Change{
		{Action: AuditCreate, Entity: "ext_notes_note", ID: testRowID, After: json.RawMessage(`{}`)},
		{Action: AuditErase, Entity: "ext_notes_note", ID: testRowID, Before: json.RawMessage(`{}`)},
	} {
		if err := ch.Validate(); err != nil {
			t.Errorf("a %s with one image was refused: %v", ch.Action, err)
		}
	}
}

func TestChangeValidateRefusesWhatTheLedgerCannotHold(t *testing.T) {
	valid := Change{Action: AuditCreate, Entity: "ext_notes_note", ID: testRowID}
	for name, mutate := range map[string]func(*Change){
		// The ledger's action column is a closed CHECK; an unknown verb would
		// fail inside the transaction with a constraint name for a message.
		"an action outside the ledger's vocabulary": func(c *Change) { c.Action = "merge" },
		"no action at all":                          func(c *Change) { c.Action = "" },
		// entity_type names a KIND of record. A core one would put a line into
		// a core record's history describing a write the core never made.
		"a core entity type":       func(c *Change) { c.Entity = "person" },
		"a schema-qualified table": func(c *Change) { c.Entity = "ext.ext_notes_note" },
		"no entity":                func(c *Change) { c.Entity = "" },
		// entity_id is a uuid column.
		"an id that is not a UUID": func(c *Change) { c.ID = "note-7" },
		"an upper-case UUID":       func(c *Change) { c.ID = strings.ToUpper(testRowID) },
		"no id":                    func(c *Change) { c.ID = "" },
		// The three images are jsonb.
		"a before image that is not JSON": func(c *Change) { c.Before = json.RawMessage(`{`) },
		"an after image that is not JSON": func(c *Change) { c.After = json.RawMessage(`nope`) },
		"a detail that is not JSON":       func(c *Change) { c.Detail = json.RawMessage(`{"a":}`) },
	} {
		ch := valid
		mutate(&ch)
		if err := ch.Validate(); err == nil {
			t.Errorf("Change.Validate accepted %s", name)
		}
	}
}

func TestEventValidateRefusesWhatTheBusCannotCarry(t *testing.T) {
	if err := (Event{Verb: "note_added", Payload: json.RawMessage(`{"id":"x"}`)}).Validate(); err != nil {
		t.Fatalf("a well-formed event was refused: %v", err)
	}
	// An event with no payload is the common case: the subject carries it.
	if err := (Event{Verb: "note_added"}).Validate(); err != nil {
		t.Fatalf("an event with no payload was refused: %v", err)
	}

	for name, ev := range map[string]Event{
		"a verb that is not lower snake_case": {Verb: "NoteAdded"},
		"a verb starting with a digit":        {Verb: "2nd_note"},
		"a namespaced type rather than a verb": {
			// The namespace is the core's to supply. A unit spelling one would
			// be publishing under a name it does not own — here it is simply
			// not a verb.
			Verb: "ext_notes.note_added",
		},
		"no verb":                    {},
		"a payload that is not JSON": {Verb: "note_added", Payload: json.RawMessage(`{`)},
		"a payload past the cap": {
			Verb:    "note_added",
			Payload: json.RawMessage(`"` + strings.Repeat("x", MaxEventPayloadBytes) + `"`),
		},
	} {
		if err := ev.Validate(); err == nil {
			t.Errorf("Event.Validate accepted %s", name)
		}
	}
}

// The pairing the surface exists to guarantee: one call carries the ledger row
// and the event, so neither can be written without the other. It is the
// product's own write shape, and a unit gets it by construction rather than by
// remembering to make a second call.
func TestOneCallRecordsTheLedgerRowAndTheEvent(t *testing.T) {
	tx := &fakeTx{}
	if err := tx.Record(context.Background(), Change{
		Action: AuditCreate, Entity: "ext_notes_note", ID: testRowID,
		After: json.RawMessage(`{"body":"hello"}`),
	}, Event{Verb: "note_added"}); err != nil {
		t.Fatalf("recording a well-formed write: %v", err)
	}
	if len(tx.audited) != 1 || len(tx.published) != 1 {
		t.Fatalf("audited %d changes and published %d events, want 1 and 1", len(tx.audited), len(tx.published))
	}
	if tx.published[0].Verb != "note_added" {
		t.Errorf("published verb %q, want note_added", tx.published[0].Verb)
	}

	// Either half being malformed refuses the whole call, so a unit can never
	// end up with one of the two written.
	for name, call := range map[string]struct {
		ch Change
		ev Event
	}{
		"a change the ledger refuses": {
			Change{Action: AuditCreate, Entity: "person", ID: testRowID}, Event{Verb: "note_added"},
		},
		"an event the bus refuses": {
			Change{Action: AuditCreate, Entity: "ext_notes_note", ID: testRowID}, Event{Verb: "Note Added"},
		},
	} {
		before, published := len(tx.audited), len(tx.published)
		if err := tx.Record(context.Background(), call.ch, call.ev); err == nil {
			t.Errorf("Record accepted %s", name)
		}
		if len(tx.audited) != before || len(tx.published) != published {
			t.Errorf("%s left a half-written record behind", name)
		}
	}
}
