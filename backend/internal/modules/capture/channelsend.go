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
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// ErrChannelConnectionAmbiguous reports more than one live channel binding for
// one provider in one workspace. uq_channel_connection_ws makes that state
// unreachable through this application, and this refusal is what stops the
// resolver depending on that: it reads the rows rather than trusting a constraint
// it cannot see, because replying through the wrong bot reaches a chat the
// customer never opened, which Telegram refuses — so the rep's message would
// vanish with a provider error naming nothing an operator can act on.
//
// It is a FAULT and not one of the three deployment facts below: if it is ever
// reached, an operator disconnecting the surplus binding repairs every delivery
// still pending.
var ErrChannelConnectionAmbiguous = errors.New("capture: more than one live channel connection for this provider in this workspace")

// ErrChannelBindingReplaced reports that the binding a send resolved its
// credential from is no longer the workspace's live one — replaced by another
// bot, or withdrawn — by the time that credential was about to be spent.
//
// It is TRANSIENT, not a deployment fact: the workspace still has a bot, and
// re-resolving finds it. Parking here would destroy a message whose only problem
// is that it resolved a moment too early.
var ErrChannelBindingReplaced = errors.New("capture: the channel binding was replaced after this send resolved its credential")

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
// Only a `connected`, un-archived row counts. `error` and `reauth_required` are
// what the poller parks a broken binding under, and `disconnected` is what an
// operator withdrew: transmitting through any of them would spend a credential the
// row no longer stands behind, on a channel nobody has been told is broken.
//
//nolint:ireturn // returns the optional connector.MessageSender seam by design, the posture SenderFor takes for connector.EmailSender
func (r *Registry) ChannelSenderFor(ctx context.Context, provider string) (connector.MessageSender, connector.Auth, error) {
	binding, err := r.liveChannelBinding(ctx, provider)
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
	auth, err := r.resolveCredential(ctx, &binding.credentialRef, nil)
	if err != nil {
		return nil, nil, err
	}
	return fencedChannelSender{sender: sender, registry: r, binding: binding}, auth, nil
}

// fencedChannelSender re-reads the binding this credential came from at the last
// moment before spending it, and refuses if it moved.
//
// The credential is unsealed when the delivery resolves, and the send path then
// walks the seat, consent and pacing gates — several database round trips —
// before the provider is called. An admin replacing the bot inside that window
// has withdrawn the token in hand: the row no longer names the outgoing bot, so
// nothing polls it, and a reply transmitted through it reaches a chat this
// installation will never read an answer from; a rotated token is refused outright.
//
// This NARROWS the window; it does not close it. No transaction spans Telegram's
// HTTP call, so a replacement committing between the re-read and the provider
// call still transmits through the outgoing bot. That residual race is
// sub-millisecond and accepted by construction — the alternative would be
// holding a database transaction open across a remote call.
type fencedChannelSender struct {
	sender   connector.MessageSender
	registry *Registry
	binding  channelBinding
}

func (f fencedChannelSender) SendMessage(ctx context.Context, auth connector.Auth, msg connector.ChannelMessage) (connector.SendReceipt, error) {
	if err := f.registry.requireBindingUnchanged(ctx, f.binding); err != nil {
		return connector.SendReceipt{}, err
	}
	return f.sender.SendMessage(ctx, auth, msg)
}

// requireBindingUnchanged reports whether the row this credential was read from
// is still live and still on the version it was read at. A version bump is a
// replacement (ReplaceToken repoints the row in place), and a row that no longer
// answers has been disconnected or archived; both mean the credential in hand is
// not the workspace's sender any more.
//
// Both answers are ErrChannelBindingReplaced, which is transient, so the ladder
// re-resolves rather than parking. A database that cannot answer is transient
// too, for the reason ChannelSenderFor states: a failure to get an answer must
// never destroy a legitimate message.
func (r *Registry) requireBindingUnchanged(ctx context.Context, resolved channelBinding) error {
	var version int64
	err := database.WithWorkspaceTx(ctx, r.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT version FROM channel_connection
			 WHERE id = $1 AND status = $2 AND archived_at IS NULL`,
			resolved.id, channelStatusConnected).Scan(&version)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("capture: the binding this message resolved through is no longer live: %w",
			ErrChannelBindingReplaced)
	}
	if err != nil {
		return fmt.Errorf("capture: re-reading the sending channel connection: %w", err)
	}
	if version != resolved.version {
		return fmt.Errorf("capture: the binding moved from version %d to %d while this message waited: %w",
			resolved.version, version, ErrChannelBindingReplaced)
	}
	return nil
}

// ChannelSendCapable answers the REQUEST-TIME pre-flight: has this workspace
// bound a bot for provider at all? It reads the same rows ChannelSenderFor
// resolves through and stops short of the credential, because unsealing a secret
// to answer a question about liveness would spend a vault round trip on every
// send request and turn a keyvault blip into a user-facing refusal — the posture
// the mailbox half takes for the same reason (compose's mailboxGrants).
//
// It deliberately does NOT distinguish the ambiguous case. Which of two live
// bindings transmits is the delivery path's decision, and that path refuses to
// guess; the pre-flight answers only the fact a rep can act on — that no bot is
// bound.
func (r *Registry) ChannelSendCapable(ctx context.Context, provider string) (bool, error) {
	bindings, err := r.liveChannelBindings(ctx, provider)
	if err != nil {
		return false, err
	}
	return len(bindings) > 0, nil
}

// channelBinding is one live connection as the send path needs it: the vault ref
// to unseal, plus the row identity and version that say WHICH binding the
// resolved credential came from. The pair travels with the credential so the
// send can prove, at the moment it transmits, that it is still spending the
// workspace's current bot.
type channelBinding struct {
	id            ids.UUID
	version       int64
	credentialRef string
}

// liveChannelBinding reads the one live binding, and refuses rather than picking
// when there are two. It collects the matching rows instead of taking the first:
// a QueryRow would silently prefer whichever row the planner returned, which is
// the same silent wrong-bot send the ambiguity error exists to prevent.
func (r *Registry) liveChannelBinding(ctx context.Context, provider string) (channelBinding, error) {
	bindings, err := r.liveChannelBindings(ctx, provider)
	if err != nil {
		return channelBinding{}, err
	}
	switch len(bindings) {
	case 0:
		return channelBinding{}, ErrNoConnection
	case 1:
		return bindings[0], nil
	default:
		return channelBinding{}, fmt.Errorf("capture: %d live %s connections: %w",
			len(bindings), provider, ErrChannelConnectionAmbiguous)
	}
}

// liveChannelBindings is the ONE spelling of what "live binding" means:
// connected, un-archived, this workspace's (RLS binds that). The send resolve
// and the pre-flight read the same predicate here rather than each carrying a
// copy, so a binding one of them counts is a binding the other does too.
func (r *Registry) liveChannelBindings(ctx context.Context, provider string) ([]channelBinding, error) {
	var bindings []channelBinding
	err := database.WithWorkspaceTx(ctx, r.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id, version, credential_ref FROM channel_connection
			 WHERE provider = $1 AND status = $2 AND archived_at IS NULL`,
			provider, channelStatusConnected)
		if err != nil {
			return err
		}
		bindings, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (channelBinding, error) {
			var b channelBinding
			return b, row.Scan(&b.id, &b.version, &b.credentialRef)
		})
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("capture: resolving the sending channel connection: %w", err)
	}
	return bindings, nil
}
