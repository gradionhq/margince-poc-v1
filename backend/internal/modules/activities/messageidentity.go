// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

// The activity side of the outbound-send reconcile: a sent message re-keyed
// onto the RFC822 identity its provider actually stamped, absorbing the
// provider's own captured echo of it when that echo won the race. The delivery
// half lives in comms, which declares the seam this satisfies and owns the
// savepoint the call runs inside; all SQL against activity lives here, where
// the table is owned.

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// uqActivitySource is the natural-key index a captured echo trips the re-key
// against: (workspace_id, source_system, source_id). It is named because the
// absorb below is a legitimate answer to THIS constraint alone — any other
// uniqueness rule firing on the same statement is a fault, and treating it as
// the race would delete a row for a reason nobody established.
const uqActivitySource = "uq_activity_source"

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
// The caller owns the transaction AND a savepoint around this call. Every
// error raised here therefore travels to the caller rather than being
// contained, and degrades to "receipt recorded, one duplicate timeline row"
// instead of un-sending a sent message.
func (s *Store) ReconcileMessageIdentityTx(ctx context.Context, tx pgx.Tx, activityID ids.ActivityID, previous, stamped string) error {
	if stamped == "" || stamped == previous {
		// The provider honoured the identity, reports none, or could not be
		// asked. All three mean the staged key is already the wire's key, so
		// writing anything would bump a version over a change nobody made.
		return nil
	}
	moved, err := reKeyAbsorbingTheEcho(ctx, tx, activityID, previous, stamped)
	if err != nil {
		return err
	}
	if !moved {
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

// reKeyAbsorbingTheEcho moves the natural key, and turns the ONE collision the
// move can legitimately lose into a branch instead of a fault. It reports
// whether a row was actually re-keyed.
//
// The collision is the provider's own echo of this message. Capture cannot
// learn of a send until the send is recorded, so an echo that arrived before
// this reconcile committed already holds the stamped natural key. Looking for
// it first would not help — READ COMMITTED lets the echo commit between the
// look and the UPDATE — so the violation is CAUGHT rather than prevented, and
// the row it names is absorbed into this one.
//
// The first attempt runs in a savepoint of its own, nested inside the caller's,
// because a failed statement aborts the whole transaction in Postgres: without
// one there would be nothing left to run the absorb ON. Same shape and same
// reason as capture's decideCounterpartyGuarded.
//
// Exactly ONE retry. A second violation is not this race repeating — the echo
// it named is gone — so it travels to the caller, whose savepoint degrades it.
func reKeyAbsorbingTheEcho(ctx context.Context, tx pgx.Tx, activityID ids.ActivityID, previous, stamped string) (bool, error) {
	sp, err := tx.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("activities: opening the message-identity re-key savepoint: %w", err)
	}
	moved, reKeyErr := reKeyActivity(ctx, sp, activityID, previous, stamped)
	if reKeyErr == nil {
		if err := sp.Commit(ctx); err != nil {
			return false, fmt.Errorf("activities: committing the message-identity re-key: %w", err)
		}
		return moved, nil
	}
	constraint, collided := storekit.UniqueViolation(reKeyErr)
	if rbErr := sp.Rollback(ctx); rbErr != nil {
		// Without a clean rollback the caller's transaction stays poisoned, so
		// there is nothing left to absorb onto and no second attempt to make.
		// Both causes travel: the rollback failure explains why the collision
		// was not answered.
		return false, errors.Join(reKeyErr, rbErr)
	}
	if !collided || constraint != uqActivitySource {
		return false, reKeyErr
	}
	if err := absorbEcho(ctx, tx, activityID, stamped); err != nil {
		return false, err
	}
	return reKeyActivity(ctx, tx, activityID, previous, stamped)
}

// reKeyActivity writes the stamped identity onto the row, moving thread_key
// with it ONLY when the two were equal — a conversation ROOT re-roots onto the
// identity the world will reply to, while a REPLY's thread key is the root of
// the conversation it joined and belongs to that conversation.
func reKeyActivity(ctx context.Context, tx pgx.Tx, activityID ids.ActivityID, previous, stamped string) (bool, error) {
	tag, err := tx.Exec(ctx, `
		UPDATE activity
		   SET source_id  = $2,
		       thread_key = CASE WHEN thread_key = $3 THEN $2 ELSE thread_key END
		 WHERE id = $1`, activityID, stamped, previous)
	if err != nil {
		return false, fmt.Errorf("activities: re-keying the message identity: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// absorbEcho folds the provider's captured copy of this message into the row
// the send wrote, and deletes it.
//
// OUR row is the survivor, never the echo: it holds the delivery this message
// was sent through, the consent purpose it was sent under, the draft outcome
// and counterparty_outbound_attested — facts capture can neither observe nor
// recreate. The echo holds only what capture derived from the bytes. The order
// below is forced by the schema: every foreign key onto activity is ON DELETE
// CASCADE, so whatever must survive moves BEFORE the delete.
//
// Three of the echo's dependents are deliberately not moved.
// person_signature_enrich_state cascades away, which is what its own design
// asks for — losing the activity honestly reopens the person for a fresh read.
// field_provenance and embedding carry no foreign key, so nothing cascades and
// both are left orphaned: every read reaches them THROUGH an activity id that
// no longer resolves, so they are unreachable rather than wrong, and deleting
// them from here would buy a table-ownership waiver nothing.
func absorbEcho(ctx context.Context, tx pgx.Tx, survivorID ids.ActivityID, stamped string) error {
	var echoID ids.ActivityID
	// The violated index keys on (workspace_id, source_system, source_id) and
	// the re-key changes only the last of the three, so the row that holds the
	// key is the one sharing this row's source system under the stamped id.
	err := tx.QueryRow(ctx, `
		SELECT echo.id
		  FROM activity echo
		  JOIN activity survivor ON survivor.id = $1
		 WHERE echo.id <> survivor.id
		   AND echo.source_system = survivor.source_system
		   AND echo.source_id = $2`, survivorID, stamped).Scan(&echoID)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("activities: the re-key collided on %s but no readable row holds the identity %q",
			uqActivitySource, stamped)
	}
	if err != nil {
		return fmt.Errorf("activities: finding the captured echo of the sent message: %w", err)
	}
	if err := repointEchoLinks(ctx, tx, survivorID, echoID); err != nil {
		return err
	}
	if err := repointEchoReviews(ctx, tx, survivorID, echoID); err != nil {
		return err
	}
	return deleteAbsorbedEcho(ctx, tx, survivorID, echoID)
}

// repointEchoLinks moves the echo's timeline placements onto the survivor —
// the sent mail's presence on an auto-created person's record, which is most of
// what the echo's row was worth.
//
// Two kinds of link are deliberately left behind for the delete to cascade. One
// the survivor already holds, because uq_activity_link keys on the activity,
// the entity type, and the five target columns coalesced into one — re-pointing
// a duplicate would raise the very violation this path exists to answer. And a
// project link whenever the survivor holds one at all, because
// uq_activity_link_project permits exactly one per activity WHATEVER the
// target: the link ladder decides once and never overwrites, so the survivor's
// project wins rather than the echo's replacing it.
func repointEchoLinks(ctx context.Context, tx pgx.Tx, survivorID, echoID ids.ActivityID) error {
	if _, err := tx.Exec(ctx, `
		UPDATE activity_link echo_link SET activity_id = $1
		 WHERE echo_link.activity_id = $2
		   AND NOT EXISTS (
		       SELECT 1 FROM activity_link held
		        WHERE held.activity_id = $1
		          AND held.entity_type = echo_link.entity_type
		          AND coalesce(held.person_id, held.organization_id, held.deal_id, held.lead_id, held.project_id)
		            = coalesce(echo_link.person_id, echo_link.organization_id, echo_link.deal_id, echo_link.lead_id, echo_link.project_id))
		   AND NOT (echo_link.entity_type = 'project' AND EXISTS (
		       SELECT 1 FROM activity_link held
		        WHERE held.activity_id = $1 AND held.entity_type = 'project'))`,
		survivorID, echoID); err != nil {
		return fmt.Errorf("activities: re-pointing the absorbed echo's timeline links: %w", err)
	}
	return nil
}

// repointEchoReviews moves the echo's queued counterparty dispositions onto the
// survivor. What those rows hold is a queued HUMAN review, and an ensure-retry
// cursor, that the survivor does not re-queue: letting them cascade away would
// silently drop a counterparty derivation nothing else raises again. Their
// live-row uniqueness keys on (workspace_id, email), which this write does not
// touch, so a re-point can collide with nothing.
func repointEchoReviews(ctx context.Context, tx pgx.Tx, survivorID, echoID ids.ActivityID) error {
	if _, err := tx.Exec(ctx,
		`UPDATE capture_pending_counterparty SET activity_id = $1 WHERE activity_id = $2`,
		survivorID, echoID); err != nil {
		return fmt.Errorf("activities: re-pointing the absorbed echo's queued counterparty reviews: %w", err)
	}
	return nil
}

// deleteAbsorbedEcho removes the row that was folded in, audit-only.
//
// 'merge' is the verb because that is what happened — two rows for one message
// collapse into one — and the images name the row that went and the row it went
// into, the same shape a person merge audits. There is no EVENT: the catalog
// has no verb for a hard delete, and activity.archived would be a lie, because
// nothing was archived. A subscriber holding the echo's id from its already
// relayed activity.captured is the accepted residue — that delivery has
// happened and no later event can un-deliver it.
func deleteAbsorbedEcho(ctx context.Context, tx pgx.Tx, survivorID, echoID ids.ActivityID) error {
	if _, err := tx.Exec(ctx, `DELETE FROM activity WHERE id = $1`, echoID); err != nil {
		return fmt.Errorf("activities: deleting the absorbed echo: %w", err)
	}
	if _, err := storekit.Audit(ctx, tx, "merge", "activity", echoID.UUID,
		map[string]any{"merged_into_id": nil},
		map[string]any{"merged_into_id": survivorID}); err != nil {
		return fmt.Errorf("activities: auditing the absorbed echo: %w", err)
	}
	return nil
}
