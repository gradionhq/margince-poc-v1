// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package privacy

// The send log's half of the subject's timeline. comms_outbound stores an
// outbound message's recipients, subject and body a SECOND time, behind the
// activity that records it — so every engine that destroys an activity's
// content has to destroy the delivery's copy in the same transaction, or the
// timeline row becomes a tombstone while the send log still serves the whole
// message. Kept in its own file because both destructive engines call it
// (Art. 17 in erasure.go, the retention erase action in retention.go) and it
// belongs to neither.

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// parkedByPrivacyScrub is the operator-facing reason on a delivery the scrub
// closed before it could transmit. It names the scrub and nothing else: it
// replaces free text that could quote the subject back, so a reason assembled
// from the row would reintroduce exactly what the same statement removed.
const parkedByPrivacyScrub = "content removed by a privacy scrub before this message was sent"

// redactDeliveries scrubs the outbound deliveries behind the activities the
// caller just destroyed the content of. comms_outbound is the delivery
// machinery behind an outbound activity, and it stores the recipient
// addresses, the subject line and the body a second time — so an activity
// whose text is gone while its delivery still carries all three has not been
// erased at all.
//
// The posture is the ACTIVITY's: scrub in place, never delete. The row's
// receipt and timestamps are the record of what became of the message, and
// that fact survives the loss of what the message said — exactly as the
// timeline row survives the loss of its body.
//
// The selection is deliberately the caller's activity ids and nothing else,
// even though the eraser holds the subject's addresses and could match on
// them. Keying off the activity means a delivery inherits every shield the
// activity engine already applies — the statutory correspondence floor and the
// subject-only test — so the two rows can never disagree about the same
// message; an address match would scrub the delivery copy of a Handelsbrief
// the nightly evaluator refuses to touch.
//
// The scrub is SHAPE-AWARE because comms_outbound is: a row is mail-shaped or
// channel-shaped and never half of each (comms_outbound_shape, 0155). Writing
// mail's empty address lists onto a channel row would not merely be
// meaningless — the constraint refuses it, and an Art. 17 erasure that reached a
// channel delivery would fail outright. The two arms therefore run as two
// statements over disjoint rows, and what each removes is the same fact in its
// own vocabulary: who the message named, what it said, and any live identifier
// pointing back at the subject.
//
// A channel row's channel_user_id is EMPTIED rather than nulled, exactly as
// mail's address lists are, and for a sharper reason: it is also the row's shape
// discriminator, so nulling it would re-declare the row as mail with every mail
// column missing. Emptied, the row stays a channel delivery that names nobody —
// and no recipient shape validates as empty, so a scrubbed delivery could not
// transmit even if its status were somehow reopened.
//
// recipients/cc/subject/body are emptied rather than nulled on the mail arm for
// the same reason: an empty list is the mail shape's "nobody", where NULL is now
// the schema's "this is not a mail row". Two more columns go with them:
//
//   - list_unsubscribe carries a per-recipient one-click token — a live
//     identifier for the subject, and the same link the body footer this scrub
//     is clearing already carried.
//   - reason is free text written from faultReason, which splices up to 200
//     bytes of the PROVIDER's own message onto the row; a provider refusing a
//     recipient routinely names the address it refused, so the one column that
//     explains a failed send is also the one that can quote the subject back.
//
// What deliberately STAYS is the proof that a message left: sent_at and
// provider_message_id, plus the threading columns (message identities, like
// the activity's own thread_key — not the subject's data). status stays too
// wherever it is already terminal: a message that went out did go out, and a
// scrub that rewrote that would falsify the send log.
//
// A delivery still `pending` is the one row the scrub cannot leave as it
// found it. Its River job is still live, and the dispatcher transmits
// whatever the row holds — so an untouched status means the subject who just
// exercised Art. 17 receives the tombstone this scrub wrote. Closing it as
// `parked` is what makes it terminal to the dispatcher (comms.Store.Load and
// every transition are guarded on `status = 'pending'`), and the park reason
// is a fixed sentence rather than anything read off the row, since it lands
// in the very column the scrub above is clearing.
func redactDeliveries(ctx context.Context, tx pgx.Tx, activityIDs []ids.UUID, tombstone string) error {
	if len(activityIDs) == 0 {
		return nil
	}
	if _, err := tx.Exec(ctx, `
		UPDATE comms_outbound
		   SET recipients = '[]'::jsonb, cc = '[]'::jsonb, subject = $2,
		       body = '', list_unsubscribe = NULL,
		       status = CASE WHEN status = 'pending' THEN 'parked' ELSE status END,
		       reason = CASE WHEN status = 'pending' THEN $3 ELSE NULL END
		 WHERE activity_id = ANY($1) AND channel_user_id IS NULL`,
		activityIDs, tombstone, parkedByPrivacyScrub); err != nil {
		return fmt.Errorf("redacting the deliveries of scrubbed activities: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE comms_outbound
		   SET channel_user_id = '', body = '',
		       status = CASE WHEN status = 'pending' THEN 'parked' ELSE status END,
		       reason = CASE WHEN status = 'pending' THEN $2 ELSE NULL END
		 WHERE activity_id = ANY($1) AND channel_user_id IS NOT NULL`,
		activityIDs, parkedByPrivacyScrub); err != nil {
		return fmt.Errorf("redacting the channel deliveries of scrubbed activities: %w", err)
	}
	return nil
}
