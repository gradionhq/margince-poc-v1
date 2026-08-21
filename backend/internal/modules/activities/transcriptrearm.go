// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

// Handing a dead reading back to a worker — a different concern from starting
// one, and split out when transcriptread.go crossed the line cap.

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// rearmIfAbandoned hands a dead reading back to a worker.
//
// A row still `running` past its lease is not a live reading: the worker that
// claimed it was killed, timed out, or exhausted its retries. Nothing else
// would ever pick it up — a finished job is not re-enqueued, and
// uq_transcript_read_inflight makes the corpse block every new reading of that
// transcript — so without this the transcript is unreadable for good.
//
// Pressing the button again is therefore the recovery path, which is also the
// thing a rep would try unprompted.
func rearmIfAbandoned(
	ctx context.Context, tx pgx.Tx, read *TranscriptRead, enqueue TranscriptReadEnqueue,
) error {
	if read.Status != TranscriptReadRunning {
		return nil
	}
	rearmed, err := scanTranscriptRead(tx.QueryRow(ctx, `
		UPDATE transcript_read
		   SET status = 'queued', started_at = NULL, status_detail = NULL
		 WHERE id = $1
		   AND status = 'running'
		   AND started_at < now() - ($2 * interval '1 microsecond')
		RETURNING `+transcriptReadColumns, read.ID, TranscriptReadLease.Microseconds()))
	if errors.Is(err, pgx.ErrNoRows) {
		// Inside its lease: a real worker holds it, and joining is correct.
		return nil
	}
	if err != nil {
		return fmt.Errorf("re-arm abandoned transcript read: %w", err)
	}
	*read = rearmed
	if enqueue == nil {
		return nil
	}
	return enqueue(ctx, tx, rearmed)
}
