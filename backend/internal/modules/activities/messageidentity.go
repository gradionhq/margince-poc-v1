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
// the race would take a row off the timeline for a reason nobody established.
const uqActivitySource = "uq_activity_source"

// sourceIDImage names the audit image field both writes below move. Spelled
// once because they are two halves of one fact: the survivor takes the natural
// key, the folded-in row gives it up.
const sourceIDImage = "source_id"

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
		// A defensive no-op rather than a scenario anyone can name. The caller
		// reached this line by updating a comms_outbound row, and that row's
		// activity_id is a composite foreign key ON DELETE CASCADE (0136): a
		// deleter that took the activity took the delivery with it, so the
		// receipt UPDATE would have matched nothing and reported ErrTerminal
		// long before the reconcile. What this guards is the argument's
		// premise drifting — the key loosened to SET NULL, a future deleter
		// that is not a cascade — for the price of one comparison, instead of
		// auditing a re-key that did not happen.
		return nil
	}
	// 'update' is the audit verb, and the before/after images name the two
	// identities: this row IS the operator-visible evidence that the mailbox
	// rewrites what it is given, so no separate flag or counter has to exist.
	auditID, err := storekit.Audit(ctx, tx, "update", "activity", activityID.UUID,
		map[string]any{sourceIDImage: previous}, map[string]any{sourceIDImage: stamped})
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
// it named has given the identity up — so it travels to the caller, whose
// savepoint degrades it.
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
	moved, err = reKeyActivity(ctx, tx, activityID, previous, stamped)
	if err != nil {
		return false, err
	}
	if !moved {
		// The absorb released the identity and took a row off the timeline for
		// a survivor that then was not there to take it. This is NOT the
		// erasure-shaped no-op the first attempt tolerates: absorbEcho joins
		// the survivor and fails loudly without it, so this transaction saw
		// the row exist moments ago. Reporting it hands the caller's savepoint
		// the rollback that puts the archived row back, and leaves the
		// breadcrumb an operator can act on — returning nil here would leave a
		// message hidden from the timeline to no end.
		return false, fmt.Errorf(
			"activities: the absorb released %s from the captured echo but no surviving row took it", uqActivitySource)
	}
	return true, nil
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
// the send wrote, and archives what is left.
//
// OUR row is the survivor, never the echo: it holds the delivery this message
// was sent through, the consent purpose it was sent under, the draft outcome
// and counterparty_outbound_attested — facts capture can neither observe nor
// recreate. The echo holds only what capture derived from the bytes: what a
// human would otherwise have to raise again moves onto the survivor first, and
// the rest stays on a row that still exists.
//
// WHICH row is absorbed is decided here, and it is decided narrowly on purpose.
// `stamped` is parsed out of a remote provider's response bytes, so a hostile
// or corrupted answer must not be able to nominate an arbitrary activity to be
// taken off the timeline. Holding the collided identity is therefore necessary
// but not sufficient: the row must also be shaped like this message's echo.
func absorbEcho(ctx context.Context, tx pgx.Tx, survivorID ids.ActivityID, stamped string) error {
	var echoID ids.ActivityID
	// The violated index keys on (workspace_id, source_system, source_id) and
	// the re-key changes only the last of the three — that is what makes a row
	// a CANDIDATE. Each further predicate is what makes it this message's echo:
	//
	//   workspace_id — RLS already binds every statement here to the GUC
	//     workspace, and this states the same bound in the join so the query
	//     reads as narrowly as it behaves.
	//   kind/direction — a message this mailbox SENT. An inbound mail, a call
	//     or a meeting sharing the key is a fault, not an echo.
	//   captured_by — the connector's own provenance, which the sink validates
	//     against the acting connector principal before it writes. The
	//     survivor's is 'human:<id>', so this alone rules the send path's own
	//     rows out.
	//   created_at — an echo cannot predate the send it echoes: capture learns
	//     of the message only from the provider's copy of a transmission this
	//     row staged. Both timestamps are stamped by THIS installation, which
	//     occurred_at is not — that one is the provider's Date header, the same
	//     remote input this predicate exists to distrust.
	//
	// A collision that matches none of it is reported rather than absorbed. The
	// caller's savepoint degrades that to "receipt recorded, one duplicate row"
	// plus a breadcrumb, which is the right price for never archiving a row
	// nobody showed was this message.
	err := tx.QueryRow(ctx, `
		SELECT echo.id
		  FROM activity survivor
		  JOIN activity echo
		    ON echo.workspace_id  = survivor.workspace_id
		   AND echo.id           <> survivor.id
		   AND echo.source_system = survivor.source_system
		   AND echo.source_id     = $2
		   AND echo.kind          = 'email'
		   AND echo.direction     = 'outbound'
		   AND echo.captured_by LIKE 'connector:%'
		   AND echo.created_at   >= survivor.created_at
		 WHERE survivor.id = $1`, survivorID, stamped).Scan(&echoID)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("activities: the re-key collided on %s, but no row this workspace holds under that identity is a captured outbound echo of this send",
			uqActivitySource)
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
	return archiveAbsorbedEcho(ctx, tx, survivorID, echoID, stamped)
}

// repointEchoLinks moves the echo's timeline placements onto the survivor —
// the sent mail's presence on an auto-created person's record, which is most of
// what the echo's row was worth.
//
// Two kinds of link are deliberately left where they are, on the row that is
// about to be archived off the timeline. One the survivor already holds,
// because uq_activity_link keys on the activity, the entity type, and the five
// target columns coalesced into one — re-pointing a duplicate would raise the
// very violation this path exists to answer. And a project link whenever the
// survivor holds one at all, because
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
// cursor, that the survivor does not re-queue: left on the row this absorb
// archives, the question "who is this stranger?" would be asked about a message
// the workspace can no longer see. Their live-row uniqueness keys on
// (workspace_id, email), which this write does not touch, so a re-point can
// collide with nothing.
func repointEchoReviews(ctx context.Context, tx pgx.Tx, survivorID, echoID ids.ActivityID) error {
	if _, err := tx.Exec(ctx,
		`UPDATE capture_pending_counterparty SET activity_id = $1 WHERE activity_id = $2`,
		survivorID, echoID); err != nil {
		return fmt.Errorf("activities: re-pointing the absorbed echo's queued counterparty reviews: %w", err)
	}
	return nil
}

// archiveAbsorbedEcho releases the natural key the folded-in row was holding
// and takes that row off the timeline, audit-only.
//
// It RELEASES rather than deletes, and the difference is the whole point.
// uq_activity_source is a PARTIAL index — it keys only rows whose source
// columns are both non-NULL — so nulling them frees the identity for the
// survivor while the row itself stays. Deleting it instead would take with it
// everything that hangs off an activity and can be reached no other way: the
// attachment rows whose storage_key is the only handle on their object-store
// bytes, the field_provenance and embedding rows that carry no foreign key at
// all. Art. 17 erasure and the retention evaluator both walk to those THROUGH
// the activity, so a hard delete leaves bytes with no key left to erase them
// by. It is also the one thing this path may not decide on its own: a sent
// email is commercial correspondence under a statutory retention floor that
// erasure itself refuses to cross, and a de-duplication establishes no ground
// to cross it.
//
// Dedupe is what makes the timeline honest, so the row is archived rather than
// left visible: a human sees the message once, on the send that owns it. The
// key it gives up is not needed for de-duplication either — the survivor now
// holds (source_system, source_id), so a later capture of the same message
// collapses onto the survivor, and raw_capture is keyed on the natural key
// rather than on an activity id, so the provider's evidence already resolves
// there.
//
// 'merge' is the verb because that is what happened — two rows for one message
// collapse into one — and the images name what the row gave up and the row it
// went into, the same shape a person merge audits. There is no EVENT: the
// catalog is closed and has no activity.merged, while activity.archived would
// report a lifecycle disposition nobody made (this is de-duplication; the
// archive is its mechanism, not its meaning). The survivor's own
// activity.updated, emitted in the same transaction, is what tells a read model
// where the message now lives. A subscriber holding the echo's id from its
// already relayed activity.captured is the accepted residue — that delivery has
// happened and no later event can un-deliver it.
func archiveAbsorbedEcho(ctx context.Context, tx pgx.Tx, survivorID, echoID ids.ActivityID, stamped string) error {
	// The identity predicate is the concurrency guard, and it guards a real
	// window: the row was chosen by a SELECT, and only this UPDATE re-asserts
	// under a write lock that it is still the row holding the key. Matching
	// nothing means something else moved the identity in between, so there is
	// no ground left to take this row off the timeline for.
	//
	// archived_at is coalesced: a row a noise disposition already hid keeps the
	// moment it was hidden, which is the fact its undo window is measured from.
	tag, err := tx.Exec(ctx, `
		UPDATE activity
		   SET source_system = NULL,
		       source_id     = NULL,
		       archived_at   = coalesce(archived_at, now()),
		       updated_at    = now()
		 WHERE id = $1 AND source_id = $2`, echoID, stamped)
	if err != nil {
		return fmt.Errorf("activities: archiving the absorbed echo: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return errors.New("activities: the captured echo gave the stamped identity up between the lookup and the fold")
	}
	if _, err := storekit.Audit(ctx, tx, "merge", "activity", echoID.UUID,
		map[string]any{sourceIDImage: stamped, "merged_into_id": nil},
		map[string]any{sourceIDImage: nil, "merged_into_id": survivorID}); err != nil {
		return fmt.Errorf("activities: auditing the absorbed echo: %w", err)
	}
	return nil
}
