// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package dispact

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
const provider = "dispact"

// clientFor opens this member's own connection to the provider.
//
// It reuses providerFor's two facts — the member's sealed token and the base
// URL their connection was made against — rather than re-deriving either: a
// second way to build a client is a second place the wrong credential can be
// picked up, at the one seam where "whose credential" is the entire question.
//
// ErrNotFound when the member has no connection, so a caller can tell "this
// member disconnected" from "the provider would not answer".
func clientFor(ctx context.Context, rt extension.Runtime, member extension.UserID) (*client, error) {
	var conn *connection
	if err := rt.Tx(ctx, func(ctx context.Context, tx extension.Tx) error {
		found, err := connectionOf(ctx, tx, string(member))
		conn = found
		return err
	}); err != nil {
		return nil, err
	}
	if conn == nil {
		return nil, fmt.Errorf("%w: this member has no Dispact connection", extension.ErrNotFound)
	}
	api, _, err := providerFor(ctx, rt, *conn, newClient)
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
	c, err := clientFor(ctx, rt, msg.Member)
	if err != nil {
		return extension.Receipt{}, err
	}
	// The recipient's account id IS the channel slug for this provider — the
	// ingress records the DM's slug as the party's channel identity, so the
	// send routes to the same conversation the message was read from rather
	// than resolving a second one.
	sentID, err := c.sendMessage(ctx, msg.Recipient.ChannelUserID, msg.Body)
	if err != nil {
		return extension.Receipt{}, sendRefusal(err)
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
	c, err := clientFor(ctx, rt, member)
	switch {
	case errors.Is(err, extension.ErrNotFound):
		// No connection at all is a confirmed no, not a fault: the member
		// disconnected, and there is nothing to retry into.
		return false, nil
	case err != nil:
		return false, err
	}
	if _, err := c.me(ctx); err != nil {
		if errors.Is(err, errUnauthorized) {
			return false, nil
		}
		return false, fmt.Errorf("dispact: could not confirm the connection: %w", err)
	}
	return true, nil
}

// sendRefusal maps a provider failure onto what the core does with it.
//
// Only a revoked credential is PERMANENT here. Everything else — a timeout, a
// 5xx, a rate limit — is transient by default, which is the conservative
// posture for a channel: parking a message that would have sent is recoverable
// by a human, and re-sending one that already arrived is not.
func sendRefusal(err error) error {
	if errors.Is(err, errUnauthorized) {
		return fmt.Errorf("%w: %s", extension.ErrForbidden, err.Error())
	}
	return err
}
