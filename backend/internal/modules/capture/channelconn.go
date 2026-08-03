// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package capture

// The workspace-level channel connection (telegram-oa design v2 §4): one bot
// binding per row, connected by an admin on behalf of the whole workspace
// rather than by one human over their own mailbox (which is what
// capture_connection models, and why channel_connection is a separate table —
// 0151_channel_connection.up.sql carries that reasoning).
//
// Connect's ORDERING is the load-bearing part of this file, and it is spelled
// out at Connect itself. Ingress PULLS (compose/telegrampoll.go), so there is no
// registration to make and no provider call after the insert: connect either
// succeeds as `connected` or writes nothing.
//
// The write is AUDIT-ONLY, the same ratified posture auditLifecycle documents
// for capture_connection (EVT-NOEVT-3): the closed event catalog
// (shared/kernel/events) defines no verb for a channel connection, so there is
// no event half to emit. Adding one is a spec change, not a local decision.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/capture/telegram"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// channelConnectionObject is the RBAC object gating the channel-connection
// surface (identity/internal/policy coreObjects). Connecting a bot is
// destructive workspace-wide config — every seat's inbound Telegram traffic
// arrives through it — so create/update/delete are admin/ops-only while every
// role may read the status, the same posture overlay_connection holds.
const channelConnectionObject = "channel_connection"

// ProviderTelegram is the only channel provider implemented, and the only
// value channel_connection.provider's CHECK admits.
const ProviderTelegram = telegram.ProviderName

// The channel_connection lifecycle states. There is deliberately no
// half-connected state: a pull ingress makes no provider call after the insert,
// so no row can be written that is not already live.
const (
	// channelStatusConnected: the binding is live and the dispatcher polls it.
	channelStatusConnected = "connected"
	// channelStatusDisconnected: the operator withdrew the binding. Captured
	// activities remain — disconnecting is not erasing.
	channelStatusDisconnected = "disconnected"
	// channelStatusError: the poll cannot proceed for a reason no retry repairs
	// — another consumer holds this bot's updates. The due-scan stops selecting
	// the row, so this is what actually parks it.
	channelStatusError = "error"
	// channelStatusReauthRequired: Telegram refused the sealed token. The admin
	// re-pastes it (ReplaceToken); nothing else can recover from here.
	channelStatusReauthRequired = "reauth_required"
)

// ChannelPollStopped names the two statuses a failing poll parks a connection
// under, so the poller in the composition layer states them by name rather than
// re-spelling the column's vocabulary.
const (
	ChannelPollStoppedByRivalConsumer = channelStatusError
	ChannelPollStoppedByBadToken      = channelStatusReauthRequired
)

// channelConnectionColumns is the read shape, spelled once so every scan
// agrees with every select.
const channelConnectionColumns = `id, workspace_id, provider, channel_id, channel_label, status, version, created_at, updated_at`

// ChannelConnection is one channel binding as read back. No vault ref rides this
// shape: the bot token lives sealed in the vault, addressed by a ref no caller
// here has a legitimate reason to hold — List, Get and Connect's own response all
// leave it out, and the poller reads it through its own narrow shape
// (ChannelPollTarget).
type ChannelConnection struct {
	ID          ids.UUID
	WorkspaceID ids.UUID
	Provider    string
	ChannelID   string
	// ChannelLabel is the bot's @username — display only. A bot's username is
	// mutable and re-assignable, so it identifies nothing; ChannelID (the
	// bot's global numeric id) is the key.
	ChannelLabel string
	Status       string
	Version      int64
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// ConnectRequest is Connect's input: which provider, and the BotFather token to
// seal. Everything else about the connection — the bot id, its label, the
// connecting human — is server-derived, so a caller cannot claim a bot identity
// it does not hold the token for.
type ConnectRequest struct {
	Provider string
	BotToken string
}

// ErrChannelWorkspaceBotAlreadyBound reports that this workspace already holds a
// live bot. It is a DIFFERENT refusal from the bot already being bound somewhere:
// the remedy is to disconnect the existing binding, not to pick another bot.
//
// Only one live bot per workspace is permitted, because every outbound reply
// resolves the workspace's bot and the send path refuses to guess between two —
// so a second binding would not add a channel, it would take away the ability to
// reply on either.
var ErrChannelWorkspaceBotAlreadyBound = errors.New("capture: this workspace already has a live channel connection")

// ErrChannelWiringIncomplete reports that this deployment composed no credential
// custodian, so a bot token can neither be sealed nor destroyed. It is a
// DEPLOYMENT FACT an operator can act on, not an internal fault, so it refuses by
// name (503) instead of the opaque 500 an untyped error would become — which
// sends whoever is looking at the screen to a server log they may not have.
var ErrChannelWiringIncomplete = errors.New("capture: no credential custodian is composed for the channel surface")

// ChannelStore owns channel_connection and its write shape. vault is the
// custodian of the sealed bot token; api is the Telegram boundary.
//
// Connecting needs no public address of our own: a poll dials OUT, so this store
// has nothing to tell the provider about where we are.
//
// api is REQUIRED — every composition of this store supplies one, and a role
// that serves no channel surface composes no store at all rather than a
// client-less one. vault may be absent on a deployment that configured none, and
// every entry point that needs it refuses by name rather than proceeding
// half-wired.
type ChannelStore struct {
	pool  *pgxpool.Pool
	vault keyvault.Vault
	api   telegram.API
	log   *slog.Logger
}

// NewChannelStore wires the channel-connection store. vault may be nil when the
// deployment configured none — the mutating paths then refuse with a named,
// actionable error instead of writing a connection whose token nothing could
// unseal.
func NewChannelStore(pool *pgxpool.Pool, vault keyvault.Vault, api telegram.API, log *slog.Logger) *ChannelStore {
	if log == nil {
		log = slog.Default()
	}
	return &ChannelStore{pool: pool, vault: vault, api: api, log: log}
}

// withVault returns a copy of the store carrying the credential custodian. The
// composition root learns the vault from a later option than the one that built
// the store, and a copy keeps that late binding from mutating an instance a
// concurrent request could already be reading through.
func (s *ChannelStore) withVault(vault keyvault.Vault) *ChannelStore {
	next := *s
	next.vault = vault
	return &next
}

// Connect binds one bot to this workspace. The ORDER is the design's (v2 §4) and
// is not interchangeable:
//
//  1. getMe — validates the token and yields the bot id and username. A bad
//     token fails here, before anything is sealed or written.
//  2. deleteWebhook — clears any registration the bot still carries, because
//     Telegram refuses getUpdates while one exists. Pending updates are
//     deliberately KEPT: they are the customer's messages.
//  3. Seal the token in the vault.
//  4. Insert the row `connected` with a zero cursor, in one transaction with its
//     audit row.
//
// Nothing follows step 4 — no registration, no flip — which is why there is no
// `pending` state to reach: a poll dials out, so a committed row is already live,
// and a failure anywhere before the commit leaves nothing behind but a vault entry
// this path destroys itself. The dispatcher's next tick picks the row up.
//
// Step 2 precedes step 3 for the ordinary reason: a provider refusal must not
// leave a sealed secret nothing names.
func (s *ChannelStore) Connect(ctx context.Context, req ConnectRequest) (ChannelConnection, error) {
	if err := auth.Require(ctx, channelConnectionObject, principal.ActionCreate); err != nil {
		return ChannelConnection{}, err
	}
	// Human-only: the token grants read of every message the bot receives, so
	// an agent must not be able to bind one on its own initiative.
	if err := auth.RequireHuman(ctx); err != nil {
		return ChannelConnection{}, err
	}
	if err := s.requireConnectWiring(req.Provider); err != nil {
		return ChannelConnection{}, err
	}
	if err := telegram.ValidateToken(req.BotToken); err != nil {
		return ChannelConnection{}, err
	}
	ws, ok := principal.WorkspaceID(ctx)
	if !ok {
		return ChannelConnection{}, errors.New("capture: channel connect called outside a workspace context")
	}

	bot, err := s.api.GetMe(ctx, req.BotToken)
	if err != nil {
		return ChannelConnection{}, err
	}
	if err := s.clearWebhook(ctx, req.BotToken); err != nil {
		return ChannelConnection{}, err
	}

	credentialRef, err := s.sealBotToken(ctx, ws, req.BotToken)
	if err != nil {
		return ChannelConnection{}, err
	}
	row, err := s.insertConnected(ctx, bot, credentialRef)
	if err != nil {
		// A lost race for either unique index is the one failure that
		// GUARANTEES no row persisted, so the just-sealed ref is definitely
		// orphaned and safe to destroy. Any other error leaves the commit
		// outcome ambiguous, and deleting then could strand a live
		// connection's credential — the same put-then-commit posture
		// Registry.Connect and overlay's Connect document.
		if constraint, unique := storekit.UniqueViolation(err); unique {
			keyvault.DeleteDetached(ctx, s.vault, s.log, ws, credentialRef, "channel-connect-lost-race")
			return ChannelConnection{}, channelUniquenessRefusal(constraint)
		}
		return ChannelConnection{}, err
	}
	return row, nil
}

// channelUniquenessRefusal names WHICH uniqueness rule a connect or a
// replacement lost to, because the two send an operator to do different things:
// one says pick a different bot, the other says disconnect the bot this workspace
// already has. Answering both as "this bot is already connected" leaves an admin
// hunting for a binding of a bot they have never used.
func channelUniquenessRefusal(constraint string) error {
	if constraint == channelWorkspaceUniqueIndex {
		return fmt.Errorf("another bot is already connected to this workspace; disconnect it first: %w",
			ErrChannelWorkspaceBotAlreadyBound)
	}
	return fmt.Errorf("this bot is already connected: %w", apperrors.ErrConflict)
}

// channelWorkspaceUniqueIndex is the partial unique index that permits ONE live
// bot per workspace (0151). Named here because the refusal above branches on it,
// and a rename in the migration must break this compile-time-invisible link
// loudly — which the connect suite's two distinct refusals are what enforce.
const channelWorkspaceUniqueIndex = "uq_channel_connection_ws"

// requireConnectWiring refuses a connect this deployment cannot honestly
// complete: an unimplemented provider, or a missing vault (nothing could seal the
// token). Each refusal names what to fix.
func (s *ChannelStore) requireConnectWiring(provider string) error {
	if provider != ProviderTelegram {
		return fmt.Errorf("channel provider %q is not implemented: %w", provider, apperrors.ErrConflict)
	}
	if s.vault == nil {
		return fmt.Errorf("configure a credential store for this installation, so a bot token can be sealed: %w",
			ErrChannelWiringIncomplete)
	}
	return nil
}

// clearWebhook removes any webhook a bot still carries, because Telegram refuses
// getUpdates for a bot with one registered (telegram.ErrWebhookActive). It runs
// before anything is sealed or written, so a provider refusal leaves no trace to
// clean up.
//
// drop_pending_updates is NOT sent: those pending updates are the customer's
// messages, and the first poll is meant to collect them.
//
// This DOES take a bot away from whatever had registered that webhook — a staging
// installation, or an unrelated integration. That is unavoidable rather than
// overlooked: the two ingress modes cannot coexist on one bot, so binding a bot
// here means taking it, and the operator pasting the token is the one asserting
// they may.
func (s *ChannelStore) clearWebhook(ctx context.Context, token string) error {
	if err := s.api.DeleteWebhook(ctx, token); err != nil {
		return fmt.Errorf("capture: clearing the bot's webhook so its updates can be polled: %w", err)
	}
	return nil
}

// sealBotToken puts the token in the vault before any row names it
// (put-then-commit). One secret per connection now: a poll authenticates with the
// bot token alone, so there is no second value to mint, rotate or destroy.
func (s *ChannelStore) sealBotToken(ctx context.Context, ws ids.UUID, token string) (keyvault.Ref, error) {
	ref, err := s.vault.Put(ctx, ids.From[ids.WorkspaceKind](ws), []byte(token))
	if err != nil {
		return "", fmt.Errorf("capture: sealing the bot token: %w", err)
	}
	return ref, nil
}

// insertConnected writes the live row and its audit in one transaction. The
// cursor starts at 0 — "whatever Telegram still holds" — so a freshly connected
// bot collects the messages waiting for it rather than starting from silence.
func (s *ChannelStore) insertConnected(ctx context.Context, bot telegram.Bot, credentialRef keyvault.Ref) (ChannelConnection, error) {
	connectedBy, err := channelActor(ctx)
	if err != nil {
		return ChannelConnection{}, err
	}
	var out ChannelConnection
	err = database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		out, err = scanChannelConnection(tx.QueryRow(ctx, `
			INSERT INTO channel_connection
			  (workspace_id, provider, channel_id, channel_label, credential_ref, status, poll_offset, connected_by)
			VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid, $1, $2, $3, $4, $5, 0, $6)
			RETURNING `+channelConnectionColumns,
			ProviderTelegram, channelIDOf(bot), bot.Username,
			string(credentialRef), channelStatusConnected, connectedBy))
		if err != nil {
			return err
		}
		return auditLifecycle(ctx, tx, "create", channelConnectionObject, out.ID, nil,
			channelAuditImage(out.ChannelID, out.ChannelLabel, out.Status))
	})
	if err != nil {
		return ChannelConnection{}, err
	}
	return out, nil
}

// channelIDOf renders the bot's global numeric id as the channel_id column's
// text. Text rather than bigint because channel_id is the provider's opaque
// handle for whatever a future provider calls a channel, not an integer this
// system does arithmetic on.
func channelIDOf(bot telegram.Bot) string {
	return fmt.Sprintf("%d", bot.ID)
}

// channelActor resolves the human a connection is attributed to. connected_by
// is audit only — never an owner — but it is NOT NULL, so a principal with no
// human identity cannot connect a channel.
func channelActor(ctx context.Context) (ids.UUID, error) {
	p, err := storekit.Actor(ctx)
	if err != nil {
		return ids.Nil, err
	}
	switch {
	case !p.UserID.IsZero():
		return p.UserID, nil
	case !p.OnBehalfOf.IsZero():
		return p.OnBehalfOf, nil
	default:
		return ids.Nil, fmt.Errorf("a channel connection records the human who made it: %w", apperrors.ErrPermissionDenied)
	}
}

// channelAuditImage is one side of a channel connection's audit trail. Neither
// vault ref appears: the audit spine must not become a second custodian of the
// credentials, and a ref tells a reader nothing the bot's own identity does not.
func channelAuditImage(channelID, label, status string) map[string]any {
	return map[string]any{
		"provider":      ProviderTelegram,
		"channel_id":    channelID,
		"channel_label": label,
		"status":        status,
	}
}

func scanChannelConnection(r pgx.Row) (ChannelConnection, error) {
	var c ChannelConnection
	err := r.Scan(&c.ID, &c.WorkspaceID, &c.Provider, &c.ChannelID, &c.ChannelLabel,
		&c.Status, &c.Version, &c.CreatedAt, &c.UpdatedAt)
	return c, err
}
