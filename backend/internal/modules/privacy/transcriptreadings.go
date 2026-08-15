// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package privacy

// The readings of a transcript, when the transcript's content is destroyed.
//
// Kept in its own file for the reason deliveries.go is: it belongs to neither
// destructive engine and both reach it — the Art. 17 cascade for the timeline
// rows it redacted, the retention sweep for one aged-out activity — and filing
// it under either one would say it was that engine's.

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// purgeTranscriptReadings drops the readings of the transcripts whose bodies
// have just been destroyed.
//
// A reading is a record OF a body: how many lines it addressed, which proposals
// it produced from them, and who asked for it. Once the body is gone it
// describes text that no longer exists, and its line count still answers a
// question about that text — how long the meeting's transcript ran to — that
// the destruction was supposed to end.
//
// Its own schema means it to go by cascade: transcript_read references the
// activity ON DELETE CASCADE (core 0245). That cascade has never once fired,
// because neither engine ever DELETES an activity — Art. 17 redacts the row in
// place and retention nulls its body, both because a timeline row is other
// people's record too. So the obligation the schema states has to be performed
// by statement, in the same transaction that empties the body.
//
// Deleted rather than emptied, unlike the activity itself: nobody else's record
// is inside a reading, so there is nothing here to preserve for a third party.
func purgeTranscriptReadings(ctx context.Context, tx pgx.Tx, activities []ids.UUID) error {
	if len(activities) == 0 {
		return nil
	}
	_, err := tx.Exec(ctx,
		`DELETE FROM transcript_read WHERE activity_id = ANY($1)`, activities)
	return err
}
