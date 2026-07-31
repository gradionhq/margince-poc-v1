// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package comms

// The channel-shaped half of the comms_outbound seam (telegram-oa design §8.3).
// It writes the same table, into the same status machine, for the same
// dispatcher and the same retry ladder — only the columns differ, because a
// messaging channel has no RFC822 identity, no subject and no address lists.
//
// It lives beside StageTx rather than inside it: comms_outbound admits a
// mail-shaped row or a channel-shaped one and never half of each
// (comms_outbound_shape, 0155), and TWO input types is how that invariant
// reaches Go. One struct with a mode flag could name a subject and a channel
// recipient together, and the only thing left to refuse it would be the
// database — after the caller had already decided to write.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// StageChannelInput is one CHANNEL message staged for transmission — the
// channel-shaped twin of StageInput.
//
// There is deliberately no UserID field, for StageInput's reason: the human
// whose act this is comes from the authenticated principal (stagingUser), never
// from a caller-supplied value.
type StageChannelInput struct {
	ActivityID ids.ActivityID
	Provider   string
	// Recipient is the channel identity to deliver to. Only its Provider and
	// ChannelUserID are persisted — the username is display-only and mutable, so
	// a copy stored here would be stale by the time the message transmits, and
	// nothing routes on it.
	Recipient      connector.ChannelIdentity
	Body           string
	ConsentPurpose string
	// ReplyTo anchors this message on one the provider already delivered; empty
	// starts an unanchored message. It shares the row's in_reply_to column with
	// mail's RFC822 anchor because both name the message being replied to, each
	// in the vocabulary of its own transport.
	ReplyTo string
}

// ErrNoChannelRecipient marks a channel delivery staged with nobody to reach.
// It is refused here, where the caller is still inside the transaction that
// would have written the row, for ErrNoAddressee's reason: staged, it could only
// be refused later by a consent gate asked about nobody, and the operator would
// read "no consent" where the truth is "no recipient".
var ErrNoChannelRecipient = errors.New("comms: a channel delivery needs a recipient account id")

// ErrNoChannelBody marks a channel delivery staged with nothing to transmit.
// A messaging provider refuses a text-less message, so a staged one can only
// spend the whole retry ladder discovering that and then park under a reason
// about the transport rather than about the message. Whitespace counts as
// nothing: it is what an accidental send leaves in the composer.
var ErrNoChannelBody = errors.New("comms: a channel delivery needs a message body")

// StageChannelTx records one channel delivery inside the caller's transaction,
// so the delivery and the activity it reports on commit together.
//
// The mail columns are named EXPLICITLY as NULL rather than left out. cc and
// references_chain still carry the mail shape's DEFAULT of an empty JSON array
// (0136), so omitting them would write a mail default onto a channel row and the
// shape constraint would refuse it; naming all five also makes the row's shape
// readable at the one place it is written.
func (s *Store) StageChannelTx(ctx context.Context, tx pgx.Tx, in StageChannelInput) (ids.UUID, error) {
	userID, err := stagingUser(ctx)
	if err != nil {
		return ids.UUID{}, err
	}
	if in.Recipient.ChannelUserID == "" {
		return ids.UUID{}, ErrNoChannelRecipient
	}
	if strings.TrimSpace(in.Body) == "" {
		return ids.UUID{}, ErrNoChannelBody
	}
	id := ids.NewV7()
	if _, err := tx.Exec(ctx, `
		INSERT INTO comms_outbound
		  (id, workspace_id, activity_id, user_id, provider, channel_user_id,
		   body, consent_purpose, in_reply_to,
		   message_id, recipients, cc, subject, references_chain,
		   status, created_at)
		VALUES ($1, current_setting('app.workspace_id')::uuid, $2, $3, $4, $5,
		        $6, $7, NULLIF($8,''),
		        NULL, NULL, NULL, NULL, NULL,
		        'pending', $9)`,
		id, in.ActivityID, userID, in.Provider, in.Recipient.ChannelUserID,
		in.Body, in.ConsentPurpose, in.ReplyTo, s.now().UTC()); err != nil {
		return ids.UUID{}, fmt.Errorf("comms: staging channel delivery: %w", err)
	}
	return id, nil
}

// ParkTransmitted closes a delivery whose message the provider ALREADY accepted
// and whose receipt could not be written, keeping the provider's message id on
// the row.
//
// It lives HERE, with the at-most-once marker, because it exists for the same
// shape and the same reason: a transport whose retries cannot discover a prior
// send has no way to record that receipt later, so the park is the last chance
// to write down that this message went. Mail never reaches it — its next attempt
// finds the message at the provider and records the receipt then.
//
// The id is the whole reason this is not Park. Once the receipt write failed,
// nothing else in this installation holds the identity the provider filed the
// message under, and nothing can go and ask for it. Parked without it, the send
// log would carry a message the recipient is holding with no way to point at it.
//
// It runs DETACHED from the caller's context for commitReceipt's reason, one
// step further along: the message is out, this is the second attempt to write
// that fact down, and a job deadline expiring must not be what turns it into no
// record at all.
func (s *Store) ParkTransmitted(ctx context.Context, id ids.UUID, reason, providerMessageID string) error {
	ctx, cancel := detachedWrite(ctx)
	defer cancel()
	return s.update(ctx, `
		UPDATE comms_outbound
		   SET status = 'parked', reason = $2, provider_message_id = $3
		 WHERE id = $1 AND status = 'pending'`, id, reason, providerMessageID)
}

// MarkInFlight records — DURABLY, before anything reaches the provider — that
// this delivery is about to be transmitted through a seam whose retries cannot
// detect a prior send (design §8.4).
//
// The ordering is the entire guarantee. Marked afterwards, a worker that died
// mid-send would leave a row that looks untried, the job would be redelivered,
// and Telegram — which has no idempotency key and no prior-send lookup — would
// deliver a second copy with nothing able to notice. Marked first, the next
// attempt can see that the outcome was never learned and stop.
//
// It is deliberately NOT a status and NOT a claim. An in-flight status would
// make River's redelivery a silent skip for mail as well, disabling the
// connector's retransmission check in exactly the crash it exists for; this
// column is read only by the seams that need it, which is why the schema
// constrains it to the channel shape.
//
// Guarded on status = 'pending' like every other transition here: a stale
// attempt that lost a race to one which already closed the row reports
// ErrTerminal rather than marking a finished delivery live again.
func (s *Store) MarkInFlight(ctx context.Context, id ids.UUID) error {
	return s.update(ctx, `
		UPDATE comms_outbound SET inflight_at = $2
		 WHERE id = $1 AND status = 'pending'`, id, s.now().UTC())
}

// ClearInFlight retracts the marker, and its caller is the one place that may:
// a DEFINITE answer from the provider, which proves the message did not go.
// Anything less — a timeout, a reset, a response that could not be read — leaves
// the marker standing, because that is the whole fact it exists to carry.
//
// It is shape-BLIND on purpose. inflight_at is NULL on every mail row, so
// clearing one is a no-op there, and the send path gets one rule ("a definite
// refusal retracts the marker") instead of a second branch on provider class.
func (s *Store) ClearInFlight(ctx context.Context, id ids.UUID) error {
	return s.update(ctx, `
		UPDATE comms_outbound SET inflight_at = NULL
		 WHERE id = $1 AND status = 'pending'`, id)
}
