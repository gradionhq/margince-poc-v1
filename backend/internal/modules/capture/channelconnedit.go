// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package capture

// The two lifecycle changes a live channel connection admits after connect
// (telegram-oa design §5 and §9.2): replacing the bot token in place, and
// disconnecting. Split from channelconn.go to stay under the file-length cap;
// the ordering rules and the wiring guards they rely on live there.
//
// What makes editing safe rather than merely convenient: Telegram user ids are
// global, and person_channel_identity's key omits the bot id, so every identity
// binding — and all captured history — keeps resolving across a token rotation
// or even a swap to a different bot.

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/capture/telegram"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// channelRow is one connection plus the two vault refs the read shape
// deliberately hides — the lifecycle paths need them to revoke or supersede
// the credentials, and nothing else does.
type channelRow struct {
	ChannelConnection
	credentialRef keyvault.Ref
	secretRef     keyvault.Ref
}

// ReplaceToken points a live connection at a new bot token, re-running the
// full connect sequence (§9.2) and passing back through `pending` on the way
// to `connected` for the same reason the first connect does: the row must be
// the authority on where Telegram is being told to deliver at every instant,
// so it may not read `connected` while its registration is mid-flight.
//
// The connection row itself survives, which is the point: captured activities
// and every person_channel_identity binding are keyed on the Telegram user, not
// on this row or on the bot, so rotating the token — or swapping in a different
// bot — loses no history.
//
// The bot's PREVIOUS registration is deliberately not deleted. For a rotation
// of the same bot there is nothing to delete (Telegram allows one webhook per
// bot, so setWebhook below replaces it), and for a swap to a different bot the
// old bot's registration now carries a secret this connection no longer holds,
// so ingress refuses its deliveries — it cannot leak, and the revoked old token
// could not authorize a deleteWebhook anyway.
func (s *ChannelStore) ReplaceToken(ctx context.Context, id ids.UUID, token string) error {
	if err := auth.Require(ctx, channelConnectionObject, principal.ActionUpdate); err != nil {
		return err
	}
	if err := auth.RequireHuman(ctx); err != nil {
		return err
	}
	if err := s.requireConnectWiring(ProviderTelegram); err != nil {
		return err
	}
	if err := telegram.ValidateToken(token); err != nil {
		return err
	}
	ws, ok := principal.WorkspaceID(ctx)
	if !ok {
		return errors.New("capture: channel token replacement called outside a workspace context")
	}

	current, err := s.readChannelRow(ctx, id)
	if err != nil {
		return err
	}
	bot, err := s.api.GetMe(ctx, token)
	if err != nil {
		return err
	}
	if err := s.preflightWebhook(ctx, token); err != nil {
		return err
	}

	sealed, err := s.sealChannelSecrets(ctx, ws, token)
	if err != nil {
		return err
	}
	pending, err := s.repointPending(ctx, current, bot, sealed)
	if err != nil {
		if storekit.IsUniqueViolation(err) {
			sealed.destroy(ctx, s.vault, s.log, ws, "channel-replace-lost-race")
			return fmt.Errorf("this bot is already connected: %w", apperrors.ErrConflict)
		}
		return err
	}
	// The row now names the new refs, so the superseded pair is unreachable
	// from any row and must be destroyed here — not after setWebhook, which may
	// fail and return, leaving them orphaned with nothing left to name them.
	channelSecrets{credentialRef: current.credentialRef, secretRef: current.secretRef}.
		destroy(ctx, s.vault, s.log, ws, "channel-token-replaced")

	if err := s.api.SetWebhook(ctx, token, s.webhookURL(id), sealed.webhookSecret, channelAllowedUpdates); err != nil {
		// Same posture as connect: the pending row stays visible so an
		// operator can retry against the id already in the URL.
		return err
	}
	if _, err := s.markConnected(ctx, pending); err != nil {
		return err
	}
	return nil
}

// repointPending flips the connection back to `pending` and re-points it at the
// new bot and the new sealed pair, in one transaction with its audit row.
//
// The row is locked first because the decision to repoint was made from a read
// taken before two provider round trips: without the lock a concurrent rotation
// or disconnect in that window would be overwritten by a decision made against
// state that no longer holds.
func (s *ChannelStore) repointPending(ctx context.Context, current channelRow, bot telegram.Bot, sealed channelSecrets) (ChannelConnection, error) {
	var out ChannelConnection
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := storekit.LockRow(ctx, tx, "channel_connection", current.ID, storekit.LiveOnly); err != nil {
			return err
		}
		var err error
		out, err = scanChannelConnection(tx.QueryRow(ctx, `
			UPDATE channel_connection
			   SET channel_id = $2, channel_label = $3, credential_ref = $4,
			       webhook_secret_ref = $5, status = $6
			 WHERE id = $1 AND archived_at IS NULL
			 RETURNING `+channelConnectionColumns,
			current.ID, channelIDOf(bot), bot.Username,
			string(sealed.credentialRef), string(sealed.secretRef), channelStatusPending))
		if err != nil {
			return err
		}
		return auditLifecycle(ctx, tx, "update", channelConnectionObject, out.ID,
			channelAuditImage(current.ChannelID, current.ChannelLabel, current.Status),
			channelAuditImage(out.ChannelID, out.ChannelLabel, out.Status))
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ChannelConnection{}, apperrors.ErrNotFound
	}
	if err != nil {
		return ChannelConnection{}, err
	}
	return out, nil
}

// Disconnect withdraws the binding: it revokes the webhook at Telegram,
// archives the row as `disconnected`, and destroys both sealed credentials.
// Already-captured activities are retained — disconnecting stops capture, it
// does not erase history.
//
// The row is archived as well as flipped, which is what frees both partial
// unique indexes so the same bot can be connected again later. Its history does
// not live on this row: the activities and the person_channel_identity bindings
// outlive it.
func (s *ChannelStore) Disconnect(ctx context.Context, id ids.UUID) error {
	if err := auth.Require(ctx, channelConnectionObject, principal.ActionDelete); err != nil {
		return err
	}
	if err := auth.RequireHuman(ctx); err != nil {
		return err
	}
	if s.vault == nil {
		return errors.New("capture: no keyvault is configured — a channel credential cannot be revoked")
	}
	ws, ok := principal.WorkspaceID(ctx)
	if !ok {
		return errors.New("capture: channel disconnect called outside a workspace context")
	}

	current, err := s.readChannelRow(ctx, id)
	if err != nil {
		return err
	}
	s.revokeWebhook(ctx, ws, current)
	if err := s.archiveDisconnected(ctx, current); err != nil {
		return err
	}
	channelSecrets{credentialRef: current.credentialRef, secretRef: current.secretRef}.
		destroy(ctx, s.vault, s.log, ws, "channel-disconnected")
	return nil
}

// revokeWebhook tells Telegram to stop delivering, before the local teardown.
//
// A failure here is reported and the teardown continues, because the row — not
// Telegram — is what ingress consults: a delivery for an archived, disconnected
// connection is refused, so a registration we could not revoke cannot leak
// anything. Refusing the disconnect instead would leave the operator unable to
// end a binding whenever Telegram is down, which is the worse failure.
func (s *ChannelStore) revokeWebhook(ctx context.Context, ws ids.UUID, current channelRow) {
	if s.api == nil {
		s.log.WarnContext(ctx, "capture: no Telegram client is composed, so this channel's webhook registration was left in place — it delivers to an archived connection, which ingress refuses",
			"connection", current.ID.String())
		return
	}
	token, err := s.vault.Get(ctx, ids.From[ids.WorkspaceKind](ws), current.credentialRef)
	if err != nil {
		s.log.WarnContext(ctx, "capture: could not resolve a channel's bot token to revoke its webhook; the registration stays, delivering to an archived connection that ingress refuses",
			"connection", current.ID.String(), "err", err)
		return
	}
	if err := s.api.DeleteWebhook(ctx, string(token)); err != nil {
		s.log.WarnContext(ctx, "capture: Telegram refused to revoke a disconnected channel's webhook; the registration stays, delivering to an archived connection that ingress refuses",
			"connection", current.ID.String(), "err", err)
	}
}

// archiveDisconnected flips the row disconnected and archives it, with its
// audit row, in one transaction, under the row lock — so a rotation racing this
// teardown cannot interleave with it and leave the row live but pointing at
// credentials this call is about to destroy.
func (s *ChannelStore) archiveDisconnected(ctx context.Context, current channelRow) error {
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := storekit.LockRow(ctx, tx, "channel_connection", current.ID, storekit.LiveOnly); err != nil {
			return err
		}
		after, err := scanChannelConnection(tx.QueryRow(ctx, `
			UPDATE channel_connection SET status = $2, archived_at = now()
			 WHERE id = $1 AND archived_at IS NULL
			 RETURNING `+channelConnectionColumns, current.ID, channelStatusDisconnected))
		if err != nil {
			return err
		}
		return auditLifecycle(ctx, tx, "archive", channelConnectionObject, after.ID,
			channelAuditImage(current.ChannelID, current.ChannelLabel, current.Status),
			channelAuditImage(after.ChannelID, after.ChannelLabel, after.Status))
	})
	if errors.Is(err, pgx.ErrNoRows) {
		// A concurrent disconnect already archived it; the caller's intent
		// holds either way, but the row it named is gone.
		return apperrors.ErrNotFound
	}
	return err
}

// readChannelRow loads one live connection together with its vault refs. An
// archived, absent, or other-workspace row reads as ErrNotFound —
// existence-hiding, and an archived connection is not editable.
func (s *ChannelStore) readChannelRow(ctx context.Context, id ids.UUID) (channelRow, error) {
	var out channelRow
	var credentialRef, secretRef string
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `SELECT `+channelConnectionColumns+`, credential_ref, webhook_secret_ref
			 FROM channel_connection WHERE id = $1 AND archived_at IS NULL`, id)
		return row.Scan(&out.ID, &out.WorkspaceID, &out.Provider, &out.ChannelID, &out.ChannelLabel,
			&out.Status, &out.Version, &out.CreatedAt, &out.UpdatedAt, &credentialRef, &secretRef)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return channelRow{}, apperrors.ErrNotFound
	}
	if err != nil {
		return channelRow{}, fmt.Errorf("capture: reading channel connection %s: %w", id, err)
	}
	out.credentialRef = keyvault.Ref(credentialRef)
	out.secretRef = keyvault.Ref(secretRef)
	return out, nil
}
