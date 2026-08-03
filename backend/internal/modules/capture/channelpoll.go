// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package capture

// The channel_connection reads and writes the getUpdates poller works through
// (design v2 §3). Ingress pulls rather than being pushed, so nothing carries a
// tenant into this path: the dispatcher enumerates the fleet the way the Gmail
// push walk does (push.go), and every per-connection step runs under the
// workspace the enumeration resolved.
//
// The offset write is the load-bearing one. getUpdates(offset = poll_offset) is
// ALSO Telegram's acknowledgement of everything below that number, so the
// advance is exposed as a Tx call and nothing else: its caller commits it in the
// same transaction that made the batch durable, and there is deliberately no
// pool-level variant that could be spent on its own.

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// DueChannelConnection names one connection the dispatcher schedules a poll
// for. The workspace travels with it because the poll job re-establishes the
// tenant from its args, exactly as CaptureSyncArgs does — a job queue carries no
// request context to inherit one from.
type DueChannelConnection struct {
	WorkspaceID ids.UUID
	ID          ids.UUID
}

// ChannelPollTarget is one connection as a poll needs it, and nothing more: the
// bot the raw key is namespaced by, the sealed token to authenticate with, and
// the cursor to ask from.
type ChannelPollTarget struct {
	ID          ids.UUID
	WorkspaceID ids.UUID
	// ChannelID is the bot's numeric id. It namespaces the raw_capture key,
	// because update_id is a PER-BOT sequence.
	ChannelID     string
	CredentialRef keyvault.Ref
	PollOffset    int64
}

// DueChannelConnections lists every live connection of one provider across the
// fleet. Only a `connected`, un-archived row is due: a disconnected or
// reauth-required connection has nothing this installation may still poll, and
// leaving it out of the scan is what stops the poll retrying a bot whose token
// Telegram has already refused.
//
// One workspace's probe failing does NOT abandon the rest — the scan accumulates
// and returns what it found alongside the joined error, the posture the push
// walk takes. A single broken tenant must not stop every other workspace's
// messages arriving.
func DueChannelConnections(ctx context.Context, pool *pgxpool.Pool, provider string) ([]DueChannelConnection, error) {
	// rls-exempt: fleet enumeration — the workspace table is not workspace-scoped; a scheduled poll carries no tenant, so every workspace is scanned under its own GUC.
	rows, err := pool.Query(ctx, `SELECT id FROM workspace WHERE archived_at IS NULL`)
	if err != nil {
		return nil, fmt.Errorf("capture: listing workspaces for the channel poll scan: %w", err)
	}
	workspaces, err := pgx.CollectRows(rows, pgx.RowTo[ids.UUID])
	if err != nil {
		return nil, err
	}

	var due []DueChannelConnection
	var errs error
	for _, wsID := range workspaces {
		wsCtx := principal.WithWorkspaceID(ctx, wsID)
		err := database.WithWorkspaceTx(wsCtx, pool, func(tx pgx.Tx) error {
			rows, err := tx.Query(ctx, `
				SELECT id FROM channel_connection
				 WHERE provider = $1 AND status = $2 AND archived_at IS NULL`,
				provider, channelStatusConnected)
			if err != nil {
				return err
			}
			live, err := pgx.CollectRows(rows, pgx.RowTo[ids.UUID])
			if err != nil {
				return err
			}
			for _, id := range live {
				due = append(due, DueChannelConnection{WorkspaceID: wsID, ID: id})
			}
			return nil
		})
		if err != nil {
			errs = errors.Join(errs, fmt.Errorf("capture: channel poll scan in workspace %s: %w", wsID, err))
		}
	}
	return due, errs
}

// LoadChannelPollTarget reads the connection a poll is about to run, under the
// workspace already bound to ctx. A row that is no longer live reads as
// ErrNotFound: a disconnect landing between the dispatcher's scan and this read
// is the ordinary race, and the poll simply has nothing left to do.
//
// It takes `provider` for the same reason the due-scan does, and the two must keep
// agreeing: a scan that schedules rows this read then declines to resolve turns
// into the silent no-op above, which is indistinguishable from a disconnect.
//
// The offset is read HERE and not carried in the job args, because it moves: an
// args-borne cursor would be whatever was true when the job was enqueued, and a
// retried job would re-ask from a point already acknowledged.
func LoadChannelPollTarget(ctx context.Context, pool *pgxpool.Pool, provider string, id ids.UUID) (ChannelPollTarget, error) {
	var out ChannelPollTarget
	var credentialRef string
	err := database.WithWorkspaceTx(ctx, pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT id, workspace_id, channel_id, credential_ref, poll_offset
			  FROM channel_connection
			 WHERE id = $1 AND provider = $2 AND status = $3 AND archived_at IS NULL`,
			id, provider, channelStatusConnected).
			Scan(&out.ID, &out.WorkspaceID, &out.ChannelID, &credentialRef, &out.PollOffset)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ChannelPollTarget{}, apperrors.ErrNotFound
	}
	if err != nil {
		return ChannelPollTarget{}, fmt.Errorf("capture: reading channel connection %s for its poll: %w", id, err)
	}
	out.CredentialRef = keyvault.Ref(credentialRef)
	return out, nil
}

// AdvanceChannelPollOffsetTx moves the cursor past a batch, inside the caller's
// transaction — the SAME transaction that persisted that batch, which is the
// whole correctness story: asking Telegram for the next offset is what tells it
// to forget the previous updates, so an offset committed ahead of the batch
// would drop messages that exist nowhere else.
//
// It carries THREE predicates, and each rules out a different way of losing
// messages:
//
//   - `id` — the connection this poll was about.
//   - `channel_id = bot` — the bot this poll actually SPOKE TO. update_id is a
//     per-bot sequence, and a ReplaceToken committing during the poll's 25s at
//     Telegram repoints the row at another bot and restarts its cursor. Without
//     this predicate the finishing poll would stamp the outgoing bot's number onto
//     the incoming one, whose own updates are all numbered far below it — so
//     Telegram would be told to forget every message it is holding and the
//     connection would go permanently deaf while still reading `connected`. Zero
//     rows matched is the correct outcome: the raw rows this transaction wrote are
//     keyed on the bot that sent them and stay valid.
//   - `poll_offset < $3` — monotonicity. A retried job holding a cursor an earlier
//     attempt already advanced would otherwise walk it backwards and re-ask for
//     updates this installation has captured.
//
// No audit row: this is not a change to the binding an operator configured, it
// is ingress's own read position. The captured records the batch produced carry
// their own provenance.
func AdvanceChannelPollOffsetTx(ctx context.Context, tx pgx.Tx, id ids.UUID, bot string, offset int64) error {
	if _, err := tx.Exec(ctx, `
		UPDATE channel_connection SET poll_offset = $3
		 WHERE id = $1 AND channel_id = $2 AND poll_offset < $3`, id, bot, offset); err != nil {
		return fmt.Errorf("capture: advancing the poll offset of connection %s: %w", id, err)
	}
	return nil
}

// StopPollingChannelConnection takes a connection out of the poll rotation and
// records why, under the workspace bound to ctx.
//
// status must be one the due-scan does not select, which is what actually stops
// the polling — there is no separate "enabled" flag to clear, and a second flag
// would be a second truth about the same fact. reason is what an operator reads
// to know which of the two happened: a token Telegram now refuses
// (reauth_required, the admin re-pastes it) or a rival consumer holding the
// bot's updates (error, the admin finds and stops it).
//
// The version predicate is deliberately absent: this write is not acting on a
// decision made from a stale snapshot, it is recording what the provider just
// answered about the token the row named a moment ago. If a replacement
// committed in between, the next poll re-reads the new token and the connection
// comes back — refusing here would instead leave a connection nothing ever
// reports on.
func StopPollingChannelConnection(ctx context.Context, pool *pgxpool.Pool, id ids.UUID, status, reason string) error {
	err := database.WithWorkspaceTx(ctx, pool, func(tx pgx.Tx) error {
		if _, err := storekit.LockRow(ctx, tx, "channel_connection", id, storekit.LiveOnly); err != nil {
			return err
		}
		var was string
		var channelID, label string
		if err := tx.QueryRow(ctx,
			`SELECT channel_id, channel_label, status FROM channel_connection WHERE id = $1`, id).
			Scan(&channelID, &label, &was); err != nil {
			return fmt.Errorf("capture: reading channel connection %s before stopping its poll: %w", id, err)
		}
		if was == status {
			// Already recorded — a redelivered poll job re-learning the same
			// provider fact. Writing again would append an audit row saying
			// nothing changed.
			return nil
		}
		if _, err := tx.Exec(ctx,
			`UPDATE channel_connection SET status = $2 WHERE id = $1`, id, status); err != nil {
			return fmt.Errorf("capture: taking channel connection %s out of the poll rotation: %w", id, err)
		}
		return auditLifecycle(ctx, tx, "update", channelConnectionObject, id,
			channelPollHealthImage(channelID, label, was, ""),
			channelPollHealthImage(channelID, label, status, reason))
	})
	if errors.Is(err, apperrors.ErrNotFound) {
		// Archived or gone while the provider was being asked. There is nothing
		// left to stop polling — the due-scan already skips it.
		return nil
	}
	return err
}

// channelPollHealthImage is one side of a poll-health transition's audit trail.
// Both sides carry the same keys so the trail is diffable field for field, and
// `poll_stopped_because` is the only record of WHICH provider fact moved the
// connection — the row itself has no column for it, so an operator reading a
// bare `error` status would otherwise have nothing to act on.
func channelPollHealthImage(channelID, label, status, reason string) map[string]any {
	image := channelAuditImage(channelID, label, status)
	image["poll_stopped_because"] = reason
	return image
}
