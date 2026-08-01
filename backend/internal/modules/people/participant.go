// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// Naming the counterparty among an activity's participants (ACT-DDL-3 /
// ADR-0078).
//
// Capture stamps a participant row for the counterparty as an ADDRESS, because
// at that moment there is no person: the tiered creation gate has not run, and
// for a suppressed or deferred sender it never will. When a person does
// resolve, the row that already names that address is the same party — so it
// is UPDATED to carry the person id rather than joined by a second row. One
// party, one row.
//
// This runs at linkActivityToPerson, the one point every ensure path reaches
// and the one that has already settled the person against a merge, so the id
// written here is the canonical one for the same reason the link is.

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// namePersonAmongParticipants attaches a resolved person to the participant
// rows that named them only by address.
//
// The address set comes from the person's own emails, so an alias resolves as
// readily as the primary — capture recorded whichever address the message
// actually used, which need not be the one the record was created from.
//
// It is deliberately forgiving about finding nothing. A manually logged
// activity has no address-only rows; a channel message has no address at all;
// a replayed capture already promoted its row on the first pass. None of those
// is an error, and none should fail an ensure that has otherwise succeeded.
func namePersonAmongParticipants(ctx context.Context, tx pgx.Tx, activityID ids.ActivityID, personID ids.PersonID) error {
	// The WHERE clause carries the idempotence: a row that already names a
	// person is left alone, so a second pass cannot repoint a participant that
	// a merge or a human has since settled differently.
	//
	// The NOT EXISTS guard is what keeps the update from colliding with the
	// ACT-DDL-3 uniqueness index. If a person row for the same (activity, role)
	// already exists — capture knew the person up front, say, from a reply
	// lookup — then promoting the address row would duplicate it. Leaving the
	// address row unpromoted in that case is correct: it is a second address
	// for a party already recorded, not a second party.
	if _, err := tx.Exec(ctx, `
		UPDATE activity_participant ap
		   SET person_id = $2
		 WHERE ap.activity_id = $1
		   AND ap.person_id IS NULL
		   AND ap.user_id IS NULL
		   AND ap.address IS NOT NULL
		   AND EXISTS (
		       SELECT 1 FROM person_email pe
		        WHERE pe.person_id = $2 AND lower(pe.email) = ap.address)
		   AND NOT EXISTS (
		       SELECT 1 FROM activity_participant other
		        WHERE other.activity_id = ap.activity_id
		          AND other.role = ap.role
		          AND other.person_id = $2)`,
		activityID, personID); err != nil {
		return fmt.Errorf("people: naming the person among an activity's participants: %w", err)
	}
	return nil
}
