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
// (comms_outbound_shape, 0149), and TWO input types is how that invariant
// reaches Go. One struct with a mode flag could name a subject and a channel
// recipient together, and the only thing left to refuse it would be the
// database — after the caller had already decided to write.

import (
	"context"
	"errors"
	"fmt"

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
