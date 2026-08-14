// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package notes

import (
	"context"
	"encoding/json"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

// withdrawFiling clears a note's filing when the activity it was filed to is
// archived.
//
// WHY THIS IS AN OBLIGATION RATHER THAN A DEMONSTRATION. file_note writes two
// rows: this unit's note, and a core activity on the record's timeline. The
// note keeps the activity's id, which is what lets the screen say a note
// reached a record. Archiving that activity takes the timeline entry away — and
// leaves the note claiming a filing whose entry nobody can see. Nothing in the
// core knows this unit's column exists, so if the unit does not listen, the
// claim stays wrong forever.
//
// NOBODY IS BEHIND THIS CALL. rt.Caller() is the zero Caller and the core port
// is shut, which is exactly right for the work: the row this touches is the
// unit's own, and the archive that triggered it was already authorized by
// whoever made it.
//
// IT IS SAFE TO RUN TWICE, which the bus requires: the UPDATE matches on the
// filing that is being withdrawn, so a redelivery matches nothing, writes no
// ledger row and publishes nothing. An audit trail of writes that did not
// happen would be worse than no trail at all.
func withdrawFiling(ctx context.Context, rt extension.Runtime, d extension.Delivery) error {
	if d.Entity.Type != "activity" || !extension.IsCanonicalUUID(d.Entity.ID) {
		// A delivery this handler cannot act on is ACKED, not failed. Failing
		// it would put it back in the pending set for the reclaim pass to hand
		// over again, forever, at the cost of one wasted handler run per pass —
		// and it would say "this failed" about an event that simply is not this
		// handler's business.
		return nil
	}
	return rt.Tx(ctx, func(ctx context.Context, tx extension.Tx) error {
		withdrawn, err := clearFilings(ctx, tx, d.Entity.ID)
		if err != nil {
			return err
		}
		for _, n := range withdrawn {
			if err := recordWithdrawal(ctx, tx, n, d); err != nil {
				return err
			}
		}
		return nil
	})
}

// clearFilings takes the filing off every note that named this activity, and
// answers the rows as they now stand.
//
// EVERY note, not the first: nothing in the schema stops two notes naming one
// activity, and a loop that handled one would leave the others claiming a
// filing — the exact state this handler exists to remove.
func clearFilings(ctx context.Context, tx extension.Tx, activityID string) ([]note, error) {
	rows, err := tx.Query(ctx,
		`UPDATE `+noteTable+` SET filed_activity_id = NULL
		 WHERE filed_activity_id = $1::uuid
		 RETURNING `+noteColumns, activityID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var withdrawn []note
	for rows.Next() {
		n, err := scanNote(rows.Scan)
		if err != nil {
			return nil, err
		}
		withdrawn = append(withdrawn, n)
	}
	return withdrawn, rows.Err()
}

// recordWithdrawal writes the ledger row for one withdrawal and publishes it.
//
// The BEFORE image is reconstructed rather than read back, and it is exact: the
// UPDATE changed one column, and the value it held is the id of the activity
// whose archive is being handled. A second read to fetch what this statement
// just overwrote would be a round trip for a value already in hand — and it
// would have to run before the write, where it could disagree with the row the
// UPDATE actually matched.
func recordWithdrawal(ctx context.Context, tx extension.Tx, after note, d extension.Delivery) error {
	activityID := d.Entity.ID
	before := after
	before.FiledActivityID = &activityID

	// The CAUSE of the write, which belongs in evidence rather than in the
	// images: the note's own fields did not record why they changed, and a
	// reader asking "who un-filed this" is asking about the event, not the row.
	detail, err := json.Marshal(struct {
		Cause      string `json:"cause"`
		ActivityID string `json:"activity_id"`
		EventID    string `json:"event_id"`
	}{Cause: d.Type, ActivityID: activityID, EventID: d.EventID})
	if err != nil {
		return err
	}
	payload, err := json.Marshal(struct {
		ActivityID string `json:"activity_id"`
	}{ActivityID: activityID})
	if err != nil {
		return err
	}
	return recordNote(ctx, tx, extension.AuditUpdate, eventFilingWithdrawn, &before, &after, detail, payload)
}
