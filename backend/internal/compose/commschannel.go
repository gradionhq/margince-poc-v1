// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The channel half of the send-side resolve (telegram-oa design §8.3) — the
// second cross-module edge comms must not hold itself, wired here beside the
// mailbox half in commsjobs.go.
//
// It is a separate lookup rather than a parameter on the mailbox one because it
// asks a different question of a different table: a mailbox is one HUMAN's grant
// of one connector, while a channel is a bot an admin bound for the whole
// workspace. Only the credential lookup moves off the human, though — the seat
// gate still re-reads the person who staged the message, so a rep without a
// live, mutating seat is refused whichever transport they staged against.

import (
	"context"
	"errors"
	"fmt"

	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/modules/comms"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// channelSenders is the capture lookup the channel half of the resolver needs,
// and nothing more — narrowed for the reason mailboxSenders is: the translation
// below is a branch whose mis-reading either destroys a legitimate message or
// keeps re-sending one, so it has to be provable without a database.
// *capture.Registry is the only implementation the product ships.
type channelSenders interface {
	ChannelSenderFor(ctx context.Context, provider string) (connector.MessageSender, connector.Auth, error)
}

var _ channelSenders = (*capture.Registry)(nil)

// ResolveChannel resolves the workspace's transmitting channel binding over the
// capture registry.
//
// The translation is the mailbox one's mirror, and deliberately just as narrow.
// The same three capture answers are FACTS about the deployment; everything else
// is a failure to get an answer. A workspace holding TWO live bindings lands in
// that second group on purpose — capture refuses to guess between them, and an
// operator disconnecting the surplus binding repairs every message still pending,
// so parking on it would destroy sends that nothing is wrong with.
//
//nolint:ireturn // implements comms.ConnectionResolver, whose contract returns the optional connector.MessageSender seam
func (r commsResolver) ResolveChannel(ctx context.Context, provider string) (connector.MessageSender, connector.Auth, error) {
	sender, auth, err := r.channels.ChannelSenderFor(ctx, provider)
	switch {
	case errors.Is(err, capture.ErrNoConnection):
		return nil, nil, fmt.Errorf("%w: %w", comms.ErrNoMailbox, err)
	case errors.Is(err, capture.ErrConnectorCannotSend):
		return nil, nil, fmt.Errorf("%w: %w", comms.ErrCannotSend, err)
	case errors.Is(err, capture.ErrConnectorNotConfigured):
		return nil, nil, fmt.Errorf("%w: %w", comms.ErrProviderNotConfigured, err)
	case err != nil:
		return nil, nil, err
	}
	return sender, auth, nil
}
