// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package privacy

// The messages nobody has sent yet. A scheduled send (ADR-0104) holds a frozen
// payload — the addresses, the subject and the body — before any activity
// exists, which is exactly why the rest of this package cannot reach it: every
// other outbound scrub keys off the activity the message became, and a message
// that has not fired has none.
//
// Left alone, a subject who exercises Art. 17 the night before a scheduled send
// gets the message anyway, at nine the next morning, from a system that has
// just certified their data destroyed. Cancelled and held rows are worse: they
// have no timer at all, so their copy of the address and the body would sit
// there indefinitely with nothing that would ever look at them again.
//
// Kept in its own file for the reason deliveries.go is: both destructive
// engines call it, and it belongs to neither.

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// redactScheduledSends empties every scheduled message addressed to one of the
// subject's addresses, and stops the ones that have not fired.
//
// It selects on ADDRESSES rather than on activities, which is the opposite of
// redactDeliveries and forced by the shape of the thing: a scheduled send has
// no activity to inherit a decision from. That means it also inherits none of
// the activity engine's shields — but there is nothing for them to protect
// here. The statutory correspondence floor exists to keep SENT business
// letters; a message nobody has sent is not correspondence yet, and a
// Handelsbrief that was never sent is not one.
//
// Pending rows are CANCELLED rather than merely emptied. A scheduled row whose
// payload is blank still has a live timer, and the fire path would replay an
// empty message to an empty address list — a send that fails confusingly at
// best, and at worst one the gates cannot refuse because there is no longer
// anyone named to refuse on behalf of. Cancelling is what makes the timer a
// no-op, and it is terminal to every other transition.
//
// Released rows are emptied but keep their status: the message was sent, the
// activity and the delivery record that fact, and both are scrubbed by the
// engines that own them. Rewriting the status here would falsify what happened.
//
// No reason is written anywhere. A cancelled row carries none by design (the
// state-shape CHECK reserves held_reason for holds), and the delivery scrub's
// park reason has no analogue here: nothing was handed to a provider, so there
// is no operator waiting to be told why a transmission stopped.
func redactScheduledSends(ctx context.Context, tx pgx.Tx, emails []string) error {
	if len(emails) == 0 {
		return nil
	}
	// jsonb ?| asks whether any of these addresses appears in the array. The
	// payload keeps To, Cc and Bcc in one merged recipients list, so one test
	// covers all three — a blind copy is not a hiding place.
	if _, err := tx.Exec(ctx, `
		UPDATE scheduled_send
		   SET payload = jsonb_build_object(
		         'recipients', '[]'::jsonb,
		         'subject', ''::text,
		         'body', ''::text,
		         'consent_purpose', payload->>'consent_purpose'),
		       status = CASE WHEN status = 'scheduled' THEN 'cancelled' ELSE status END,
		       held_reason = NULL,
		       version = version + 1,
		       updated_at = now()
		 WHERE payload->'recipients' ?| $1
		    OR payload->'cc' ?| $1
		    OR payload->'bcc' ?| $1`, emails); err != nil {
		return fmt.Errorf("redacting the scheduled messages addressed to the subject: %w", err)
	}
	return nil
}
