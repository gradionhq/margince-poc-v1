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
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// ErrNoConnection marks a user with no live connection to a provider. It is a
// fact to report ("connect a mailbox"), never a transient failure to retry.
var ErrNoConnection = errors.New("capture: no live connection for this user and provider")

// ErrConnectorCannotSend marks a live connection whose connector implements
// capture only. No retry turns a capture-only connector into a transmitting
// one, so a caller is told the fact rather than left to read an outage into
// it.
var ErrConnectorCannotSend = errors.New("capture: this connector cannot transmit messages")

// ErrConnectorNotConfigured marks a provider this process role did not compile
// in — the deployment configured no OAuth app for it, so the registry holds no
// connector to reach it through.
//
// It is a FACT about the deployment, not an outage, and it is separated from
// the failures around it for exactly that reason: read as transient it looks
// identical to a provider blip, and a caller that keeps retrying leaves a
// message queued against an integration that does not exist here until its
// ladder runs out and the row sits pending forever. Named, the caller can stop
// and say why.
var ErrConnectorNotConfigured = errors.New("capture: no connector for this provider is configured on this process role")

// sendableConnection is the row predicate BOTH send-side lookups select on,
// spelled once so the pre-flight and the transmission can never disagree about
// which connections count as live.
//
// The status filter is SyncOnce's, and for the same reason: 'error' is a
// degraded connection, not a dead one, and reading it as "no mailbox" would
// let a transient sync failure permanently park mail. Only 'disconnected' and
// 'reauth_required' mean there is nothing to send through.
const sendableConnection = `
	 WHERE user_id = $1 AND provider = $2
	   AND status IN ('connected','error') AND archived_at IS NULL`

// GrantedScopesFor reports the scopes the PROVIDER says one user's live
// connection holds — and resolves no credential.
//
// That omission is the point. The request-time pre-flight needs only the
// grant, so unsealing the mailbox's secret to answer a question about scopes
// would spend a vault round trip on every send request and turn a keyvault
// blip into a refusal the user cannot act on. ErrNoConnection is the only fact
// it reports; every other error is a failure to get an answer.
func (r *Registry) GrantedScopesFor(ctx context.Context, userID ids.UserID, provider string) ([]string, error) {
	var granted []string
	err := database.WithWorkspaceTx(ctx, r.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT provider_scopes FROM capture_connection`+sendableConnection,
			userID, provider).Scan(&granted)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNoConnection
	}
	if err != nil {
		return nil, fmt.Errorf("capture: reading the connection's granted scopes: %w", err)
	}
	return granted, nil
}

// SenderFor resolves the transmitting connection for one user and provider:
// the connector's EmailSender seam, its unsealed credential, and the scopes
// the PROVIDER says the grant holds. It is the send path's entry point, which
// knows a user and a provider rather than a connection id.
//
// Everything this cannot answer is returned as the failure it is. Only the two
// sentinels above are facts about the deployment; a vault outage or a database
// timeout must never be mistaken for one, because the caller's response to a
// fact is to stop trying.
//
//nolint:ireturn // returns the optional connector.EmailSender seam by design (the same posture Registry.connector takes for connector.Connector)
func (r *Registry) SenderFor(ctx context.Context, userID ids.UserID, provider string) (connector.EmailSender, connector.Auth, []string, error) {
	var (
		credentialRef *string
		authBytes     []byte
		granted       []string
	)
	err := database.WithWorkspaceTx(ctx, r.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT credential_ref, auth, provider_scopes FROM capture_connection`+sendableConnection,
			userID, provider).Scan(&credentialRef, &authBytes, &granted)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, nil, ErrNoConnection
	}
	if err != nil {
		return nil, nil, nil, fmt.Errorf("capture: resolving the sending connection: %w", err)
	}
	c, err := r.connector(provider)
	if err != nil {
		// The registry's only failure here is a provider this role did not
		// compile in, and the send path must be able to tell that from an
		// outage — see ErrConnectorNotConfigured.
		return nil, nil, nil, fmt.Errorf("%w: %w", ErrConnectorNotConfigured, err)
	}
	// Two-value form: EmailSender is optional (connector.go), so a capture-only
	// connector is reported rather than silently treated as absent.
	sender, sends := c.(connector.EmailSender)
	if !sends {
		return nil, nil, nil, fmt.Errorf("capture: connector %q: %w", provider, ErrConnectorCannotSend)
	}
	auth, err := r.resolveCredential(ctx, credentialRef, authBytes)
	if err != nil {
		return nil, nil, nil, err
	}
	return sender, auth, granted, nil
}

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

	// ProviderScopes is what the PROVIDER granted, in the provider's own
	// vocabulary — the fact the contract's CaptureConnection.scopes names, and
	// the one a human can act on. Nil for a connection whose connector cannot
	// report it, or one made before the grant was recorded: absence, not an
	// empty grant. The internal permission scopes stay in the row's own
	// `scopes` column and are not a list-surface concern.
	ProviderScopes []string

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
			SELECT c.id, c.provider, c.status, c.sync_cursor, c.watch_expires_at, c.provider_scopes,
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
			if err := rows.Scan(&v.ID, &v.Provider, &v.Status, &v.Cursor, &v.WatchExpiresAt, &v.ProviderScopes,
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
			run, err := latestBackfill(ctx, tx, out[i].ID)
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
// re-open it. Phase 1 (withdrawConnection) keeps the credential_ref through the
// status flip so a crash between the phases leaves a recoverable state: the
// retry predicate keys on 'credential_ref IS NOT NULL', so a half-finished
// disconnect converges rather than orphaning the secret. Delete is idempotent
// (keyvault contract), so the retry is safe.
func (r *Registry) Disconnect(ctx context.Context, name string) error {
	actor, ok := principal.Actor(ctx)
	if !ok || actor.Type != principal.PrincipalHuman {
		return errors.New("capture: only a human disconnects a connector")
	}
	ws, ok := principal.WorkspaceID(ctx)
	if !ok {
		return errors.New("capture: disconnect without a workspace in context")
	}

	// Phase 1: stop capture.
	ref, err := r.withdrawConnection(ctx, actor.UserID, name)
	if err != nil {
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

	// Phase 2: destroy the secret. A ref with no vault configured is a wiring
	// fault, not something to skip past — continuing to phase 3 would null the
	// only pointer to a secret nobody deleted.
	if r.vault == nil {
		return errors.New("capture: connection carries a credential ref but no keyvault is configured to delete it")
	}
	// The delete runs after phase 1 committed, so it must outlive the request:
	// a client that hangs up the moment it has its 204 must not be the reason a
	// revoked credential stays decryptable. It cannot fail the caller either —
	// the disconnect already happened, and reporting an error for a committed
	// withdrawal would invite a retry that finds nothing left to do.
	keyvault.DeleteDetached(ctx, r.vault, slog.Default(), ws, keyvault.Ref(*ref), "disconnect")
	// Phase 3: clear only the ref THIS call resolved and deleted
	// (credential_ref = $3). Without that guard, a reconnect landing between
	// phase 1 and here — Connect writes a NEW ref onto the same row — would
	// have its live, still-vaulted ref nulled out from under it: a
	// 'connected' row with no credential, and the fresh secret orphaned.
	//
	// It is detached for the same reason phase 2 is, on the same post-commit
	// cleanup budget: the secret is already destroyed, so a caller that hung up
	// must not leave the row naming a blob that no longer exists.
	refCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), keyvault.CleanupTimeout)
	defer cancel()
	return database.WithWorkspaceTx(refCtx, r.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(refCtx, `
			UPDATE capture_connection SET credential_ref = NULL
			 WHERE user_id = $1 AND provider = $2 AND credential_ref = $3`,
			actor.UserID, name, *ref)
		return err
	})
}

// withdrawConnection is Disconnect's phase 1: flip the caller's connection for
// provider name to 'disconnected', clear the legacy auth bytea (a row whose
// vault migration never ran holds its credential there — it must not escape
// erasure through the older column), and audit the withdrawal. It returns the
// credential_ref the row still names, which the caller destroys next; nil means
// there is no vaulted secret to destroy. pgx.ErrNoRows means nothing matched.
//
// The audit row and the generation bump belong to the row that was still LIVE:
// they record the human's act of withdrawing, which happens exactly once no
// matter how many calls it takes to finish destroying the credential.
//
// credential_ref is deliberately KEPT here: phase 2 needs it, and a crash
// between the phases must leave a recoverable state.
//
// The predicate matches a row that is either still LIVE (status <>
// 'disconnected') or still holds a ref (a prior call stopped between phases):
//   - fresh vault row (connected, ref set): live → matches, ref returned.
//   - legacy row (connected, ref NULL, credential in auth): live → matches;
//     auth is erased here, ref stays NULL — nothing to vault-delete.
//   - half-finished retry (already disconnected, ref still set):
//     ref IS NOT NULL → matches, retries the vault delete, audits nothing.
//   - fully done (disconnected, ref NULL): matches neither arm → ErrNoRows →
//     idempotent no-op.
//
// A credential_ref-only predicate misses the legacy case entirely:
// credential_ref IS NULL there even though a live secret sits in auth, so the
// row would never match and disconnect would be a silent no-op that leaves the
// row connected and the credential intact.
//
// The row is read FOR UPDATE before the flip so the audit before-image is the
// status the human is actually withdrawing from, and so a concurrent reconnect
// serializes behind this transaction rather than racing it.
func (r *Registry) withdrawConnection(ctx context.Context, userID ids.UUID, name string) (*string, error) {
	var ref *string
	err := database.WithWorkspaceTx(ctx, r.pool, func(tx pgx.Tx) error {
		var connID ids.UUID
		var priorStatus string
		var priorLabel *string
		if err := tx.QueryRow(ctx, `
			SELECT id, status, account_label FROM capture_connection
			 WHERE user_id = $1 AND provider = $2
			   AND (status <> 'disconnected' OR credential_ref IS NOT NULL)
			 FOR UPDATE`,
			userID, name).Scan(&connID, &priorStatus, &priorLabel); err != nil {
			return err
		}
		// A row already 'disconnected' is a retry re-driving the cleanup the
		// previous call left unfinished — the withdrawal itself happened then, and
		// what follows here is only what phases 2 and 3 still owe.
		alreadyWithdrawn := priorStatus == "disconnected"
		// The generation bump fences every cycle already out at the provider: a
		// sync or backfill page that reads a connected row, spends minutes
		// fetching, and comes back to commit must find that its generation is
		// gone rather than write a watermark onto a withdrawn grant. The retry
		// has no cycle left to fence — the first withdrawal fenced them — and a
		// second bump would only invalidate generations nothing holds.
		if err := tx.QueryRow(ctx, `
			UPDATE capture_connection
			   SET status = 'disconnected', auth = NULL,
			       generation = generation + CASE WHEN $2 THEN 1 ELSE 0 END
			 WHERE id = $1
			RETURNING credential_ref`, connID, !alreadyWithdrawn).Scan(&ref); err != nil {
			return err
		}
		if alreadyWithdrawn {
			// Auditing here would record a fresh deliberate withdrawal whose
			// before- and after-images are identical, once per retry: a trail that
			// reads as repeated human acts nobody performed is worse than no trail.
			return nil
		}
		// Withdrawing a connector is a human's deliberate act over their own
		// mailbox and is attributed like any other record mutation.
		return auditLifecycle(ctx, tx, "archive", captureConnectionObject, connID,
			connectionAuditImage(name, priorStatus, priorLabel),
			connectionAuditImage(name, "disconnected", priorLabel))
	})
	if err != nil {
		return nil, err
	}
	return ref, nil
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
