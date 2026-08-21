// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package relayprobe

// The transport half of this unit: what it takes to send a message back into a
// conversation the poll captured (ADR-0107/A158, DESIGN-SP5 §9).
//
// Whose credential transmits is the whole shape of this file. A message leaves
// under the MEMBER's own token — the person who staged it — never under an
// installation credential, because this provider has none: the unit holds one
// sealed secret per member and nothing else. That is also why Live exists: the
// core has to be able to ask "can this member still send" without spending the
// credential to find out.

import (
	"context"
	"errors"
	"fmt"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

// provider is this unit's key in channel_provider. It is NOT an activity kind:
// a message it carries lands as kind `message` with this name on its own
// column, which is the separation the whole decision rests on.
const provider = "relay"

// clientFor opens this member's own connection to the provider.
//
// It reuses providerFor's two facts — the member's sealed token and the base
// URL their connection was made against — rather than re-deriving either: a
// second way to build a client is a second place the wrong credential can be
// picked up, at the one seam where "whose credential" is the entire question.
//
// dial is a parameter for the reason the poll's is: the production constructor
// refuses to dial anything that is not a globally routable address, which is a
// guard with its own tests and one no test server can satisfy. Injecting the
// factory is what lets the send path be exercised at all — the alternative is a
// transmission seam proven only in production.
//
// ErrNotFound when the member has no connection, so a caller can tell "this
// member disconnected" from "the provider would not answer".
func clientFor(ctx context.Context, rt extension.Runtime, member extension.UserID, dial clientFactory) (*client, error) {
	var conn *connection
	if err := rt.Tx(ctx, func(ctx context.Context, tx extension.Tx) error {
		found, err := connectionOf(ctx, tx, string(member))
		conn = found
		return err
	}); err != nil {
		return nil, err
	}
	if conn == nil {
		return nil, fmt.Errorf("%w: this member has no Relay connection", extension.ErrNotFound)
	}
	api, _, err := providerFor(ctx, rt, *conn, dial)
	if err != nil {
		return nil, err
	}
	return api, nil
}

// send transmits one message on this member's connection.
//
// It resolves the member's own connection rather than any connection: the
// OutboundMessage names whose credential must carry it, and a unit that sent on
// somebody else's would be transmitting as a colleague the recipient never
// wrote to.
func send(ctx context.Context, rt extension.Runtime, msg extension.OutboundMessage) (extension.Receipt, error) {
	return sendVia(ctx, rt, msg, newClient)
}

func sendVia(ctx context.Context, rt extension.Runtime, msg extension.OutboundMessage, dial clientFactory) (extension.Receipt, error) {
	c, err := clientFor(ctx, rt, msg.Member, dial)
	if err != nil {
		// Through the same classifier the transmission's own failure goes
		// through, because a revoked credential is discovered HERE as often as
		// there: opening the client spends the token on `me`, so a token
		// revoked between staging and sending is refused before a message is
		// ever posted. Returning it raw would report the one permanent failure
		// this unit has as a transient one, and the core would retry a
		// credential that will never be accepted again.
		return extension.Receipt{}, sendRefusal(err)
	}
	// The recipient is an ACCOUNT, and the conversation is resolved from it
	// against the provider. That indirection is the point: the core binds a
	// party by account id, one row per human, and keying that row on a
	// conversation instead would make the same colleague two people and leave
	// an erasure covering only one of their chats.
	channel, err := c.dmChannelWith(ctx, msg.Recipient.ChannelUserID)
	if err != nil {
		// Resolution transmits nothing, so it is a pre-flight failure whatever
		// went wrong — never the unknown-outcome class.
		return extension.Receipt{}, sendRefusal(err)
	}
	sentID, err := c.sendMessage(ctx, channel, msg.Body)
	if err != nil {
		return extension.Receipt{}, transmissionRefusal(err)
	}
	return extension.Receipt{ProviderMessageID: sentID}, nil
}

// live answers whether this member's connection can still send, without
// spending the credential on a message.
//
// `me` is the dry run: it is the cheapest call this provider has that proves
// the token is still accepted, and it changes nothing. A rejected token answers
// FALSE (a confirmed "not usable"), and any other failure returns an error —
// the core parks on the first and retries on the second, so collapsing them
// would either strand a deliverable message or re-send a refused one.
func live(ctx context.Context, rt extension.Runtime, member extension.UserID) (bool, error) {
	return liveVia(ctx, rt, member, newClient)
}

func liveVia(ctx context.Context, rt extension.Runtime, member extension.UserID, dial clientFactory) (bool, error) {
	c, err := clientFor(ctx, rt, member, dial)
	switch {
	case errors.Is(err, extension.ErrNotFound):
		// No connection at all is a confirmed no, not a fault: the member
		// disconnected, and there is nothing to retry into.
		return false, nil
	case errors.Is(err, errUnauthorized):
		// A credential the provider no longer accepts, discovered while opening
		// the client — which is where it is usually discovered, because
		// providerFor spends the token on `me` before this function's own dry
		// run gets to. It is the SAME confirmed no as the one below, and
		// reporting it as an error here would have the core retry a revoked
		// token until the ladder was spent instead of parking the delivery
		// where a human can reconnect.
		return false, nil
	case err != nil:
		return false, err
	}
	if _, err := c.me(ctx); err != nil {
		if errors.Is(err, errUnauthorized) {
			return false, nil
		}
		return false, fmt.Errorf("relay: could not confirm the connection: %w", err)
	}
	return true, nil
}

// sendRefusal maps a failure that happened BEFORE anything was transmitted —
// opening the client, unsealing the credential, the `me` dry run.
//
// Only a revoked credential is PERMANENT here. Everything else — a timeout, a
// 5xx, a rate limit — is transient, which is the conservative posture for a
// channel: parking a message that would have sent is recoverable by a human.
func sendRefusal(err error) error {
	if errors.Is(err, errUnauthorized) {
		return fmt.Errorf("%w: %s", extension.ErrForbidden, err.Error())
	}
	return err
}

// transmissionRefusal maps a failure of the POST ITSELF, which carries one
// class the pre-flight cannot: the message may already be at the recipient.
//
// This provider has no idempotency key and no prior-send lookup, so a request
// whose answer never came back is a question no later attempt can settle. The
// core retries every refusal it is not told is unanswerable, so reporting one
// as an ordinary transport failure delivers the rep's message twice — the one
// failure a human cannot undo. ErrSendOutcomeUnknown stops the delivery instead
// and leaves the uncertainty on the record, which is worse for this unit and
// better for the person receiving the message.
func transmissionRefusal(err error) error {
	if errors.Is(err, errUnanswered) {
		return fmt.Errorf("%w: %s", extension.ErrSendOutcomeUnknown, err.Error())
	}
	return sendRefusal(err)
}
