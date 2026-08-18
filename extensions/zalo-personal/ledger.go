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
	"errors"
	"fmt"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

// connectionEntity is what the LEDGER calls this unit's table, and
// connectionTable is what SQL calls it: audit_log.entity_type names a kind of
// record and takes no schema, while a statement resolves through a search_path
// the ext schema is not on. One is derived from the other so the two spellings
// cannot drift into two tables.
const connectionEntity = "ext_zalo_personal_connection"

// allowlistEntity's ledger name is declared beside the table in allowlist.go,
// for the same reason connectionEntity is declared here: one spelling, derived
// into the schema-qualified one, so the two cannot drift into two tables.

// The verbs this unit publishes about its own rows. The type on the bus is
// `ext_zalo_personal.<verb>` — the core prefixes the namespace, so these are
// verbs and not types.
const (
	// A member connected their own account, or withdrew it.
	eventConnected    = "connected"
	eventDisconnected = "disconnected"
	// A member decided about one counterparty. One event per verdict, because
	// which conversations an installation was permitted to read is the record of
	// their consent and a count cannot answer that question later.
	eventVerdictSet = "verdict_set"
	// The moment this installation began reading a named human's personal
	// messages at all. It is announced separately from the verdicts that caused
	// it because it happens ONCE per connection and is the fact a listener would
	// actually act on.
	eventCaptureArmed = "capture_armed"
	// A tick that learned something: a cursor that moved, or a class that
	// changed. A tick that found nothing announces nothing.
	eventPolled = "polled"
	// A session that stopped being accepted. Separate from `polled` because the
	// remedy is a human with a phone, not a retry.
	eventReconnectNeeded = "reconnect_needed"
	// A member took somebody off their list entirely. Announced separately from
	// verdict_set because it is the only one that REMOVES a decision, and "when did
	// this person stop being on the list" is a question somebody will ask.
	eventVerdictDropped = "verdict_dropped"
	// A message that passed the member's filter and still could not be landed.
	// It carries the message id and never the message.
	eventMessageDropped = "message_dropped"
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

// recordVerdict writes the ledger row and the event for one verdict, in the
// caller's transaction.
//
// It records the COUNTERPARTY and the decision and nothing about the
// conversation, which is the whole of what a later reader needs: whether this
// installation was ever permitted to read a named person's messages, and when
// that changed.
func recordVerdict(ctx context.Context, tx extension.Tx, before, after *allowEntry) error {
	if after == nil {
		return errors.New("zalo-personal: recording a verdict needs the row the write returned — the ledger's id comes from it")
	}
	beforeImage, err := verdictImage(before)
	if err != nil {
		return err
	}
	afterImage, err := verdictImage(after)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(struct {
		ChannelUserID string `json:"channel_user_id"`
		Mode          string `json:"mode"`
	}{ChannelUserID: after.ChannelUserID, Mode: string(after.Mode)})
	if err != nil {
		return err
	}
	return tx.Record(ctx,
		extension.Change{
			Action: verdictActionFor(before),
			Entity: allowlistEntity,
			ID:     after.ID,
			Before: beforeImage,
			After:  afterImage,
		},
		extension.Event{Verb: eventVerdictSet, Payload: payload})
}

// verdictActionFor reports whether this write created the verdict or changed one.
// Recording a change as a create would put a ledger row with no before-image over
// a state that existed, and read as "this person was allowed for the first time"
// for a member who merely re-saved their list.
func verdictActionFor(before *allowEntry) extension.AuditAction {
	if before == nil {
		return extension.AuditCreate
	}
	return extension.AuditUpdate
}

// verdictImage renders one side of a verdict change, or nothing at all. A missing
// image is nil rather than `null`, exactly as connectionImage's is.
func verdictImage(entry *allowEntry) (json.RawMessage, error) {
	if entry == nil {
		return nil, nil
	}
	return json.Marshal(struct {
		ChannelUserID string `json:"channel_user_id"`
		Mode          string `json:"mode"`
		DisplayName   string `json:"display_name,omitempty"`
		Version       int    `json:"version"`
	}{
		ChannelUserID: entry.ChannelUserID,
		Mode:          string(entry.Mode),
		DisplayName:   entry.DisplayName,
		Version:       entry.Version,
	})
}

// recordVerdictDropped writes the ledger row and the event for one verdict a member
// removed from their list.
//
// AuditErase, and the before-image is the whole record of what was removed: the row
// is gone, so this ledger row is the only place that says a decision about this named
// person ever existed. There is no after-image, which the published grammar enforces.
func recordVerdictDropped(ctx context.Context, tx extension.Tx, before *allowEntry) error {
	if before == nil {
		return errors.New("zalo-personal: recording a removed verdict needs the row that was removed — the ledger's id comes from it")
	}
	beforeImage, err := verdictImage(before)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(struct {
		ChannelUserID string `json:"channel_user_id"`
	}{ChannelUserID: before.ChannelUserID})
	if err != nil {
		return err
	}
	return tx.Record(ctx,
		extension.Change{
			Action: extension.AuditErase,
			Entity: allowlistEntity,
			ID:     before.ID,
			Before: beforeImage,
		},
		extension.Event{Verb: eventVerdictDropped, Payload: payload})
}
