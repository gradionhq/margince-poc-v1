// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

// The capture-classify label columns (ADR-0063) belong to the activity
// row, so their reads and writes live here — the classify engine consumes
// these two methods and never touches activity SQL itself. A label routes
// attention only: setting one changes NOTHING else on the row, mints no
// audit entry and no event (§3.2 — a noise label deleting or archiving
// anything is a hard-floor violation).

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// UnlabeledEmail is one classify-backlog row as the prompt consumes it.
type UnlabeledEmail struct {
	ID      ids.UUID
	Subject string
	Body    string // pre-truncated to bodyLimit
}

// UnlabeledCaptureEmails reads the oldest connector-captured emails not
// yet labeled — the partial-index backlog (idx_activity_unlabeled).
func (s *Store) UnlabeledCaptureEmails(ctx context.Context, limit, bodyLimit int) ([]UnlabeledEmail, error) {
	var out []UnlabeledEmail
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id, coalesce(subject, ''), coalesce(left(body, $1), '')
			FROM activity
			WHERE capture_label IS NULL AND captured_by LIKE 'connector:%' AND kind = 'email'
			  AND archived_at IS NULL
			ORDER BY occurred_at
			LIMIT $2`, bodyLimit, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var m UnlabeledEmail
			if err := rows.Scan(&m.ID, &m.Subject, &m.Body); err != nil {
				return err
			}
			out = append(out, m)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("activities: reading the classify backlog: %w", err)
	}
	return out, nil
}

// SetCaptureLabel writes one verdict, reporting whether it applied. The
// capture_label IS NULL predicate is the CAS: a concurrent pass that
// labeled the row first wins and this write reports applied=false — the
// earlier verdict stands, never an overwrite.
func (s *Store) SetCaptureLabel(ctx context.Context, id ids.UUID, label string) (applied bool, err error) {
	err = database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE activity SET capture_label = $2, capture_labeled_at = now()
			WHERE id = $1 AND capture_label IS NULL`, id, label)
		if err != nil {
			return fmt.Errorf("activities: setting capture label: %w", err)
		}
		applied = tag.RowsAffected() > 0
		return nil
	})
	return applied, err
}
