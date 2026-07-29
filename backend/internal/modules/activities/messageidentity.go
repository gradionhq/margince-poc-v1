// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

// The activity side of the outbound-send reconcile: a sent message re-keyed
// onto the RFC822 identity its provider actually stamped. The delivery half
// lives in comms, which declares the seam this satisfies and owns the
// savepoint the call runs inside; all SQL against activity lives here, where
// the table is owned.

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// ReconcileMessageIdentityTx re-keys one sent message onto the RFC822 identity
// its provider actually stamped.
//
// The send path mints an identity and stages the timeline row under it, because
// a message must carry one and nothing else knows it yet. A provider is free to
// discard it — Gmail does — and when it does, the row is keyed on an identity
// that exists nowhere on the wire: the provider's echo of the message arrives
// under a DIFFERENT natural key and creates a second row, and the counterparty's
// reply roots at that other key and attributes to the echo instead of to the
// send. Re-keying here is what makes the echo's upsert collapse onto this row
// and the reply's thread join find it.
//
// thread_key moves ONLY when it equalled the message's own identity. That is
// the difference between a conversation ROOT — which re-roots, because the
// world will reply to the stamped identity — and a REPLY, whose thread_key is
// the root of the conversation it joined and belongs to that conversation, not
// to this message.
//
// source is deliberately untouched: it says a human wrote this ('manual'), and
// re-keying the transport identity does not make it connector-ingested.
//
// The caller owns the transaction AND a savepoint around this call. A unique
// violation raised here — an echo already holding the stamped key — therefore
// travels to the caller rather than being contained, and degrades to "receipt
// recorded, one duplicate timeline row" instead of un-sending a sent message.
func (s *Store) ReconcileMessageIdentityTx(ctx context.Context, tx pgx.Tx, activityID ids.ActivityID, previous, stamped string) error {
	if stamped == "" || stamped == previous {
		// The provider honoured the identity, reports none, or could not be
		// asked. All three mean the staged key is already the wire's key, so
		// writing anything would bump a version over a change nobody made.
		return nil
	}
	tag, err := tx.Exec(ctx, `
		UPDATE activity
		   SET source_id  = $2,
		       thread_key = CASE WHEN thread_key = $3 THEN $2 ELSE thread_key END
		 WHERE id = $1`, activityID, stamped, previous)
	if err != nil {
		return fmt.Errorf("activities: re-keying the message identity: %w", err)
	}
	if tag.RowsAffected() == 0 {
		// The row went away between transmit and receipt — an Art. 17 erasure
		// hard-deletes it. There is no identity left to correct, and the send
		// itself is already recorded by the delivery row the caller holds.
		return nil
	}
	// 'update' is the audit verb, and the before/after images name the two
	// identities: this row IS the operator-visible evidence that the mailbox
	// rewrites what it is given, so no separate flag or counter has to exist.
	auditID, err := storekit.Audit(ctx, tx, "update", "activity", activityID.UUID,
		map[string]any{"source_id": previous}, map[string]any{"source_id": stamped})
	if err != nil {
		return fmt.Errorf("activities: auditing the message-identity re-key: %w", err)
	}
	// activity.updated's changed_fields is a BOUNDED delta over the fields a
	// human patches, and the natural key is not one of them — so the event
	// carries no delta and says only "re-read this activity". That is the whole
	// job here: a read model or an E10 subscriber holding the minted identity
	// is holding one that resolves nowhere. It fires no user automation either:
	// the trigger catalog's only activity trigger is activity.captured.
	if err := storekit.EmitEvent(ctx, tx, auditID, activityID.UUID,
		crmcontracts.PublicEventActivityUpdated{}); err != nil {
		return fmt.Errorf("activities: emitting the message-identity re-key: %w", err)
	}
	return nil
}
