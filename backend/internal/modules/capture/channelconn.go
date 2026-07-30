// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package capture

// The workspace-level channel connection (telegram-oa design §5): one bot
// binding per row, connected by an admin on behalf of the whole workspace
// rather than by one human over their own mailbox (which is what
// capture_connection models, and why channel_connection is a separate table —
// 0145_channel_connection.up.sql carries that reasoning).
//
// Connect's ORDERING is the load-bearing part of this file, and it is spelled
// out at Connect itself: the row must exist before setWebhook, because the
// webhook URL contains the connection id.
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
	"strings"
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
const ProviderTelegram = "telegram"

// The channel_connection lifecycle states this file drives. The column's CHECK
// also admits 'error' and 'reauth_required', which no path here sets yet —
// they belong to the ingress health signal, not to connect.
const (
	// channelStatusPending: the row exists so setWebhook has an id to
	// register against, but the registration has not yet succeeded. Ingress
	// and send both treat a pending row as not live.
	channelStatusPending = "pending"
	// channelStatusConnected: the webhook is registered and Telegram is
	// delivering here.
	channelStatusConnected = "connected"
	// channelStatusDisconnected: the operator withdrew the binding. Captured
	// activities remain — disconnecting is not erasing.
	channelStatusDisconnected = "disconnected"
)

// channelWebhookRoute is the ingress path segment the registered webhook URL
// is built from; the connection id follows it. Spelled here because connect is
// what registers the URL, and it must be the same route the webhook handler
// mounts.
const channelWebhookRoute = "/webhooks/telegram/"

// channelAllowedUpdates narrows what Telegram sends us: the messages a person
// writes, and the membership changes that tell us a person blocked or
// unblocked the bot. Anything else (polls, inline queries, edited channel
// posts) is bandwidth this system has no reader for, and asking for it would
// mean parking updates nobody consumes.
var channelAllowedUpdates = []string{"message", "my_chat_member"}

// channelConnectionColumns is the read shape, spelled once so every scan
// agrees with every select.
const channelConnectionColumns = `id, workspace_id, provider, channel_id, channel_label, status, version, created_at, updated_at`

// ChannelConnection is one channel binding as read back. Neither the bot token
// nor the webhook secret rides this shape: both live sealed in the vault,
// addressed by refs this type deliberately does not carry.
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
// webhook secret, the connecting human — is server-derived, so a caller cannot
// claim a bot identity it does not hold the token for.
type ConnectRequest struct {
	Provider string
	BotToken string
}

// ErrChannelWebhookOwnedElsewhere reports that the bot already delivers its
// updates to a URL this installation does not serve — the staging-vs-production
// collision. Telegram permits exactly ONE webhook per bot, so connecting anyway
// would silently steal the other installation's traffic. No local constraint
// can see this; only the provider knows.
var ErrChannelWebhookOwnedElsewhere = errors.New("capture: the bot's webhook is registered to another installation")

// ErrChannelWebhookBaseUnset reports that this deployment does not know its own
// externally-reachable origin, so it cannot tell Telegram where to deliver.
// Connect refuses rather than guessing: a bot registered against an
// unreachable URL reads `connected` and then simply falls quiet, which is
// indistinguishable from a healthy channel nobody is messaging.
var ErrChannelWebhookBaseUnset = errors.New("capture: no public webhook base URL is configured")

// ChannelStore owns channel_connection and its write shape. vault is the
// custodian of both sealed values; api is the Telegram boundary; webhookBase is
// this installation's externally-reachable origin. Each may be absent on a
// process role that composes no channel surface, and every entry point that
// needs one refuses loudly rather than proceeding half-wired.
type ChannelStore struct {
	pool        *pgxpool.Pool
	vault       keyvault.Vault
	api         telegram.API
	webhookBase string
	log         *slog.Logger
}

// NewChannelStore wires the channel-connection store. api and vault may be nil
// on a role that serves reads only; webhookBase may be empty when the
// deployment did not declare its origin — in both cases the mutating paths
// refuse with a named, actionable error instead of writing a connection that
// could never receive a delivery.
func NewChannelStore(pool *pgxpool.Pool, vault keyvault.Vault, api telegram.API, webhookBase string, log *slog.Logger) *ChannelStore {
	if log == nil {
		log = slog.Default()
	}
	return &ChannelStore{
		pool:        pool,
		vault:       vault,
		api:         api,
		webhookBase: strings.TrimRight(webhookBase, "/"),
		log:         log,
	}
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

// webhookURL renders the delivery target for one connection. The id is IN the
// URL — that is what forces the row to exist before setWebhook is called.
func (s *ChannelStore) webhookURL(id ids.UUID) string {
	return s.webhookBase + channelWebhookRoute + id.String()
}

// ownsWebhook reports whether a webhook URL Telegram already holds is one this
// installation registered. A URL under our own ingress route is ours to
// overwrite (a retried connect, or a bot we previously held); anything else
// belongs to another installation and is a conflict.
func (s *ChannelStore) ownsWebhook(registered string) bool {
	return strings.HasPrefix(registered, s.webhookBase+channelWebhookRoute)
}

// Connect binds one bot to this workspace. The ORDER is the design's (§5) and
// is not interchangeable:
//
//  1. getMe — validates the token and yields the bot id and username. A bad
//     token fails here, before anything is sealed or written.
//  2. getWebhookInfo preflight — a bot already delivering to another
//     installation is refused. Only the provider knows this.
//  3. Seal the token and a freshly minted webhook secret in the vault.
//  4. Insert the row with status='pending' (domain row + audit, one
//     transaction).
//  5. setWebhook against the URL carrying that row's id.
//  6. Flip to status='connected'.
//
// Step 4 MUST precede step 5, because the webhook URL contains the connection
// id: registering first would leave Telegram delivering to an id that does not
// exist, every delivery answering 401, and the half-registration living on
// Telegram's side where no local transaction can roll it back. A failure at
// step 5 instead leaves a `pending` row an operator can see and retry or clean
// up — a state the system can name.
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
	if err := s.preflightWebhook(ctx, req.BotToken); err != nil {
		return ChannelConnection{}, err
	}

	sealed, err := s.sealChannelSecrets(ctx, ws, req.BotToken)
	if err != nil {
		return ChannelConnection{}, err
	}
	row, err := s.insertPending(ctx, bot, sealed)
	if err != nil {
		// A lost race for either unique index is the one failure that
		// guarantees no row persisted, so the just-sealed refs are definitely
		// orphaned and safe to destroy. Any other error leaves the commit
		// outcome ambiguous, and deleting then could strand a live
		// connection's credentials — the same put-then-commit posture
		// Registry.Connect and overlay's Connect document.
		if storekit.IsUniqueViolation(err) {
			sealed.destroy(ctx, s.vault, s.log, ws, "channel-connect-lost-race")
			return ChannelConnection{}, fmt.Errorf("this bot is already connected: %w", apperrors.ErrConflict)
		}
		return ChannelConnection{}, err
	}

	if err := s.api.SetWebhook(ctx, req.BotToken, s.webhookURL(row.ID), sealed.webhookSecret, channelAllowedUpdates); err != nil {
		// The pending row and its sealed credentials are deliberately KEPT: an
		// operator retries or removes the connection, and a retry needs the
		// same id it already registered against.
		return ChannelConnection{}, err
	}
	return s.markConnected(ctx, row)
}

// requireConnectWiring refuses a connect this deployment cannot honestly
// complete: an unimplemented provider, a missing vault (nothing could seal the
// token), a missing Telegram boundary, or an unknown public origin. Each
// refusal names what to fix — a connect that half-succeeds is the failure mode
// this whole path is shaped to avoid.
func (s *ChannelStore) requireConnectWiring(provider string) error {
	if provider != ProviderTelegram {
		return fmt.Errorf("channel provider %q is not implemented: %w", provider, apperrors.ErrConflict)
	}
	if s.api == nil {
		return errors.New("capture: no Telegram client is composed — this process role serves no channel connect")
	}
	if s.vault == nil {
		return errors.New("capture: no keyvault is configured — a bot token cannot be sealed")
	}
	if s.webhookBase == "" {
		return fmt.Errorf("set --public-base-url (or --api-base-url when the api is on its own origin) to this installation's externally-reachable origin, so Telegram can be told where to deliver: %w",
			ErrChannelWebhookBaseUnset)
	}
	return nil
}

// preflightWebhook refuses a bot Telegram already delivers elsewhere. It runs
// before anything is sealed or written, so a refusal leaves no trace to clean
// up.
func (s *ChannelStore) preflightWebhook(ctx context.Context, token string) error {
	info, err := s.api.GetWebhookInfo(ctx, token)
	if err != nil {
		return err
	}
	if info.URL != "" && !s.ownsWebhook(info.URL) {
		// The registered URL is another installation's and is NOT echoed to
		// the caller: it names an internal host of a system the caller may
		// have no business knowing about.
		return fmt.Errorf("the bot already delivers its updates to another installation of this system: %w",
			ErrChannelWebhookOwnedElsewhere)
	}
	return nil
}

// channelSecrets is the pair of vault refs one connection is built on, plus the
// webhook secret's plaintext for the setWebhook call that follows. The
// plaintext lives only for the span of a connect: the row stores refs.
type channelSecrets struct {
	credentialRef keyvault.Ref
	secretRef     keyvault.Ref
	webhookSecret string
}

// sealChannelSecrets mints the webhook secret and seals it together with the
// bot token (put-then-commit: the vault holds both before any row names them).
// A failure sealing the second value destroys the first rather than leaving it
// orphaned — nothing references it, and there is no vault sweep to collect it.
func (s *ChannelStore) sealChannelSecrets(ctx context.Context, ws ids.UUID, token string) (channelSecrets, error) {
	secret, err := telegram.MintWebhookSecret()
	if err != nil {
		return channelSecrets{}, err
	}
	wsKey := ids.From[ids.WorkspaceKind](ws)
	credentialRef, err := s.vault.Put(ctx, wsKey, []byte(token))
	if err != nil {
		return channelSecrets{}, fmt.Errorf("capture: sealing the bot token: %w", err)
	}
	secretRef, err := s.vault.Put(ctx, wsKey, []byte(secret))
	if err != nil {
		keyvault.DeleteDetached(ctx, s.vault, s.log, ws, credentialRef, "channel-connect-seal-failed")
		return channelSecrets{}, fmt.Errorf("capture: sealing the webhook secret: %w", err)
	}
	return channelSecrets{credentialRef: credentialRef, secretRef: secretRef, webhookSecret: secret}, nil
}

// destroy removes both sealed values, for the paths that must undo a seal.
func (c channelSecrets) destroy(ctx context.Context, v keyvault.Vault, log *slog.Logger, ws ids.UUID, lifecycle string) {
	keyvault.DeleteDetached(ctx, v, log, ws, c.credentialRef, lifecycle)
	keyvault.DeleteDetached(ctx, v, log, ws, c.secretRef, lifecycle)
}

// insertPending writes the pending row and its audit in one transaction. It is
// the step that gives setWebhook an id to register against.
func (s *ChannelStore) insertPending(ctx context.Context, bot telegram.Bot, sealed channelSecrets) (ChannelConnection, error) {
	connectedBy, err := channelActor(ctx)
	if err != nil {
		return ChannelConnection{}, err
	}
	var out ChannelConnection
	err = database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		out, err = scanChannelConnection(tx.QueryRow(ctx, `
			INSERT INTO channel_connection
			  (workspace_id, provider, channel_id, channel_label, credential_ref, webhook_secret_ref, status, connected_by)
			VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid, $1, $2, $3, $4, $5, $6, $7)
			RETURNING `+channelConnectionColumns,
			ProviderTelegram, channelIDOf(bot), bot.Username,
			string(sealed.credentialRef), string(sealed.secretRef), channelStatusPending, connectedBy))
		if err != nil {
			return err
		}
		return auditLifecycle(ctx, tx, "create", channelConnectionObject, out.ID, nil,
			channelAuditImage(out.ChannelID, out.ChannelLabel, channelStatusPending))
	})
	if err != nil {
		return ChannelConnection{}, err
	}
	return out, nil
}

// markConnected flips a registered connection live. Reached only after
// setWebhook succeeded, so the row and Telegram agree from here on.
//
// The row is locked first because a provider round trip sat between the pending
// insert and this flip: a disconnect that landed in that window archived the row
// deliberately, and this write must not resurrect it as connected. The lock is
// taken inside this transaction only — never held across the provider call.
func (s *ChannelStore) markConnected(ctx context.Context, row ChannelConnection) (ChannelConnection, error) {
	var out ChannelConnection
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := storekit.LockRow(ctx, tx, "channel_connection", row.ID, storekit.LiveOnly); err != nil {
			return err
		}
		var err error
		out, err = scanChannelConnection(tx.QueryRow(ctx,
			`UPDATE channel_connection SET status = $2 WHERE id = $1 AND archived_at IS NULL
			 RETURNING `+channelConnectionColumns, row.ID, channelStatusConnected))
		if err != nil {
			return err
		}
		return auditLifecycle(ctx, tx, "update", channelConnectionObject, out.ID,
			channelAuditImage(row.ChannelID, row.ChannelLabel, row.Status),
			channelAuditImage(out.ChannelID, out.ChannelLabel, out.Status))
	})
	if errors.Is(err, pgx.ErrNoRows) {
		// The webhook is registered but the row is gone (a concurrent
		// disconnect). Report it rather than returning a connection that does
		// not exist; the registration is inert because ingress refuses an
		// unknown connection id.
		return ChannelConnection{}, apperrors.ErrNotFound
	}
	if err != nil {
		return ChannelConnection{}, err
	}
	return out, nil
}

// List returns the workspace's live channel connections, newest first. Read is
// granted to every role: a rep needs to see whether the channel is live, the
// same as an overlay connection's status.
func (s *ChannelStore) List(ctx context.Context) ([]ChannelConnection, error) {
	if err := auth.Require(ctx, channelConnectionObject, principal.ActionRead); err != nil {
		return nil, err
	}
	var out []ChannelConnection
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+channelConnectionColumns+
			` FROM channel_connection WHERE archived_at IS NULL ORDER BY created_at DESC, id DESC`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			conn, err := scanChannelConnection(rows)
			if err != nil {
				return err
			}
			out = append(out, conn)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("capture: listing channel connections: %w", err)
	}
	return out, nil
}

// Get returns one live channel connection. An archived, absent, or
// other-workspace row reads as ErrNotFound — existence-hiding, and an archived
// connection is no longer a connection.
func (s *ChannelStore) Get(ctx context.Context, id ids.UUID) (ChannelConnection, error) {
	if err := auth.Require(ctx, channelConnectionObject, principal.ActionRead); err != nil {
		return ChannelConnection{}, err
	}
	row, err := s.readChannelRow(ctx, id)
	if err != nil {
		return ChannelConnection{}, err
	}
	return row.ChannelConnection, nil
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
