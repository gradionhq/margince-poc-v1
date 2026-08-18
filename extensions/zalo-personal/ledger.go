// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package zalopersonal

// What this unit records about its OWN table: the ledger row, and the event
// other listeners hear.
//
// EVERY state change on a connection is recorded, and here that is a stronger
// obligation than bookkeeping. A row appearing means an installation has
// acquired the ability to read a named human's personal messages, and a row
// leaving means it has given that up. Both are facts somebody — the member, an
// auditor, a controller answering a subject request — may later need answered
// with a time on it.

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

// connectionEntity is what the LEDGER calls this unit's table, and
// connectionTable is what SQL calls it: audit_log.entity_type names a kind of
// record and takes no schema, while a statement resolves through a search_path
// the ext schema is not on. One is derived from the other so the two spellings
// cannot drift into two tables.
const connectionEntity = "ext_zalo_personal_connection"

// The verbs this unit publishes about its own rows. The type on the bus is
// `ext_zalo_personal.<verb>` — the core prefixes the namespace, so these are
// verbs and not types. Only the two this change can actually produce are
// declared; the capture that lands next brings its own.
const (
	eventConnected    = "connected"
	eventDisconnected = "disconnected"
)

// recordConnection writes the ledger row and the event for one connection
// write, in the caller's transaction.
//
// before and after are the row's own images: every statement here RETURNs the
// row it wrote, so what is recorded is what the database holds rather than what
// this code believed it sent.
func recordConnection(ctx context.Context, tx extension.Tx, action extension.AuditAction, verb string,
	before, after *connection,
) error {
	subject := after
	if subject == nil {
		subject = before
	}
	if subject == nil {
		return fmt.Errorf("zalo-personal: recording a %s needs one image — the row's id comes from whichever side of the write it has", verb)
	}
	beforeImage, err := connectionImage(before)
	if err != nil {
		return err
	}
	afterImage, err := connectionImage(after)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(struct {
		Status string `json:"status"`
	}{Status: subject.Status})
	if err != nil {
		return err
	}
	return tx.Record(ctx,
		extension.Change{
			Action: action,
			Entity: connectionEntity,
			ID:     subject.ID,
			Before: beforeImage,
			After:  afterImage,
		},
		extension.Event{Verb: verb, Payload: payload})
}

// connectionImage renders one side of a change, or nothing at all. A missing
// image is nil rather than `null`: a create has no before, and the ledger reads
// "there was no such state" as an absent column rather than a JSON null sitting
// in one.
//
// THE IMAGE CARRIES NO SESSION, which is true by construction rather than by
// filtering here: the row has no such column. The member's credential lives in
// the unit's sealed secret namespace, so an audit trail of connections cannot
// become a place personal-account sessions are kept in the clear.
func connectionImage(c *connection) (json.RawMessage, error) {
	if c == nil {
		return nil, nil
	}
	return json.Marshal(c)
}
