// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package capture

// The trace's whole retention story: delete what is older than the window.
//
// There is no archive and nothing downstream reads this table, which is what
// lets a diagnostic trace hold message content at all when an operator asks for
// it. The sweep is therefore not housekeeping — it is the other half of the
// promise the payload posture makes.

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// traceSweepBatch bounds ONE delete statement.
//
// The table's whole lifecycle is insert-then-delete, so an unbounded tail
// delete on a busy installation is a large write burst and a large vacuum
// debt in the same statement. Batching keeps each transaction short and lets
// the tick stop cleanly; whatever it does not reach, the next hour does.
const traceSweepBatch = 5000

// SweepOlderThan deletes rows past the window, in bounded batches, and reports
// how many it removed.
func (s *TraceStore) SweepOlderThan(ctx context.Context, window time.Duration) (int64, error) {
	var removed int64
	for {
		var batch int64
		err := s.db.Tx(ctx, func(tx pgx.Tx) error {
			tag, err := tx.Exec(ctx, `
				DELETE FROM capture_trace
				 WHERE ctid IN (
				   SELECT ctid FROM capture_trace
				    WHERE occurred_at < now() - $1::interval
				    LIMIT $2)`, window.String(), traceSweepBatch)
			if err != nil {
				return fmt.Errorf("capture: sweeping the trace: %w", err)
			}
			batch = tag.RowsAffected()
			return nil
		})
		if err != nil {
			return removed, err
		}
		removed += batch
		if batch < traceSweepBatch {
			return removed, nil
		}
	}
}
