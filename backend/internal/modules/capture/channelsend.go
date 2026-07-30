// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package capture

// The send-side resolve for a channel connection (telegram-oa design §8.3).
//
// SenderFor (registry_connections.go) answers "which mailbox does THIS HUMAN
// transmit through", keyed (user_id, provider) because capture_connection models
// one human's grant of one connector. A channel binding is not that: an admin
// connects a bot on behalf of the whole workspace, so there is no user id to
// resolve by and the lookup is keyed on the workspace alone — which RLS already
// binds.
//
// What does NOT change is the seat check. The human who staged the delivery is
// still re-read at transmit time by the dispatcher's SeatAuthority; only the
// credential lookup moves off their account. A rep without a live, mutating seat
// is still refused, whichever transport their message was staged against.

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// ErrChannelConnectionAmbiguous reports more than one live channel binding for
// one provider in one workspace. The schema permits it — the unique indexes bind
// (workspace, provider, bot) and (provider, bot), not (workspace, provider) — and
// the send path must not guess between them: replying through the wrong bot
// reaches a chat the customer never opened, which Telegram refuses, so the rep's
// message would vanish with a provider error that names nothing an operator can
// act on.
//
// It is a FAULT and not one of the three deployment facts below: the deployment
// is answerable, and an operator disconnecting the surplus binding repairs every
// delivery still pending.
var ErrChannelConnectionAmbiguous = errors.New("capture: more than one live channel connection for this provider in this workspace")

// ChannelSenderFor resolves the WORKSPACE's transmitting channel binding for one
// provider: the connector's MessageSender seam and its unsealed credential.
//
// It reports the same three deployment facts SenderFor does, so the send path
// classifies a channel exactly as it classifies a mailbox: ErrNoConnection (bind
// a bot), ErrConnectorCannotSend (this connector captures only),
// ErrConnectorNotConfigured (this process role compiled in no such connector).
// EVERY OTHER ERROR IS TRANSIENT — a vault blip or a database timeout is a
// failure to get an answer, and parking on one would permanently destroy a
// legitimate message that nothing is wrong with.
//
// Only a `connected`, un-archived row counts. A `pending` row registered a
// binding whose webhook call never succeeded (channelconn.go), so the bot is not
// reachable in either direction; treating it as live would transmit through a
// connection the operator has not been told is broken.
//
//nolint:ireturn // returns the optional connector.MessageSender seam by design, the posture SenderFor takes for connector.EmailSender
func (r *Registry) ChannelSenderFor(ctx context.Context, provider string) (connector.MessageSender, connector.Auth, error) {
	credentialRef, err := r.liveChannelCredential(ctx, provider)
	if err != nil {
		return nil, nil, err
	}
	c, err := r.connector(provider)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %w", ErrConnectorNotConfigured, err)
	}
	// Two-value form: MessageSender is optional (connector/channelmessage.go), so
	// a capture-only connector is reported rather than silently treated as absent.
	sender, sends := c.(connector.MessageSender)
	if !sends {
		return nil, nil, fmt.Errorf("capture: connector %q: %w", provider, ErrConnectorCannotSend)
	}
	auth, err := r.resolveCredential(ctx, &credentialRef, nil)
	if err != nil {
		return nil, nil, err
	}
	return sender, auth, nil
}

// liveChannelCredential reads the one live binding's vault ref, and refuses
// rather than picking when there are two. It collects the matching refs instead
// of taking the first row: a QueryRow would silently prefer whichever row the
// planner returned, which is the same silent wrong-bot send the ambiguity error
// exists to prevent.
func (r *Registry) liveChannelCredential(ctx context.Context, provider string) (string, error) {
	var refs []string
	err := database.WithWorkspaceTx(ctx, r.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT credential_ref FROM channel_connection
			 WHERE provider = $1 AND status = $2 AND archived_at IS NULL`,
			provider, channelStatusConnected)
		if err != nil {
			return err
		}
		refs, err = pgx.CollectRows(rows, pgx.RowTo[string])
		return err
	})
	if err != nil {
		return "", fmt.Errorf("capture: resolving the sending channel connection: %w", err)
	}
	switch len(refs) {
	case 0:
		return "", ErrNoConnection
	case 1:
		return refs[0], nil
	default:
		return "", fmt.Errorf("capture: %d live %s connections: %w", len(refs), provider, ErrChannelConnectionAmbiguous)
	}
}
