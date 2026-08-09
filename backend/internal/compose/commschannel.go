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

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/modules/comms"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
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

// channelReachability is the send path's other cross-module edge: the people
// module owns the identity binding, and the reply surface must not read its rows
// directly. It carries no state because the answer is one query on the caller's
// transaction — the same transaction that reads the conversation, so the
// recipient and the conversation are one snapshot.
type channelReachability struct{}

var _ activities.ChannelReachability = channelReachability{}

// ReachableChannelIdentities forwards to people, typing the polymorphic link id
// as the person it is. The activity link is (entity_type, entity_id) and stays
// untyped on the activities side by design; the type belongs at the boundary
// where the id is finally read as a person.
func (channelReachability) ReachableChannelIdentities(ctx context.Context, tx pgx.Tx, personID ids.UUID, provider string) ([]connector.ChannelIdentity, error) {
	return people.ReachableChannelIdentities(ctx, tx, ids.From[ids.PersonKind](personID), provider)
}

// recipientDirectory is the mail twin of the edge above: an account-started
// send names its own addressees, and person_email is the people module's
// table. Stateless for the same reason — one query on the caller's own
// transaction, so the addresses resolve in the same snapshot that stages
// the message.
type recipientDirectory struct{}

var _ activities.RecipientDirectory = recipientDirectory{}

func (recipientDirectory) VisibleAddresses(ctx context.Context, tx pgx.Tx, addresses []string) (map[string]bool, error) {
	return people.VisibleAddresses(ctx, tx, addresses)
}
