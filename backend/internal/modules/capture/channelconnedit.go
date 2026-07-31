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
// A ROTATION of the same bot deletes nothing: Telegram keeps one webhook per
// bot, so the setWebhook below replaces the registration in place. A swap to a
// DIFFERENT bot must delete the outgoing bot's registration, because nothing else
// ever will — the old bot would go on delivering to this installation's URL with
// a secret the row no longer holds, every delivery refused, and invisible while
// the connection reads `connected` for the new bot.
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
	// Read while the row still NAMES the outgoing credential: it is destroyed a
	// few lines below, and a destroyed ref cannot authorize the deleteWebhook that
	// ends the outgoing bot's registration.
	outgoing := s.outgoingBotToken(ctx, ws, current, bot)

	pending, err := s.repointPending(ctx, current, bot, sealed)
	if err != nil {
		// Both of these prove the transaction wrote nothing — a unique index
		// refused it, or zero rows matched the version predicate — so the pair
		// just sealed is definitely orphaned and safe to destroy. Any other error
		// leaves the commit outcome ambiguous, and destroying then could strand a
		// live connection's credentials.
		if storekit.IsUniqueViolation(err) {
			sealed.destroy(ctx, s.vault, s.log, ws, "channel-replace-lost-race")
			return fmt.Errorf("this bot is already connected: %w", apperrors.ErrConflict)
		}
		if errors.Is(err, apperrors.ErrVersionSkew) {
			sealed.destroy(ctx, s.vault, s.log, ws, "channel-replace-lost-race")
		}
		return err
	}
	// The row now names the new refs, so the superseded pair is unreachable
	// from any row and must be destroyed here — not after setWebhook, which may
	// fail and return, leaving them orphaned with nothing left to name them.
	channelSecrets{credentialRef: current.credentialRef, secretRef: current.secretRef}.
		destroy(ctx, s.vault, s.log, ws, "channel-token-replaced")

	// Revoked HERE — after the row stopped naming the outgoing secret, and before
	// the incoming bot is registered. After, because until the repoint commits the
	// outgoing bot is still this connection's live channel and a failure earlier in
	// the sequence must leave it working. Before, because setWebhook can fail and
	// return: revoking afterwards would skip the cleanup on exactly the run that
	// leaves the row `pending` and both bots pointed at the same URL. The two
	// registrations are independent — Telegram keeps one webhook per BOT — so
	// removing the outgoing one cannot disturb the incoming one either way.
	s.revokeOutgoingWebhook(ctx, current, outgoing)

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
// state that no longer holds. The lock alone only SERIALIZES those writers,
// though — it does not tell this one that its snapshot went stale — so the
// version read with the snapshot travels into the WHERE clause as a predicate.
// Zero rows matched means another replacement already moved this connection on,
// and this one's remaining steps (a setWebhook for a bot the row no longer names,
// then a flip to `connected`) would be acting on a decision that no longer holds.
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
			 WHERE id = $1 AND version = $7 AND archived_at IS NULL
			 RETURNING `+channelConnectionColumns,
			current.ID, channelIDOf(bot), bot.Username,
			string(sealed.credentialRef), string(sealed.secretRef), channelStatusPending,
			current.Version))
		if err != nil {
			return err
		}
		return auditLifecycle(ctx, tx, "update", channelConnectionObject, out.ID,
			channelAuditImage(current.ChannelID, current.ChannelLabel, current.Status),
			channelAuditImage(out.ChannelID, out.ChannelLabel, out.Status))
	})
	if errors.Is(err, pgx.ErrNoRows) {
		// The lock resolved a LIVE row, so the live clause held and only the
		// version clause can have failed.
		return ChannelConnection{}, apperrors.ErrVersionSkew
	}
	if err != nil {
		return ChannelConnection{}, err
	}
	return out, nil
}

// outgoingBotToken resolves the plaintext token of the bot a replacement is
// swapping OUT, or "" when there is nothing to revoke. A rotation of the SAME
// bot returns "" because setWebhook replaces that bot's one registration in
// place; only a change of bot leaves a registration nobody would otherwise end.
//
// A vault that cannot answer is LOGGED and reported as nothing to revoke. The
// replacement is the operator's instruction and does not hinge on tidying up the
// bot it replaces — but the log has to name the consequence, because nothing else
// will notice it.
func (s *ChannelStore) outgoingBotToken(ctx context.Context, ws ids.UUID, current channelRow, incoming telegram.Bot) string {
	if current.ChannelID == channelIDOf(incoming) {
		return ""
	}
	token, err := s.vault.Get(ctx, ids.From[ids.WorkspaceKind](ws), current.credentialRef)
	if err != nil {
		s.log.WarnContext(ctx, "capture: could not resolve the outgoing bot's token, so its webhook stays registered against this installation; that bot's deliveries will be refused for a secret this connection no longer holds — clear the registration in BotFather",
			"connection", current.ID.String(), "outgoing_bot", current.ChannelID, "err", err)
		return ""
	}
	return string(token)
}

// revokeOutgoingWebhook ends the registration of the bot a replacement swapped
// out. An empty token means there was nothing to revoke.
//
// A failure is reported and the replacement continues, the same posture
// revokeWebhook takes for disconnect: the row is already the authority on which
// bot this connection is, and refusing here would leave an operator unable to
// finish a swap whenever Telegram is down. What the log must carry is the
// consequence — the outgoing bot keeps delivering to a URL that refuses it, and
// only an operator can end that.
func (s *ChannelStore) revokeOutgoingWebhook(ctx context.Context, current channelRow, token string) {
	if token == "" {
		return
	}
	if err := s.api.DeleteWebhook(ctx, token); err != nil {
		s.log.WarnContext(ctx, "capture: Telegram refused to revoke the outgoing bot's webhook, so it stays registered against this installation; that bot's deliveries will be refused for a secret this connection no longer holds — clear the registration in BotFather",
			"connection", current.ID.String(), "outgoing_bot", current.ChannelID, "err", err)
	}
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
//
// A replacement that commits while this call is out at the provider makes it
// refuse with version skew, teardown untouched: the operator asked to disconnect
// the bot they were shown, not the one that has since taken the connection over.
func (s *ChannelStore) Disconnect(ctx context.Context, id ids.UUID) error {
	if err := auth.Require(ctx, channelConnectionObject, principal.ActionDelete); err != nil {
		return err
	}
	if err := auth.RequireHuman(ctx); err != nil {
		return err
	}
	if s.vault == nil {
		return fmt.Errorf("configure a credential store for this installation, so the bot's sealed credentials can be destroyed: %w",
			ErrChannelWiringIncomplete)
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

// archiveDisconnected flips the row disconnected and archives it, with its audit
// row, in one transaction, under the row lock.
//
// The lock is taken because the teardown was decided from a read taken before a
// provider round trip. It only SERIALIZES the writers, though — it does not tell
// this one that its snapshot went stale — so `current.Version` travels into the
// WHERE clause as a predicate, exactly as the replacement path does. Zero rows
// matched means a replacement already repointed this connection at another bot:
// archiving anyway would retire that bot's live connection, record the outgoing
// bot as the one disconnected, and send the caller on to destroy a sealed pair
// the row no longer names — leaving the winner's pair with nothing to collect it.
func (s *ChannelStore) archiveDisconnected(ctx context.Context, current channelRow) error {
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := storekit.LockRow(ctx, tx, "channel_connection", current.ID, storekit.LiveOnly); err != nil {
			return err
		}
		after, err := scanChannelConnection(tx.QueryRow(ctx, `
			UPDATE channel_connection SET status = $2, archived_at = now()
			 WHERE id = $1 AND version = $3 AND archived_at IS NULL
			 RETURNING `+channelConnectionColumns, current.ID, channelStatusDisconnected, current.Version))
		if err != nil {
			return err
		}
		return auditLifecycle(ctx, tx, "archive", channelConnectionObject, after.ID,
			channelAuditImage(current.ChannelID, current.ChannelLabel, current.Status),
			channelAuditImage(after.ChannelID, after.ChannelLabel, after.Status))
	})
	if errors.Is(err, pgx.ErrNoRows) {
		// The lock resolved a LIVE row — an already-archived or absent one fails
		// there as ErrNotFound — so only the version clause can have failed, and
		// the caller must abort before its teardown touches the winner's state.
		return apperrors.ErrVersionSkew
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
