// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package notes

// What this unit records about its OWN writes: the ledger row, and the event
// other listeners hear.
//
// The notepad's rows are the unit's, so nothing in the core can describe a
// write to them — no entity type, no field images, no verb. This file is where
// the unit says those things, and the core supplies everything a unit must not
// choose: the actor, the workspace, the attribution and the trace.

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

// noteEntity is what the LEDGER calls this unit's table, and noteTable is what
// SQL calls it. Both, because they are two different names for one thing:
// audit_log.entity_type names a kind of record and takes no schema, while a
// statement resolves through a search_path the ext schema is not on. Deriving
// one from the other keeps them from drifting apart.
const noteEntity = "ext_notes_note"

// noteEvent is a fact this unit publishes about its own rows. The type on the
// bus is `ext_notes.<verb>` — the core prefixes the namespace, so these are
// verbs and not types.
const (
	eventNoteAdded       = "note_added"
	eventNoteFiled       = "note_filed"
	eventNoteRemoved     = "note_removed"
	eventFilingWithdrawn = "filing_withdrawn"
)

// recordNote writes the ledger row and the event for one note write, in the
// caller's transaction.
//
// It is a shape helper over tx.Record and nothing more: what it saves each
// handler is marshaling two images and remembering which entity this unit owns.
//
// before and after are the row's own images, which the notepad has in hand: the
// insert and the update both RETURN the row they wrote, so what is recorded is
// what the database holds rather than what this code believed it sent.
func recordNote(ctx context.Context, tx extension.Tx, action extension.AuditAction, verb string,
	before, after *note, detail, payload json.RawMessage,
) error {
	subject := after
	if subject == nil {
		subject = before
	}
	if subject == nil {
		return fmt.Errorf("notes: recording a %s needs one image — the row's id comes from whichever side of the write it has", verb)
	}
	beforeImage, err := noteImage(before)
	if err != nil {
		return err
	}
	afterImage, err := noteImage(after)
	if err != nil {
		return err
	}
	return tx.Record(ctx,
		extension.Change{
			Action: action,
			Entity: noteEntity,
			ID:     subject.ID,
			Before: beforeImage,
			After:  afterImage,
			Detail: detail,
		},
		extension.Event{Verb: verb, Payload: payload})
}

// noteImage renders one side of a change, or nothing at all.
//
// A missing image is nil rather than `null`: a create has no before and an
// erase has no after, and the ledger's own reading of "there was no such state"
// is an absent column, not a JSON null sitting in one.
func noteImage(n *note) (json.RawMessage, error) {
	if n == nil {
		return nil, nil
	}
	raw, err := json.Marshal(n)
	if err != nil {
		return nil, err
	}
	return raw, nil
}

// notePayload is what a listener is told about one of this unit's rows: enough
// to decide whether the row is worth reading, and nothing more. The subject's
// id rides the envelope, so what is left is the kind — the one field that
// separates a person's note from the heartbeat's own rows.
func notePayload(n note) (json.RawMessage, error) {
	return json.Marshal(struct {
		Kind noteKind `json:"kind"`
	}{Kind: n.Kind})
}
