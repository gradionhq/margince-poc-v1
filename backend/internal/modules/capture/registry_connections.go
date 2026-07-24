// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package capture

// Standing-connection management for the persisted connectors (the OAuth
// mail connectors, e.g. gmail): the per-user list behind listConnectors, the
// revoke behind disconnectConnector, and the fleet-wide due-poll the
// background sync job drives. The grant + one-sync mechanics live in
// registry.go; this file is only the connection-lifecycle reads/writes.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// ConnectionView is one row of the caller's standing capture connections,
// for the list surface (listConnectors). It carries only status + cursor +
// the watch deadline — never the credential, which lives in the vault behind
// credential_ref.
type ConnectionView struct {
	ID             ids.UUID
	Provider       string
	Status         string     // connected | disconnected | error | reauth_required (capture_connection.status)
	Cursor         []byte     // the incremental-sync watermark (jsonb bytes), or nil
	WatchExpiresAt *time.Time // push/delta subscription renewal deadline, or nil
	Scopes         []string   // the scopes frozen at grant time

	// AccountLabel is the display-only mailbox address the connector reported
	// at connect time (AccountLabeler), or nil when the connector implements
	// no such seam or could not read one from the bundle. Never routed or
	// authorized on.
	AccountLabel *string

	// Sync health from the CAP-DDL-5 sidecar; all nil before the first sync
	// (a connection with no sidecar row is simply due immediately).
	LastSyncedAt   *time.Time
	LastErrorClass *string
	NextSyncDueAt  *time.Time

	// Backfill is the newest CAP-DDL-4 run, nil when never started —
	// the list surface's per-connection summary (contract state "none").
	Backfill *BackfillRun
}

// Connections lists the CALLING human's own standing connections in the
// current workspace (RLS scopes the read to the workspace; user_id scopes
// it to this human — capture is per-user, RC-8).
func (r *Registry) Connections(ctx context.Context) ([]ConnectionView, error) {
	actor, ok := principal.Actor(ctx)
	if !ok || actor.Type != principal.PrincipalHuman {
		return nil, errors.New("capture: only a human lists their connections")
	}
	var out []ConnectionView
	err := database.WithWorkspaceTx(ctx, r.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT c.id, c.provider, c.status, c.sync_cursor, c.watch_expires_at, c.scopes,
			       c.account_label, s.last_synced_at, s.last_error_class, s.next_sync_at
			FROM capture_connection c
			LEFT JOIN capture_sync_state s ON s.connection_id = c.id
			WHERE c.user_id = $1 AND c.archived_at IS NULL
			ORDER BY c.provider`, actor.UserID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var v ConnectionView
			if err := rows.Scan(&v.ID, &v.Provider, &v.Status, &v.Cursor, &v.WatchExpiresAt, &v.Scopes,
				&v.AccountLabel, &v.LastSyncedAt, &v.LastErrorClass, &v.NextSyncDueAt); err != nil {
				return err
			}
			out = append(out, v)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		// A user holds at most a handful of connections, so the per-row
		// latest-run read stays a bounded loop, not an N-problem.
		for i := range out {
			run, err := latestBackfill(ctx, tx, out[i].ID, out[i].Provider)
			if err != nil {
				return err
			}
			out[i].Backfill = run
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("capture: listing connections: %w", err)
	}
	return out, nil
}

// Disconnect disconnects the CALLING human's connection for provider name: the
// status flips to 'disconnected' so the poller stops selecting it
// (DueConnections filters on 'connected'|'error'), the legacy auth column is
// cleared, and the sealed credential is destroyed. Already-captured activities
// are retained; capture simply stops. Idempotent — a missing or
// already-fully-disconnected connection is a no-op.
//
// Ordering closes the leak this method exists to close, and a naive order would
// re-open it. The credential_ref is KEPT through the status flip and cleared
// only AFTER the vault delete succeeds; a delete that fails leaves the row
// 'disconnected' (poller already skips it) with a live ref to retry. Retrying
// keys on 'credential_ref IS NOT NULL', so a partial failure converges rather
// than orphaning the secret. Delete is idempotent (keyvault contract), so the
// retry is safe.
func (r *Registry) Disconnect(ctx context.Context, name string) error {
	actor, ok := principal.Actor(ctx)
	if !ok || actor.Type != principal.PrincipalHuman {
		return errors.New("capture: only a human disconnects a connector")
	}
	ws, ok := principal.WorkspaceID(ctx)
	if !ok {
		return errors.New("capture: disconnect without a workspace in context")
	}

	// Phase 1: stop capture. Flip status and clear the legacy auth bytea (a row
	// whose vault migration never ran holds its credential there — it must not
	// escape erasure through the older column). Keep credential_ref: phase 2
	// needs it, and a crash between phases leaves a recoverable state.
	//
	// The predicate matches a row that is either still LIVE (status <>
	// 'disconnected') or still holds a ref (a prior call's phase 2 failed and
	// this call is retrying it):
	//   - fresh vault row (connected, ref set): live → matches, ref returned.
	//   - legacy row (connected, ref NULL, credential in auth): live →
	//     matches; auth is erased here, ref stays NULL — nothing to
	//     vault-delete, phase 2/3 are a no-op below.
	//   - partial-failure retry (already disconnected, ref still set):
	//     ref IS NOT NULL → matches, retries the vault delete.
	//   - fully done (disconnected, ref NULL): matches neither arm →
	//     ErrNoRows → idempotent no-op.
	// A credential_ref-only predicate (the prior version) misses the legacy
	// case entirely: credential_ref IS NULL there even though a live secret
	// sits in auth, so the row would never match and disconnect would be a
	// silent no-op that leaves the row connected and the credential intact.
	var ref *string
	if err := database.WithWorkspaceTx(ctx, r.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			UPDATE capture_connection
			   SET status = 'disconnected', auth = NULL
			 WHERE user_id = $1 AND provider = $2
			   AND (status <> 'disconnected' OR credential_ref IS NOT NULL)
			RETURNING credential_ref`,
			actor.UserID, name,
		).Scan(&ref)
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// No row to flip: never connected, or already fully disconnected
			// (disconnected + no ref) on a prior call. Idempotent no-op.
			return nil
		}
		return err
	}
	if ref == nil {
		// A legacy row: the credential lived in auth (just cleared above),
		// never in the vault. There is no ref to delete or null.
		return nil
	}

	// Phase 2: destroy the secret, THEN drop the ref. If the delete fails the
	// error surfaces and the ref stays, so the next call retries phase 2. A
	// ref with no vault configured is a wiring fault, not something to skip
	// past — silently continuing to phase 3 would null the only pointer to a
	// secret nobody deleted.
	if r.vault == nil {
		return errors.New("capture: connection carries a credential ref but no keyvault is configured to delete it")
	}
	if err := r.vault.Delete(ctx, ids.From[ids.WorkspaceKind](ws), keyvault.Ref(*ref)); err != nil {
		return fmt.Errorf("capture: deleting the disconnected credential: %w", err)
	}
	// Phase 3: clear only the ref THIS call resolved and deleted
	// (credential_ref = $3). Without that guard, a reconnect landing between
	// phase 1 and here — Connect writes a NEW ref onto the same row — would
	// have its live, still-vaulted ref nulled out from under it: a
	// 'connected' row with no credential, and the fresh secret orphaned.
	return database.WithWorkspaceTx(ctx, r.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			UPDATE capture_connection SET credential_ref = NULL
			 WHERE user_id = $1 AND provider = $2 AND credential_ref = $3`,
			actor.UserID, name, *ref)
		return err
	})
}

// DueConnection names one connected connection to sync, with the workspace it
// lives in — the poller sets that workspace's GUC before calling SyncOnce.
type DueConnection struct {
	Workspace ids.WorkspaceID
	ID        ids.UUID
}

// DueConnections lists every DUE connection for provider name across the
// whole fleet, so the background dispatcher can enqueue one sync per
// connection. Due means: live, in a syncable status, and past its
// next_sync_at (ADR-0063 — the sidecar's backoff/pacing gate; a connection
// with no sidecar row yet is due immediately). Status 'error' stays in the
// scan — degraded connections are probed on their daily cadence, never
// tombstoned; only 'disconnected' and 'reauth_required' park a row.
// capture_connection is RLS-scoped, so this walks each workspace under its
// own GUC. One workspace's failure does not starve the rest.
func (r *Registry) DueConnections(ctx context.Context, name string) ([]DueConnection, error) {
	return r.collectDue(ctx, func(ctx context.Context, tx pgx.Tx) ([]ids.UUID, error) {
		rows, err := tx.Query(ctx, `
			SELECT c.id FROM capture_connection c
			LEFT JOIN capture_sync_state s ON s.connection_id = c.id
			WHERE c.provider = $1 AND c.status IN ('connected','error') AND c.archived_at IS NULL
			  AND COALESCE(s.next_sync_at, now()) <= now()`, name)
		if err != nil {
			return nil, err
		}
		return pgx.CollectRows(rows, pgx.RowTo[ids.UUID])
	})
}

// collectDue is the RLS fleet-walk the poll (DueConnections) and the watch scan
// (DueWatches) share: it enumerates every live workspace, enters each one's own
// GUC, and appends the connection ids the per-workspace selector returns, tagged
// with their workspace. Per-workspace errors are joined so one workspace's
// failure never starves the rest of the fleet.
func (r *Registry) collectDue(ctx context.Context, selector func(ctx context.Context, tx pgx.Tx) ([]ids.UUID, error)) ([]DueConnection, error) {
	// rls-exempt: fleet enumeration — the workspace table is not workspace-scoped; this reads every tenant before entering each workspace's own GUC.
	rows, err := r.pool.Query(ctx, `SELECT id FROM workspace WHERE archived_at IS NULL ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("capture: listing workspaces for the fleet walk: %w", err)
	}
	workspaces, err := pgx.CollectRows(rows, pgx.RowTo[ids.UUID])
	if err != nil {
		return nil, err
	}
	var due []DueConnection
	var errs error
	for _, wsID := range workspaces {
		wsCtx := principal.WithWorkspaceID(ctx, wsID)
		ws := ids.From[ids.WorkspaceKind](wsID)
		err := database.WithWorkspaceTx(wsCtx, r.pool, func(tx pgx.Tx) error {
			selected, err := selector(wsCtx, tx)
			if err != nil {
				return err
			}
			for _, id := range selected {
				due = append(due, DueConnection{Workspace: ws, ID: id})
			}
			return nil
		})
		if err != nil {
			errs = errors.Join(errs, fmt.Errorf("capture: fleet walk in workspace %s: %w", wsID, err))
		}
	}
	return due, errs
}
