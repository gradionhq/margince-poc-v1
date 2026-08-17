// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package zalooa

// What this unit records about its OWN table: the ledger row, and the event
// other listeners hear.
//
// EVERY state change on the connection is recorded — an authorization starts, an
// account connects, the cursor moves, the credential rotates, the package lapses,
// somebody disconnects — because each is a fact somebody may later ask about,
// and for this unit one of them is a fact somebody will DEFINITELY ask about: a
// rotation that was issued and not kept is unrecoverable, and the ledger is where
// the question "when did this connection last renew" has an answer.
//
// The one write that is NOT recorded is the poll's last_polled_at touch on an
// otherwise unchanged row, and that exemption is stated where it is taken
// (poll.go), not here.

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
const connectionEntity = "ext_zalo_oa_connection"

// The verbs this unit publishes about its own row. The type on the bus is
// `ext_zalo_oa.<verb>` — the core prefixes the namespace, so these are verbs and
// not types.
const (
	// eventAuthorizationStarted is a permission URL minted and a verifier sealed.
	// It is published because the pair that follows it may not: an authorization
	// nobody completed is the ordinary explanation for a connection that never
	// appeared, and it is invisible without this.
	eventAuthorizationStarted = "authorization_started"
	eventConnected            = "connected"
	eventDisconnected         = "disconnected"
	eventPolled               = "polled"
	// eventCredentialRotated is one refresh that succeeded and was kept. It is
	// the counterpart of the class refresh_rotation_lost: between the two, a
	// human can always tell which side of a single-use rotation a connection is
	// on.
	//nolint:gosec // G101 false positive: an event verb naming that a renewal happened, not a credential
	eventCredentialRotated = "credential_rotated"
	eventReauth            = "reauth_required"
	eventTierLapsed        = "tier_lapsed"
	// eventRecordDropped is one message this connector will never land. It is
	// published because the alternative is silence, and a connector dropping
	// everything looks exactly like an account nobody writes to.
	eventRecordDropped = "record_dropped"
)

// recordConnection writes the ledger row and the event for one connection write,
// in the caller's transaction.
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
		return fmt.Errorf("zalo-oa: recording a %s needs one image — the row's id comes from whichever side of the write it has", verb)
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
		OAID   string `json:"oa_id,omitempty"`
	}{Status: subject.Status, OAID: subject.OAID})
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
// filtering here: the row has no token column. The credential lives in the
// unit's sealed secret namespace, so an audit trail of connections cannot become
// a place credentials are kept in the clear.
func connectionImage(c *connection) (json.RawMessage, error) {
	if c == nil {
		return nil, nil
	}
	return json.Marshal(c)
}
