// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package telegram

// The registered connector and its outbound seam (telegram-oa design §8.2,
// §8.4): the connector.MessageSender the workspace's bot binding transmits
// through, and the ONE place Telegram's own sentinels become the shared send
// vocabulary a dispatcher classifies on.
//
// That mapping is the safety property, not a formality. Telegram's sendMessage
// has no idempotency key and no prior-send lookup, so a retry can never discover
// that an earlier attempt already delivered — which means an outcome Telegram
// never reported must be declared UNKNOWN
// (connector.ErrSendOutcomeUnknown) and never retried. Every other class here is
// a definite answer FROM Telegram: nothing was transmitted, so the caller's
// ladder may safely try again.

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// ProviderName is the registry's stable id for this provider, spelled ONCE in
// the tree: capture.ProviderTelegram is defined as this constant rather than as
// a second copy of the literal, because a provider name that drifted would read
// a live channel as capture-only and park every message staged against it.
const ProviderName = "telegram"

// Connector is the registered Telegram connector: a push capture source whose
// updates arrive at the webhook rather than through Sync, plus the send seam the
// workspace's bot binding transmits through.
type Connector struct{ api API }

// New builds the connector over the Bot API client. It takes no deployment
// config — a bot binding's credential is per-connection and vault-sealed — so
// every capture-capable role can carry it, exactly as it carries standing IMAP.
func New(api API) *Connector { return &Connector{api: api} }

var (
	_ connector.Connector     = (*Connector)(nil)
	_ connector.MessageSender = (*Connector)(nil)
)

// Descriptor is the registry's static metadata. The tier and scope describe the
// CAPTURE surface this descriptor governs, which reads and never writes; the
// outbound send is a governed operation on the activity surface and carries its
// own confirm-first tier there, exactly as mail's does.
func (c *Connector) Descriptor() connector.Descriptor {
	return connector.Descriptor{
		Name:     ProviderName,
		Version:  "1",
		Scopes:   []principal.Scope{principal.ScopeRead},
		RiskTier: mcp.TierAutoExecute,
		Produces: []datasource.EntityType{datasource.EntityActivity},
	}
}

// Authenticate is not this connector's path. A bot binding is established by an
// admin on the workspace-level channel-connection surface, which validates the
// token with getMe and seals it itself; there is no per-user handshake to
// perform, and a caller that reached here has resolved the wrong connect flow.
func (c *Connector) Authenticate(context.Context, connector.AuthRequest) (connector.Auth, error) {
	return nil, fmt.Errorf(
		"telegram: a bot binding is established on the channel-connection surface, not through the per-user connector handshake: %w",
		ErrRequestRejected)
}

// Sync has nothing to pull, and returning the cursor unchanged is the whole
// correct behaviour rather than a stub. Telegram is a PUSH source: updates
// arrive at the webhook and are normalized from the raw capture that persisted
// them, and the Bot API exposes no history endpoint — so there is no watermark
// to advance and nothing a poll could read.
func (c *Connector) Sync(_ context.Context, _ connector.Auth, cursor connector.Cursor, _ connector.Sink) (connector.Cursor, error) {
	return cursor, nil
}

// Normalize maps one verbatim Telegram update, delegating to the package
// function the ingest worker also calls so the mapping has one spelling.
func (c *Connector) Normalize(ctx context.Context, raw connector.RawRecord) ([]connector.NormalizedRecord, error) {
	return Normalize(ctx, raw)
}

// HealthCheck asks Telegram whether the token still names a live bot. getMe is
// the Bot API's own liveness call, and for a bot binding the sealed credential
// IS the token.
func (c *Connector) HealthCheck(ctx context.Context, auth connector.Auth) error {
	if _, err := c.api.GetMe(ctx, string(auth)); err != nil {
		return err
	}
	return nil
}

// SendMessage transmits one message as the workspace's bot and reports
// Telegram's own message id for it — the id a later reply threads under.
//
// There is deliberately NO prior-send lookup, unlike the mail seam's: Telegram
// offers no idempotency key and no way to search for a message this system
// staged, so msg.Attempt is not actionable here. The at-most-once guarantee
// therefore lives entirely in the caller's in-flight marker (design §8.4), and
// this seam's contribution to it is honest classification — an outcome Telegram
// did not report must not come back looking transient.
func (c *Connector) SendMessage(ctx context.Context, auth connector.Auth, msg connector.ChannelMessage) (connector.SendReceipt, error) {
	if err := msg.Validate(); err != nil {
		return connector.SendReceipt{}, err
	}
	chatID, err := chatIDOf(msg.Recipient)
	if err != nil {
		return connector.SendReceipt{}, err
	}
	replyTo, err := replyAnchorOf(msg.ReplyTo)
	if err != nil {
		return connector.SendReceipt{}, err
	}
	id, err := c.api.SendMessage(ctx, string(auth), OutboundChannelMessage{
		ChatID:           chatID,
		Text:             msg.Body,
		ReplyToMessageID: replyTo,
	})
	if err != nil {
		return connector.SendReceipt{}, sendOutcome(err)
	}
	// RFC822MessageID stays empty, and the receipt's own contract reads that
	// emptiness as "no re-key is owed" — which is exactly the fact here: a
	// channel message has no mail identity for a timeline row to be re-keyed
	// onto.
	return connector.SendReceipt{ProviderMessageID: strconv.FormatInt(id, 10)}, nil
}

// chatIDOf reads the recipient's chat from their channel identity. A private
// chat's id IS the Telegram account id, which is why a resolved channel identity
// addresses a chat with no second lookup.
//
// It REFUSES anything that is not a positive id rather than routing to a
// guessed chat. Non-numeric is the obvious case; the sign is the dangerous one,
// because Telegram numbers a supergroup or channel NEGATIVE, so a negative id
// would send this reply — and the customer's words quoted in it — to a room
// whoever owns that id controls, and zero addresses no account at all. The
// value arrives from the staged delivery row, so a row that cannot name a
// private chat is a defect to surface; the id itself is left out of the message
// because this text reaches a log, and a counterparty's account id is not log
// material.
func chatIDOf(recipient connector.ChannelIdentity) (int64, error) {
	chatID, err := strconv.ParseInt(recipient.ChannelUserID, 10, 64)
	if err != nil || chatID <= 0 {
		return 0, fmt.Errorf("telegram: the recipient's channel account id is not a private chat id: %w", ErrRequestRejected)
	}
	return chatID, nil
}

// replyAnchorOf reads the provider message id a reply threads under. Empty is
// the ordinary unanchored case and yields 0, which is how the Bot API request
// omits the anchor.
//
// A malformed anchor is refused rather than dropped. Dropping it would send the
// rep's reply detached from the conversation it answers, which reads to the
// customer as a message out of nowhere and to the rep as a success. A provider
// message id is POSITIVE, so a stated anchor of "0" or below is refused for the
// same reason: it parses cleanly and then means "no anchor" on the wire, which
// is the silent drop written out.
func replyAnchorOf(replyTo string) (int64, error) {
	if replyTo == "" {
		return 0, nil
	}
	anchor, err := strconv.ParseInt(replyTo, 10, 64)
	if err != nil || anchor <= 0 {
		return 0, fmt.Errorf("telegram: the reply anchor is not a provider message id: %w", ErrRequestRejected)
	}
	return anchor, nil
}

// sendOutcome maps this package's sentinels onto the shared send vocabulary. It
// is the ONE translation, so what a Telegram failure MEANS for a delivery cannot
// depend on which line of the send path noticed it.
func sendOutcome(err error) error {
	switch {
	case errors.Is(err, connector.ErrRateLimited):
		// Already in the shared vocabulary, interval included (classify), so it
		// passes through untouched: wrapping it in another class here would
		// leave the caller honouring a backoff of its own invention instead of
		// the interval Telegram stated.
		return err
	case errors.Is(err, ErrTokenRejected):
		// The bot token is refused. No retry repairs it, and the caller parks
		// naming the credential that has to be replaced.
		return fmt.Errorf("%w: %w", connector.ErrAuthRejected, err)
	case errors.Is(err, ErrRecipientUnreachable):
		// The customer blocked the bot, or their account is gone. Definite —
		// nothing was transmitted — but permanent, so the caller parks naming the
		// RECIPIENT. This branch precedes the default deliberately: a 403 also
		// reads as a refusal on Telegram's own terms, and left to fall through it
		// would burn the retry ladder against a chat that will never accept the
		// message and then park under a reason that names no cause.
		return fmt.Errorf("%w: %w", connector.ErrRecipientUnreachable, err)
	case errors.Is(err, ErrUnreachable):
		// Telegram never reported what became of the request. It may have been
		// delivered, and nothing here or later can find out, so this is the one
		// class the caller must never retry.
		return fmt.Errorf("%w: %w", connector.ErrSendOutcomeUnknown, err)
	default:
		// A DEFINITE refusal on Telegram's own terms — a body it will not
		// accept, a chat id it does not recognize. A blocked chat never reaches
		// here; the branch above owns it. Nothing was transmitted, so the caller's ladder may retry it.
		// It stays in this package's vocabulary because none of the shared
		// classes describes it: it is neither an outage nor a credential fault,
		// and claiming either would send an operator after the wrong problem.
		return err
	}
}
