// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package privacy

// The attachment half of Art. 17 erasure: the subject's uploaded BYTES, not
// only the rows that name them. It lives beside erasure.go rather than inside
// it so the cascade file stays under the file-length cap; erasureCascadeFiles
// in the PII-coverage gate lists both, so extracting this scrub here does not
// take the attachment table out of that gate's sight.

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// subjectAttachmentsWhere selects the attachments Art. 17 erasure removes for
// a person: those hung off the person and those on the person's destroyable
// subject-only activities (floor-shielded correspondence keeps its
// attachments too). $1 is the person id; $2/$3 are the statutory floor's
// interval and calendar-year-end anchor.
var subjectAttachmentsWhere = `(entity_type = 'person' AND entity_id = $1)
	   OR (entity_type = 'activity' AND entity_id IN (` + subjectOnlyDestroyable + `))`

// eraseAttachments purges the matched attachments' objects and deletes their
// rows within the caller's transaction, objects FIRST: the keys live in the
// rows, so purging before the DELETE means any failure (a store error, or no
// store configured while objects exist) rolls the transaction back with the
// keys intact — a retry re-purges idempotently, and no bytes are ever
// orphaned with their only key gone. Erasure is rare and not latency-bound,
// so the brief object-store I/O held under the transaction is an acceptable
// trade for that durability guarantee.
func (e *Eraser) eraseAttachments(ctx context.Context, tx pgx.Tx, where string, args ...any) error {
	rows, err := tx.Query(ctx, `SELECT storage_key FROM attachment WHERE `+where, args...)
	if err != nil {
		return err
	}
	var keys []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			rows.Close()
			return err
		}
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(keys) > 0 {
		if e.blob == nil {
			return fmt.Errorf("privacy: %d attachment object(s) to purge but no object store is configured", len(keys))
		}
		for _, key := range keys {
			if err := e.blob.Delete(ctx, key); err != nil {
				return fmt.Errorf("privacy: purging attachment object: %w", err)
			}
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM attachment WHERE `+where, args...); err != nil {
		return err
	}
	return nil
}
