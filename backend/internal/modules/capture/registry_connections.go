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

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// ConnectionView is one row of the caller's standing capture connections,
// for the list surface (listConnectors). It carries only status + cursor —
// never the credential, which lives in the vault behind credential_ref.
type ConnectionView struct {
	ID        ids.UUID
	Connector string
	Status    string   // 'active' | 'revoked' | 'error' (connector_connection.status)
	Cursor    []byte   // opaque incremental-sync watermark, or nil
	Scopes    []string // the scopes frozen at grant time
}

// Connections lists the CALLING human's own standing connections in the
// current workspace (RLS scopes the read to the workspace; granted_by scopes
// it to this human — capture is per-user, RC-8).
func (r *Registry) Connections(ctx context.Context) ([]ConnectionView, error) {
	actor, ok := principal.Actor(ctx)
	if !ok || actor.Type != principal.PrincipalHuman {
		return nil, errors.New("capture: only a human lists their connections")
	}
	var out []ConnectionView
	err := database.WithWorkspaceTx(ctx, r.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id, connector, status, cursor, scopes FROM connector_connection
			WHERE granted_by = $1
			ORDER BY connector`, actor.UserID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var v ConnectionView
			if err := rows.Scan(&v.ID, &v.Connector, &v.Status, &v.Cursor, &v.Scopes); err != nil {
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

// Disconnect revokes the CALLING human's connection for connector name in the
// current workspace: it flips status to 'revoked' so the poller stops
// selecting it (DueConnections filters on 'active'). Idempotent — a missing
// or already-revoked connection is a no-op, not an error. Already-captured
// activities are retained; capture simply stops. The stored credential is
// left for a follow-up vault sweep (revocation upstream is the real cut-off).
func (r *Registry) Disconnect(ctx context.Context, name string) error {
	actor, ok := principal.Actor(ctx)
	if !ok || actor.Type != principal.PrincipalHuman {
		return errors.New("capture: only a human disconnects a connector")
	}
	return database.WithWorkspaceTx(ctx, r.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			UPDATE connector_connection SET status = 'revoked'
			WHERE granted_by = $1 AND connector = $2 AND status <> 'revoked'`,
			actor.UserID, name)
		return err
	})
}

// DueConnection names one active connection to sync, with the workspace it
// lives in — the poller sets that workspace's GUC before calling SyncOnce.
type DueConnection struct {
	Workspace ids.WorkspaceID
	ID        ids.UUID
}

// DueConnections lists every active connection for connector name across the
// whole fleet, so the background poller can drive one SyncOnce per
// connection. connector_connection is RLS-scoped, so this walks each
// workspace under its own GUC — the same fleet-walk shape as
// BackfillCredentials. One workspace's failure does not starve the rest.
func (r *Registry) DueConnections(ctx context.Context, name string) ([]DueConnection, error) {
	// rls-exempt: fleet enumeration — the workspace table is not workspace-scoped; this reads every tenant before entering each workspace's own GUC.
	rows, err := r.pool.Query(ctx, `SELECT id FROM workspace WHERE archived_at IS NULL ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("capture: listing workspaces for the %s poll: %w", name, err)
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
			rows, err := tx.Query(wsCtx, `
				SELECT id FROM connector_connection
				WHERE connector = $1 AND status = 'active'`, name)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var id ids.UUID
				if err := rows.Scan(&id); err != nil {
					return err
				}
				due = append(due, DueConnection{Workspace: ws, ID: id})
			}
			return rows.Err()
		})
		if err != nil {
			errs = errors.Join(errs, fmt.Errorf("capture: listing %s connections in workspace %s: %w", name, wsID, err))
		}
	}
	return due, errs
}
