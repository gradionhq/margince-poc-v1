// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package capture

// The two lifecycle changes a live channel connection admits after connect
// (telegram-oa design v2 §4): replacing the bot token in place, and
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

// channelRow is one connection plus the vault ref the read shape deliberately
// hides — the lifecycle paths need it to supersede or destroy the credential, and
// nothing else does.
type channelRow struct {
	ChannelConnection
	credentialRef keyvault.Ref
}

// ReplaceToken points a live connection at a new bot token (design v2 §4). It
// never passes through a not-live state: a poll dials out, so the row is the only
// thing that decides which token the next poll spends, and repointing it IS the
// whole change.
//
// The connection row itself survives, which is the point: captured activities and
// every person_channel_identity binding are keyed on the Telegram user, not on
// this row or on the bot, so rotating the token — or swapping in a different bot —
// loses no history.
//
// The incoming bot's webhook is cleared for the same reason connect clears one:
// Telegram refuses getUpdates while a webhook is registered. The OUTGOING bot
// needs nothing: it stops being polled the instant the row stops naming it, and
// this installation never registered anything against it that could outlive that.
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
	if err := s.clearWebhook(ctx, token); err != nil {
		return err
	}

	credentialRef, err := s.sealBotToken(ctx, ws, token)
	if err != nil {
		return err
	}
	if err := s.repoint(ctx, current, bot, credentialRef); err != nil {
		// Each of these proves the transaction wrote NOTHING — a unique index
		// refused it, zero rows matched the version predicate, or the row was
		// archived out from under it — so the ref just sealed is definitely
		// orphaned and safe to destroy. Any other error leaves the commit outcome
		// ambiguous, and destroying then could strand a live connection's
		// credential.
		if constraint, unique := storekit.UniqueViolation(err); unique {
			keyvault.DeleteDetached(ctx, s.vault, s.log, ws, credentialRef, "channel-replace-lost-race")
			return channelUniquenessRefusal(constraint)
		}
		if errors.Is(err, apperrors.ErrVersionSkew) || errors.Is(err, apperrors.ErrNotFound) {
			keyvault.DeleteDetached(ctx, s.vault, s.log, ws, credentialRef, "channel-replace-lost-race")
		}
		return err
	}
	// The row now names the new ref, so the superseded one is unreachable from any
	// row and must be destroyed here — nothing else will ever collect it.
	keyvault.DeleteDetached(ctx, s.vault, s.log, ws, current.credentialRef, "channel-token-replaced")
	return nil
}

// repoint re-points the connection at the new bot and its newly sealed token, in
// one transaction with its audit row. The status is untouched: the row was live
// before and is live after, under a different token.
//
// The row is locked first because the decision to repoint was made from a read
// taken before two provider round trips: without the lock a concurrent rotation
// or disconnect in that window would be overwritten by a decision made against
// state that no longer holds. The lock alone only SERIALIZES those writers,
// though — it does not tell this one that its snapshot went stale — so the
// version read with the snapshot travels into the WHERE clause as a predicate.
// Zero rows matched means another replacement already moved this connection on,
// and this one would otherwise destroy the credential that winner is now polling
// with.
func (s *ChannelStore) repoint(ctx context.Context, current channelRow, bot telegram.Bot, credentialRef keyvault.Ref) error {
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := storekit.LockRow(ctx, tx, "channel_connection", current.ID, storekit.LiveOnly); err != nil {
			return err
		}
		out, err := scanChannelConnection(tx.QueryRow(ctx, `
			UPDATE channel_connection
			   SET channel_id = $2, channel_label = $3, credential_ref = $4, poll_offset = 0
			 WHERE id = $1 AND version = $5 AND archived_at IS NULL
			 RETURNING `+channelConnectionColumns,
			current.ID, channelIDOf(bot), bot.Username, string(credentialRef), current.Version))
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
		return apperrors.ErrVersionSkew
	}
	return err
}

// Disconnect withdraws the binding: it archives the row as `disconnected` and
// destroys the sealed token. Already-captured activities are retained —
// disconnecting stops capture, it does not erase history.
//
// Archiving is what actually stops ingress: the poll dispatcher's due-scan selects
// only live `connected` rows, so an archived connection is never polled again. It
// also frees both partial unique indexes, so the same bot — or another one — can
// be connected here again later.
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
	if err := s.archiveDisconnected(ctx, current); err != nil {
		return err
	}
	keyvault.DeleteDetached(ctx, s.vault, s.log, ws, current.credentialRef, "channel-disconnected")
	return nil
}

// archiveDisconnected flips the row disconnected and archives it, with its audit
// row, in one transaction, under the row lock.
//
// The lock is taken because the teardown was decided from a read taken outside
// this transaction. It only SERIALIZES the writers, though — it does not tell
// this one that its snapshot went stale — so `current.Version` travels into the
// WHERE clause as a predicate, exactly as the replacement path does. Zero rows
// matched means a replacement already repointed this connection at another bot:
// archiving anyway would retire that bot's live connection, record the outgoing
// bot as the one disconnected, and send the caller on to destroy a credential the
// row no longer names — leaving the winner's with nothing to collect it.
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

// readChannelRow loads one live connection together with its vault ref. An
// archived, absent, or other-workspace row reads as ErrNotFound —
// existence-hiding, and an archived connection is not editable.
func (s *ChannelStore) readChannelRow(ctx context.Context, id ids.UUID) (channelRow, error) {
	var out channelRow
	var credentialRef string
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `SELECT `+channelConnectionColumns+`, credential_ref
			 FROM channel_connection WHERE id = $1 AND archived_at IS NULL`, id)
		return row.Scan(&out.ID, &out.WorkspaceID, &out.Provider, &out.ChannelID, &out.ChannelLabel,
			&out.Status, &out.Version, &out.CreatedAt, &out.UpdatedAt, &credentialRef)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return channelRow{}, apperrors.ErrNotFound
	}
	if err != nil {
		return channelRow{}, fmt.Errorf("capture: reading channel connection %s: %w", id, err)
	}
	out.credentialRef = keyvault.Ref(credentialRef)
	return out, nil
}
