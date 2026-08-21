// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package relayprobe

// What this unit records about its OWN table: the ledger row, and the event
// other listeners hear.
//
// EVERY state change on a connection is recorded — it appears, it moves, it
// breaks, it goes — because each is a fact somebody may later ask about: who
// connected this member's account, when the cursor last moved, when the token
// stopped working. The one write that is NOT recorded is the poll's
// last_polled_at touch on an otherwise unchanged row, and that exemption is
// stated where it is taken (poll.go), not here.

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
const connectionEntity = "ext_relay_probe_connection"

// The verbs this unit publishes about its own rows. The type on the bus is
// `ext_relay_probe.<verb>` — the core prefixes the namespace, so these
// are verbs and not types.
const (
	eventConnected    = "connected"
	eventDisconnected = "disconnected"
	eventPolled       = "polled"
	eventReauth       = "reauth_required"
	// eventRecordDropped is one notification this connector will never land —
	// a sender with no address, a record the core's grammar refuses. It is
	// published because the alternative is silence, and a connector dropping
	// everything looks exactly like a quiet feed.
	eventRecordDropped = "record_dropped"
)

// recordConnection writes the ledger row and the event for one connection
// write, in the caller's transaction.
//
// before and after are the row's own images, which every statement here has in
// hand: each RETURNs the row it wrote, so what is recorded is what the database
// holds rather than what this code believed it sent.
func recordConnection(ctx context.Context, tx extension.Tx, action extension.AuditAction, verb string,
	before, after *connection,
) error {
	subject := after
	if subject == nil {
		subject = before
	}
	if subject == nil {
		return fmt.Errorf("relay: recording a %s needs one image — the row's id comes from whichever side of the write it has", verb)
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
// image is nil rather than `null`: a create has no before and an erase has no
// after, and the ledger's own reading of "there was no such state" is an absent
// column rather than a JSON null sitting in one.
//
// THE IMAGE CARRIES NO TOKEN, which is true by construction rather than by
// filtering here: the row has no token column. The member's credential lives in
// the unit's sealed secret namespace, so an audit trail of connections cannot
// become a place credentials are kept in the clear.
func connectionImage(c *connection) (json.RawMessage, error) {
	if c == nil {
		return nil, nil
	}
	return json.Marshal(c)
}
