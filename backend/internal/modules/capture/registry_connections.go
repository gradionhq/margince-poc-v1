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
			SELECT id, provider, status, sync_cursor, watch_expires_at, scopes FROM capture_connection
			WHERE user_id = $1 AND archived_at IS NULL
			ORDER BY provider`, actor.UserID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var v ConnectionView
			if err := rows.Scan(&v.ID, &v.Provider, &v.Status, &v.Cursor, &v.WatchExpiresAt, &v.Scopes); err != nil {
				return err
			}
			out = append(out, v)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("capture: listing connections: %w", err)
	}
	return out, nil
}

// Disconnect disconnects the CALLING human's connection for provider name in
// the current workspace: it flips status to 'disconnected' so the poller stops
// selecting it (DueConnections filters on 'connected'). Idempotent — a missing
// or already-disconnected connection is a no-op, not an error. Already-captured
// activities are retained; capture simply stops. The stored credential is
// left for a follow-up vault sweep (revocation upstream is the real cut-off).
func (r *Registry) Disconnect(ctx context.Context, name string) error {
	actor, ok := principal.Actor(ctx)
	if !ok || actor.Type != principal.PrincipalHuman {
		return errors.New("capture: only a human disconnects a connector")
	}
	return database.WithWorkspaceTx(ctx, r.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			UPDATE capture_connection SET status = 'disconnected'
			WHERE user_id = $1 AND provider = $2 AND status <> 'disconnected'`,
			actor.UserID, name)
		return err
	})
}

// DueConnection names one connected connection to sync, with the workspace it
// lives in — the poller sets that workspace's GUC before calling SyncOnce.
type DueConnection struct {
	Workspace ids.WorkspaceID
	ID        ids.UUID
}

// DueConnections lists every connected connection for provider name across the
// whole fleet, so the background poller can drive one SyncOnce per
// connection. capture_connection is RLS-scoped, so this walks each
// workspace under its own GUC. One workspace's failure does not starve the rest.
func (r *Registry) DueConnections(ctx context.Context, name string) ([]DueConnection, error) {
	return r.collectDue(ctx, func(ctx context.Context, tx pgx.Tx) ([]ids.UUID, error) {
		rows, err := tx.Query(ctx, `
			SELECT id FROM capture_connection
			WHERE provider = $1 AND status = 'connected' AND archived_at IS NULL`, name)
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
