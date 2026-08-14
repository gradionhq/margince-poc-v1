// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

// Firing a scheduled message: the moment its alarm rings and it becomes a real
// send. Separate from the scheduling half because it answers a different
// question — not "what did the rep ask for" but "is that still true, and may it
// go now" — and because the whole of it runs inside one transaction whose shape
// is the point (ADR-0104 §3).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// FireOutcome says what happened when a scheduled send came due.
type FireOutcome struct {
	// Released is the activity a successful fire produced. Zero when the
	// message was held or was no longer due to fire.
	Released crmcontracts.Activity
	// Sent reports whether this call actually transmitted anything.
	Sent bool
	// Held names why a human now has to look at it; empty when nothing was
	// held.
	Held string
	// Due is non-zero when the row's moment has MOVED and the caller should
	// wake again then instead of firing now.
	Due time.Time
}

// FireScheduledSend sends one scheduled message, or holds it.
//
// The whole thing is ONE transaction, and that is the point rather than an
// optimization. The row is claimed FOR UPDATE, its state re-checked under that
// lock, the send written, and the row transitioned — so two timers waking for
// the same message produce one send, and a crash mid-flight leaves neither a
// sent message with a pending row nor a released row with no message.
//
// Every live gate runs again inside it, through the same prepareSend an
// immediate send calls. A message whose consent was withdrawn, whose recipient
// vanished or whose attachment was archived since it was scheduled is HELD
// here, never sent stale (ADR-0104 §2).
func (s *Store) FireScheduledSend(ctx context.Context, id ids.UUID, grace time.Duration, gate ConsentGate, stager DeliveryStager) (FireOutcome, error) {
	var out FireOutcome
	err := s.tx(ctx, func(tx pgx.Tx) error {
		claimed, found, err := s.claimForFire(ctx, tx, id)
		if err != nil {
			return err
		}
		switch {
		case !found:
			// Cancelled, already released, or already held. The timer is a dumb
			// alarm and the row is the schedule, so this is the ordinary way a
			// cancelled message stops: nothing to do.
			return nil
		case claimed.ScheduledAt.After(s.now()):
			// Its moment moved later. Tell the caller when to come back.
			out.Due = claimed.ScheduledAt
			return nil
		case s.now().Sub(claimed.ScheduledAt) > grace:
			// The rep chose a MOMENT, and this is no longer it. Mail timed for
			// Monday 09:00 is wrong mail at 18:00 (ADR-0104 §6).
			out.Held = HeldMissedWindow
			return s.holdInTx(ctx, tx, id, HeldMissedWindow)
		}

		origin, in, err := claimed.replay()
		if err != nil {
			return err
		}
		prepared, err := s.PrepareSend(ctx, origin, in, gate, stager)
		if err != nil {
			// A gate refused. That is an answer about the world now, not a
			// transient fault, so retrying cannot heal it: hold for a human and
			// report the refusal for the caller to log.
			if reason, ok := holdReasonFor(err); ok {
				out.Held = reason
				return s.holdInTx(ctx, tx, id, reason)
			}
			return err
		}
		sent, err := s.SendPreparedTx(ctx, tx, origin, prepared, stager)
		if err != nil {
			return err
		}
		if err := s.releaseInTx(ctx, tx, id, ids.UUID(sent.Id)); err != nil {
			return err
		}
		out.Released, out.Sent = sent, true
		return nil
	})
	if err != nil {
		return FireOutcome{}, err
	}
	return out, nil
}

// HoldScheduledSend hands a pending message to a human, with the reason.
//
// The worker calls this for the refusals it discovers OUTSIDE the fire
// transaction — a scheduler whose account is gone, a timer whose attempts ran
// out. Refusals found during the fire are held inside it, under the row's own
// lock.
func (s *Store) HoldScheduledSend(ctx context.Context, id ids.UUID, reason string) error {
	return s.tx(ctx, func(tx pgx.Tx) error {
		_, found, err := s.claimForFire(ctx, tx, id)
		if err != nil {
			return err
		}
		if !found {
			// Cancelled or already finished while this was being decided.
			// Nothing to hold, and nothing wrong.
			return nil
		}
		return s.holdInTx(ctx, tx, id, reason)
	})
}

// claimedSend is one scheduled row locked for firing.
type claimedSend struct {
	ScheduledAt time.Time
	OriginKind  string
	Anchor      *ids.UUID
	OriginLinks []byte
	Payload     []byte
	Version     int
}

// replay rebuilds the origin and the message this row froze.
func (c claimedSend) replay() (SendOrigin, SendEmailInput, error) {
	if c.Version != payloadVersionCurrent {
		return SendOrigin{}, SendEmailInput{}, fmt.Errorf(
			"scheduled send: frozen payload version %d, this build writes %d", c.Version, payloadVersionCurrent)
	}
	var payload scheduledPayload
	if err := json.Unmarshal(c.Payload, &payload); err != nil {
		return SendOrigin{}, SendEmailInput{}, fmt.Errorf("scheduled send: reading the frozen message: %w", err)
	}
	in, err := payload.thaw()
	if err != nil {
		return SendOrigin{}, SendEmailInput{}, err
	}
	if c.OriginKind == "reply" {
		if c.Anchor == nil {
			return SendOrigin{}, SendEmailInput{}, errors.New("scheduled send: a reply with no anchor")
		}
		return FromActivity(ids.ActivityID{UUID: *c.Anchor}), in, nil
	}
	var links []ActivityLinkInput
	if err := json.Unmarshal(c.OriginLinks, &links); err != nil {
		return SendOrigin{}, SendEmailInput{}, fmt.Errorf("scheduled send: reading the frozen record links: %w", err)
	}
	return FromAccount(links), in, nil
}

// claimForFire locks one scheduled row for this transaction, or reports that
// there is nothing to fire. The lock is what makes two timers safe.
func (s *Store) claimForFire(ctx context.Context, tx pgx.Tx, id ids.UUID) (claimedSend, bool, error) {
	rows, err := tx.Query(ctx, `
		SELECT scheduled_at, origin_kind, anchor_activity_id,
		       origin_links, payload, payload_version
		  FROM scheduled_send
		 WHERE id = $1 AND status = 'scheduled'
		 FOR UPDATE`, id)
	if err != nil {
		return claimedSend{}, false, fmt.Errorf("scheduled send: claiming: %w", err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return claimedSend{}, false, fmt.Errorf("scheduled send: claiming: %w", err)
		}
		// Cancelled, already released, or already held. Not an error: the alarm
		// rang for a message that no longer wants sending.
		return claimedSend{}, false, nil
	}
	var c claimedSend
	if err := rows.Scan(&c.ScheduledAt, &c.OriginKind, &c.Anchor,
		&c.OriginLinks, &c.Payload, &c.Version); err != nil {
		return claimedSend{}, false, fmt.Errorf("scheduled send: claiming: %w", err)
	}
	return c, true, nil
}

// releaseInTx records that this message reached the delivery machinery.
//
// 'released', not 'sent': the provider has not been called yet, and the
// dispatcher can still park or fail this delivery.
func (s *Store) releaseInTx(ctx context.Context, tx pgx.Tx, id ids.UUID, activityID ids.UUID) error {
	var deliveryID ids.UUID
	if err := tx.QueryRow(ctx, `
		SELECT id FROM comms_outbound WHERE activity_id = $1`, activityID).Scan(&deliveryID); err != nil {
		return fmt.Errorf("scheduled send: finding the delivery it staged: %w", err)
	}
	// Guarded on 'scheduled' as a CAS even though claimForFire holds this
	// row's FOR UPDATE lock: the lock is what makes the read-then-write safe,
	// and this is what makes a broken caller — one that reached here without
	// claiming — fail loudly instead of overwriting a cancelled message.
	tag, err := tx.Exec(ctx, `
		UPDATE scheduled_send
		   SET status = 'released', activity_id = $1, delivery_id = $2,
		       version = version + 1, updated_at = now()
		 WHERE id = $3 AND status = 'scheduled'`, activityID, deliveryID, id)
	if err != nil {
		return fmt.Errorf("scheduled send: recording the release: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("scheduled send: %s was no longer pending when its send committed", id)
	}
	_, err = storekit.Audit(ctx, tx, "release", "scheduled_send", id, nil, map[string]any{
		"activity_id": activityID,
		"delivery_id": deliveryID,
	})
	return err
}

// holdInTx parks a scheduled message for a human, with the reason they need to
// decide what to do about it.
func (s *Store) holdInTx(ctx context.Context, tx pgx.Tx, id ids.UUID, reason string) error {
	// Same CAS as the release above, for the same reason.
	tag, err := tx.Exec(ctx, `
		UPDATE scheduled_send
		   SET status = 'held', held_reason = $1,
		       version = version + 1, updated_at = now()
		 WHERE id = $2 AND status = 'scheduled'`, reason, id)
	if err != nil {
		return fmt.Errorf("scheduled send: holding: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("scheduled send: %s was no longer pending when it was held", id)
	}
	_, err = storekit.Audit(ctx, tx, "hold", "scheduled_send", id, nil, map[string]any{"reason": reason})
	return err
}

// holdReasonFor maps a gate's refusal to the reason a rep is shown.
//
// Only refusals about the WORLD are held: a withdrawn consent, a departed
// sender, an unreadable attachment are all answers that retrying cannot
// change. Anything else — a wiring fault, a database error — is returned so
// the job runner can retry it.
func holdReasonFor(err error) (string, bool) {
	var notSendCapable *MailboxNotSendCapableError
	switch {
	case errors.Is(err, apperrors.ErrConsentNotGranted):
		return HeldConsentWithdrawn, true
	case errors.As(err, &notSendCapable):
		return HeldSenderInactive, true
	case errors.Is(err, apperrors.ErrPermissionDenied), errors.Is(err, apperrors.ErrNotFound):
		// The scheduler lost the rights they had, or something the message
		// named is gone. Both are refusals a human has to resolve.
		return HeldSendRefused, true
	}
	return "", false
}
