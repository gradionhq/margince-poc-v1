// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

// Reading and changing a message that is waiting to be sent: the rep's own
// list, one message's detail, moving its moment, and withdrawing it. The
// scheduling decision itself lives beside this; firing lives in
// scheduledsendfire.go.

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// scheduledSendColumns is the read shape, spelled once so the list and the
// detail read cannot drift into scanning different rows.
const scheduledSendColumns = `
	id, status, scheduled_at, scheduled_tz, origin_kind,
	anchor_activity_id, payload, scheduled_by, activity_id,
	held_reason, version, created_at, updated_at`

// ListScheduledSends returns the caller's pending and held messages.
func (s *Store) ListScheduledSends(ctx context.Context, status string) ([]ScheduledSend, error) {
	if err := auth.Require(ctx, "activity", principal.ActionRead); err != nil {
		return nil, err
	}
	actor, err := storekit.Actor(ctx)
	if err != nil {
		return nil, err
	}
	var out []ScheduledSend
	err = s.tx(ctx, func(tx pgx.Tx) error {
		// Scheduled mail is the SENDER's own: an unsent message's body and its
		// blind-copy list are not workspace-readable the way a sent activity
		// is. The workspace bound comes from the transaction's GUC, like every
		// other tenant read here.
		rows, err := tx.Query(ctx, `
			SELECT`+scheduledSendColumns+`
			  FROM scheduled_send
			 WHERE scheduled_by = $1 AND ($2 = '' OR status = $2)
			 ORDER BY scheduled_at ASC`,
			actor.UserID, status)
		if err != nil {
			return fmt.Errorf("scheduled send: listing: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			row, err := scanScheduledSend(rows)
			if err != nil {
				return err
			}
			out = append(out, row)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("scheduled send: reading the list: %w", err)
		}
		return nil
	})
	return out, err
}

// GetScheduledSend reads one of the caller's scheduled messages.
func (s *Store) GetScheduledSend(ctx context.Context, id ids.UUID) (ScheduledSend, error) {
	if err := auth.Require(ctx, "activity", principal.ActionRead); err != nil {
		return ScheduledSend{}, err
	}
	var out ScheduledSend
	err := s.tx(ctx, func(tx pgx.Tx) (err error) {
		out, err = readScheduledSendTx(ctx, tx, id)
		return err
	})
	return out, err
}

// readScheduledSendTx is the detail read inside a caller's transaction.
func readScheduledSendTx(ctx context.Context, tx pgx.Tx, id ids.UUID) (ScheduledSend, error) {
	actor, err := storekit.Actor(ctx)
	if err != nil {
		return ScheduledSend{}, err
	}
	rows, err := tx.Query(ctx, `
		SELECT`+scheduledSendColumns+`
		  FROM scheduled_send
		 WHERE id = $1 AND scheduled_by = $2`,
		id, actor.UserID)
	if err != nil {
		return ScheduledSend{}, fmt.Errorf("scheduled send: reading: %w", err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return ScheduledSend{}, fmt.Errorf("scheduled send: reading: %w", err)
		}
		// Existence-hiding: somebody else's scheduled message is not found,
		// never forbidden.
		return ScheduledSend{}, apperrors.ErrNotFound
	}
	return scanScheduledSend(rows)
}

// RescheduleScheduledSend moves a pending message's due moment.
//
// Time only. The content is what the approval bound to, so changing it is
// cancel-and-recompose, which re-enters every gate from the top (ADR-0104 §5).
//
// The expected version is required rather than optional: two surfaces moving
// one message must not silently resolve to whichever wrote last.
func (s *Store) RescheduleScheduledSend(ctx context.Context, id ids.UUID, sched SendSchedule, expectedVersion int64, timer ScheduleTimer) (ScheduledSend, error) {
	if err := auth.Require(ctx, "activity", principal.ActionUpdate); err != nil {
		return ScheduledSend{}, err
	}
	if timer == nil {
		return ScheduledSend{}, errNoScheduleTimer
	}
	if err := validateSchedule(sched, s.now()); err != nil {
		return ScheduledSend{}, err
	}
	if !sched.At.After(s.now()) {
		return ScheduledSend{}, &InvalidScheduleError{Field: fieldScheduledAt, Reason: "is in the past"}
	}
	current, err := s.GetScheduledSend(ctx, id)
	if err != nil {
		return ScheduledSend{}, err
	}

	err = s.tx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE scheduled_send
			   SET scheduled_at = $1, scheduled_tz = $2,
			       version = version + 1, updated_at = now()
			 WHERE id = $3 AND status = 'scheduled' AND version = $4`,
			sched.At.UTC(), sched.TZ, id, expectedVersion)
		if err != nil {
			return fmt.Errorf("scheduled send: moving the due moment: %w", err)
		}
		if tag.RowsAffected() == 0 {
			// Either it fired, was cancelled, or somebody moved it first. All
			// three are the same answer to this caller: the row you saw is not
			// the row that is there.
			return apperrors.ErrVersionSkew
		}
		if _, err := storekit.Audit(ctx, tx, "reschedule", "scheduled_send", id,
			map[string]any{fieldScheduledAt: current.ScheduledAt, fieldScheduledTZ: current.ScheduledTZ},
			map[string]any{fieldScheduledAt: sched.At.UTC(), fieldScheduledTZ: sched.TZ}); err != nil {
			return err
		}
		// A FRESH timer for the new moment. The old one still wakes at the old
		// time and finds a row whose due moment has moved, which it re-snoozes
		// or ignores — the row is the schedule, the job is only an alarm.
		return timer.ScheduleTx(ctx, tx, id, sched.At.UTC())
	})
	if err != nil {
		return ScheduledSend{}, err
	}
	return s.GetScheduledSend(ctx, id)
}

// CancelScheduledSend withdraws a message before it fires.
//
// It does not touch the timer. The job wakes, reads a row that is no longer
// scheduled, and does nothing — which is also what happens if the process dies
// between this write and any attempt to cancel the job, so there is one
// behaviour rather than two.
func (s *Store) CancelScheduledSend(ctx context.Context, id ids.UUID) error {
	if err := auth.Require(ctx, "activity", principal.ActionUpdate); err != nil {
		return err
	}
	if _, err := s.GetScheduledSend(ctx, id); err != nil {
		return err
	}
	return s.tx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE scheduled_send
			   SET status = 'cancelled', version = version + 1, updated_at = now()
			 WHERE id = $1 AND status = 'scheduled'`, id)
		if err != nil {
			return fmt.Errorf("scheduled send: cancelling: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return apperrors.ErrVersionSkew
		}
		_, err = storekit.Audit(ctx, tx, "cancel", "scheduled_send", id, nil, nil)
		return err
	})
}

// scanScheduledSend reads one row, thawing the payload for the fields the
// list and detail surfaces show.
func scanScheduledSend(rows pgx.Rows) (ScheduledSend, error) {
	var (
		row        ScheduledSend
		anchor     *ids.UUID
		payloadRaw []byte
		activityID *ids.UUID
		heldReason *string
	)
	if err := rows.Scan(
		&row.ID, &row.Status, &row.ScheduledAt, &row.ScheduledTZ, &row.OriginKind,
		&anchor, &payloadRaw, &row.ScheduledBy, &activityID,
		&heldReason, &row.Version, &row.CreatedAt, &row.UpdatedAt,
	); err != nil {
		return ScheduledSend{}, fmt.Errorf("scheduled send: reading a row: %w", err)
	}
	var payload scheduledPayload
	if err := json.Unmarshal(payloadRaw, &payload); err != nil {
		return ScheduledSend{}, fmt.Errorf("scheduled send: reading the frozen message: %w", err)
	}
	if anchor != nil {
		row.Anchor = ids.ActivityID{UUID: *anchor}
	}
	if activityID != nil {
		row.ActivityID = *activityID
	}
	if heldReason != nil {
		row.HeldReason = *heldReason
	}
	row.Subject = payload.Subject
	row.Recipients = payload.Recipients
	row.Cc = payload.Cc
	row.Bcc = payload.Bcc
	row.Body = payload.Body
	return row, nil
}
