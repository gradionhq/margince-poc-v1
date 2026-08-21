// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// ExtractionActivityReconcileWindow bounds how far back a settled reading is
// re-announced.
//
// It is wider than the projection's own live set on purpose: a settled reading
// whose closing event was lost renders forever as whatever it last was —
// running, most damagingly — and the only way the projection learns otherwise
// is a pass that says so again. It is narrower than the projection's retention,
// because re-announcing a reading the projection has already aged out would
// resurrect it.
const ExtractionActivityReconcileWindow = 24 * time.Hour

// reconcileExtractionSQL selects the readings whose current state the
// projection must be told again: everything still live, and everything settled
// recently enough that a wrong display would still be on screen.
//
// Ordered oldest-first so a bounded pass makes progress from the back of the
// backlog rather than re-announcing the same newest rows every tick.
const reconcileExtractionSQL = `
  SELECT ` + extractionReadColumns + `
    FROM attachment_extraction
   WHERE status IN ('queued','running')
      OR finished_at > $1
   ORDER BY COALESCE(finished_at, started_at, created_at)
   LIMIT $2`

// ReconcileExtractionActivity re-publishes the current state of every reading
// the AI-activity projection could still be wrong about, returning how many it
// announced.
//
// It re-PUBLISHES rather than writing the projection directly, and that is the
// whole design: ai_task_run has exactly one writer, so the guard is the only
// thing that ever decides what lands. A repair path that wrote the table itself
// would be a second writer with no guard, and it would win races against the
// bus it is supposed to be repairing.
func (s *Store) ReconcileExtractionActivity(ctx context.Context, limit int, now time.Time) (int, error) {
	if limit <= 0 {
		return 0, fmt.Errorf("activities: the extraction-activity reconcile limit must be positive, got %d", limit)
	}
	announced := 0
	err := s.tx(ctx, func(tx pgx.Tx) error {
		reads, err := selectExtractionReads(ctx, tx, now.Add(-ExtractionActivityReconcileWindow), limit)
		if err != nil {
			return err
		}
		for _, read := range reads {
			if err := logExtractionActivity(ctx, tx, read); err != nil {
				return err
			}
			announced++
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return announced, nil
}

// selectExtractionReads materializes the pass's whole batch before anything is
// announced: the announce writes to the same connection, and a partly-consumed
// pgx.Rows cannot share it.
func selectExtractionReads(ctx context.Context, tx pgx.Tx, since time.Time, limit int) ([]ExtractionRead, error) {
	rows, err := tx.Query(ctx, reconcileExtractionSQL, since, limit)
	if err != nil {
		return nil, fmt.Errorf("select extraction readings to reconcile: %w", err)
	}
	defer rows.Close()
	var out []ExtractionRead
	for rows.Next() {
		read, err := scanExtractionRead(rows)
		if err != nil {
			return nil, fmt.Errorf("scan extraction reading to reconcile: %w", err)
		}
		out = append(out, read)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read extraction readings to reconcile: %w", err)
	}
	return out, nil
}
