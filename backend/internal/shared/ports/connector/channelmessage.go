// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package connector

import (
	"context"
	"errors"
)

// MessageSender is the channel twin of EmailSender: the OPTIONAL outbound seam
// a connector implements when its provider can transmit a message into a
// messaging channel (Telegram) rather than mail. Type-asserted the same way —
// a connector implements EmailSender, MessageSender, both, or neither.
type MessageSender interface {
	SendMessage(ctx context.Context, auth Auth, msg ChannelMessage) (SendReceipt, error)
}

// ChannelMessage is one message to transmit into a messaging channel, the
// channel twin of EmailMessage. It carries no RFC822 identity — a channel
// recipient is a numeric account id, not an addr-spec — so its idempotency
// guarantee rests on IdempotencyKey rather than on ValidMessageID; see
// ChannelMessage.Validate.
type ChannelMessage struct {
	// Recipient is the channel identity to deliver to (Task 8's
	// ChannelIdentity, same package).
	Recipient ChannelIdentity
	Body      string
	// ReplyTo is the provider message id to anchor a reply on; "" starts a
	// fresh message with no anchor.
	ReplyTo string
	// IdempotencyKey is this seam's retry-safety anchor, in place of the mail
	// seam's RFC822 Message-ID: Telegram's sendMessage has no idempotency key
	// and no prior-send lookup, so the guarantee EmailMessage gets from
	// ValidMessageID has no equivalent here at the provider. Callers key retry
	// safety on this instead.
	IdempotencyKey string
	// Attempt is 0 on the first transmission and increments on every retry.
	Attempt int
}

// ErrInvalidChannelMessage marks a channel message missing what
// ChannelMessage.Validate requires: a recipient to deliver to, and the
// idempotency key retry safety is keyed on.
var ErrInvalidChannelMessage = errors.New("connector: channel message carries no usable recipient or idempotency key")

// Validate refuses a channel message no provider should be handed. Unlike
// EmailMessage.Validate, this does NOT check ValidMessageID: a channel
// recipient is a numeric account id, not an RFC822 addr-spec, and running the
// mail predicate against it would reject every legitimate channel recipient.
func (m ChannelMessage) Validate() error {
	if m.Recipient.ChannelUserID == "" || m.IdempotencyKey == "" {
		return ErrInvalidChannelMessage
	}
	return nil
}
