// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package capture

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/authz"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// Registry holds the compiled-in connector set and owns the two
// authority rules of the capture path: the grant-time scope
// intersection (a connector's declared scopes ⊆ the granting human's)
// and the run-time connector principal (built from the granting
// human's LIVE authority — a demoted human instantly narrows every
// connector they granted, exactly like passports).
type Registry struct {
	mu         sync.RWMutex
	connectors map[string]connector.Connector
	pool       *pgxpool.Pool
	sink       *Sink
	authority  authz.Resolver
	// vault seals and resolves a connection's credential bundle. The row
	// carries an opaque credential_ref, never the credential bytes; the vault
	// is the custodian. May be nil for a role composed before WithKeyvault
	// wires one: Connect then refuses loudly (it must seal), and SyncOnce
	// refuses only for a row whose credential lives in the vault — a
	// not-yet-backfilled legacy row still resolves from its auth column with
	// no vault.
	vault keyvault.Vault

	// The scheduling state machine's knobs (ADR-0063): now is injected so
	// the backoff/pacing arithmetic is testable; syncInterval paces a
	// healthy connection (next_sync_at = success + interval).
	now          func() time.Time
	syncInterval time.Duration
}

// defaultSyncInterval paces a healthy connection between syncs; the push
// webhook (when live) makes this the safety net, not the latency floor.
const defaultSyncInterval = 2 * time.Minute

// NewRegistry builds the connector registry over the pool, the capture Sink,
// the live-authority resolver, and the keyvault that seals/resolves each
// connection's credential. vault may be nil for a role composed before its
// custodian is wired (WithKeyvault rebuilds the registry once it is).
func NewRegistry(pool *pgxpool.Pool, sink *Sink, authority authz.Resolver, vault keyvault.Vault) *Registry {
	return &Registry{
		connectors:   map[string]connector.Connector{},
		pool:         pool,
		sink:         sink,
		authority:    authority,
		vault:        vault,
		now:          time.Now,
		syncInterval: defaultSyncInterval,
	}
}

// WithSyncInterval overrides the healthy-connection pacing (the worker's
// --gmail-sync-interval flag lands here).
func (r *Registry) WithSyncInterval(d time.Duration) *Registry {
	if d > 0 {
		r.syncInterval = d
	}
	return r
}

// Register adds one connector at composition time.
func (r *Registry) Register(c connector.Connector) {
	desc := c.Descriptor()
	if desc.Name == "" {
		//craft:ignore panic-in-domain composition-time registration assertion — fires only while cmd wiring runs, never on a request path
		panic("capture: registering a connector with no name")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.connectors[desc.Name]; dup {
		//craft:ignore panic-in-domain composition-time registration assertion — fires only while cmd wiring runs, never on a request path
		panic(fmt.Sprintf("capture: duplicate connector %s", desc.Name))
	}
	r.connectors[desc.Name] = c
}

// Connectors lists the registered surface, stably ordered.
func (r *Registry) Connectors() []connector.Descriptor {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]connector.Descriptor, 0, len(r.connectors))
	for _, c := range r.connectors {
		out = append(out, c.Descriptor())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Connect grants one connector under the CALLING human's authority.
// The scope-intersection guard runs here: a connector demanding scopes
// the granting human does not hold is refused at grant time, not
// discovered at 3am mid-sync.
//
// note: the returned id (and the connectionID threaded through SyncOnce and
// the sync-state recording) names a capture_connection row, which the kernel
// does not model as a first-class entity — no kind exists for it, so it stays
// ids.UUID rather than inventing one.
func (r *Registry) Connect(ctx context.Context, name string, auth connector.Auth) (ids.UUID, error) {
	c, err := r.connector(name)
	if err != nil {
		return ids.Nil, err
	}
	actor, ok := principal.Actor(ctx)
	if !ok || actor.Type != principal.PrincipalHuman {
		return ids.Nil, errors.New("capture: only a human grants a connector")
	}
	for _, scope := range c.Descriptor().Scopes {
		if !actor.Scopes.Has(scope) {
			return ids.Nil, fmt.Errorf("capture: connector %s needs scope %q the granting human does not hold: %w",
				name, scope, apperrors.ErrScopeExceeded)
		}
	}
	scopes := make([]string, 0, len(c.Descriptor().Scopes))
	for _, s := range c.Descriptor().Scopes {
		scopes = append(scopes, string(s))
	}
	ws, ok := principal.WorkspaceID(ctx)
	if !ok {
		return ids.Nil, errors.New("capture: connector grant outside workspace context")
	}
	if r.vault == nil {
		return ids.Nil, errors.New("capture: no keyvault configured — a connector credential cannot be sealed")
	}
	// Put-then-commit (like blobstore): seal the credential in the vault
	// first, then commit the row that names it. A rolled-back row leaves an
	// orphan secret (encrypted and unreferenced — benign), never a row
	// promising a credential that is not there. The row stores the opaque ref;
	// the bytes never touch it.
	ref, err := r.vault.Put(ctx, ids.From[ids.WorkspaceKind](ws), []byte(auth))
	if err != nil {
		return ids.Nil, fmt.Errorf("capture: sealing connector credential: %w", err)
	}
	// Display-only; a connector that cannot name its account simply does not
	// implement the seam. This must not fail the connect — a missing label is a
	// blank line in the UI, not a lost connection.
	var accountLabel *string
	if labeler, ok := c.(connector.AccountLabeler); ok {
		if label, err := labeler.AccountLabel(auth); err == nil && label != "" {
			accountLabel = &label
		} else if err != nil {
			slog.WarnContext(ctx, "capture: connector could not name its account", "provider", name, "err", err)
		}
	}
	var id ids.UUID
	err = database.WithWorkspaceTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
			INSERT INTO capture_connection (workspace_id, provider, user_id, scopes, credential_ref, status, account_label)
			VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid, $1, $2, $3, $4, 'connected', $5)
			ON CONFLICT (workspace_id, user_id, provider)
			DO UPDATE SET credential_ref = EXCLUDED.credential_ref, auth = NULL, status = 'connected', archived_at = NULL,
			              account_label = EXCLUDED.account_label
			RETURNING id`,
			name, actor.UserID, scopes, string(ref), accountLabel).Scan(&id); err != nil {
			return err
		}
		// A (re)connect starts the scheduling ladder clean: a row parked by
		// reauth_required or degraded by backoff is due immediately with a
		// fresh credential (ADR-0063).
		_, err = tx.Exec(ctx, `
			UPDATE capture_sync_state
			SET next_sync_at = now(), consecutive_failures = 0, last_error_class = NULL
			WHERE connection_id = $1`, id)
		return err
	})
	if err != nil {
		return ids.Nil, fmt.Errorf("capture: storing connection: %w", err)
	}
	return id, nil
}

// SyncOnce runs one incremental sync for a connection: builds the
// connector principal from the granting human's live authority, hands
// the connector the sink, and advances the stored cursor only when the
// sync succeeded end to end.
func (r *Registry) SyncOnce(ctx context.Context, connectionID ids.UUID) error {
	var (
		name          string
		grantedBy     ids.UserID
		credentialRef *string
		authBytes     []byte
		cursor        []byte
	)
	err := database.WithWorkspaceTx(ctx, r.pool, func(tx pgx.Tx) error {
		// 'error' is syncable by design (ADR-0063): the daily probe of a
		// degraded connection runs through this same path, and its success
		// is what flips the row back to connected. Only 'disconnected' and
		// 'reauth_required' park a connection.
		return tx.QueryRow(ctx, `
			SELECT provider, user_id, credential_ref, auth, sync_cursor FROM capture_connection
			WHERE id = $1 AND status IN ('connected','error')`, connectionID).
			Scan(&name, &grantedBy, &credentialRef, &authBytes, &cursor)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("capture: connection %s: %w", connectionID, apperrors.ErrNotFound)
	}
	if err != nil {
		return err
	}
	c, err := r.connector(name)
	if err != nil {
		return err
	}
	// The connector principal is built before credential resolution so every
	// failure past this point records into the scheduling state under an
	// actor-bearing context (the sidecar's system_log line needs one).
	runCtx, err := r.connectorContext(ctx, name, grantedBy)
	if err != nil {
		return err
	}
	auth, err := r.resolveCredential(ctx, credentialRef, authBytes)
	if err != nil {
		if recErr := r.recordSyncFailure(runCtx, connectionID, err); recErr != nil {
			return errors.Join(err, recErr)
		}
		return err
	}

	next, syncErr := c.Sync(runCtx, auth, connector.Cursor(cursor), r.sink)
	if syncErr != nil {
		// A transient failure never kills the connection (ADR-0063): the
		// state machine classifies, backs off, degrades to a daily probe at
		// worst — and auth parks the row for its human.
		if recErr := r.recordSyncFailure(runCtx, connectionID, syncErr); recErr != nil {
			return errors.Join(syncErr, recErr)
		}
		return syncErr
	}
	err = database.WithWorkspaceTx(ctx, r.pool, func(tx pgx.Tx) error {
		// sync_cursor is jsonb; the connector's watermark is already JSON. A
		// connector that yields no cursor writes NULL, never an empty jsonb.
		var cur []byte
		if len(next) > 0 {
			cur = []byte(next)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE capture_connection SET sync_cursor = $2
			WHERE id = $1`, connectionID, cur); err != nil {
			return err
		}
		return r.seedInternalDomain(ctx, tx, cur)
	})
	if err != nil {
		return err
	}
	return r.recordSyncSuccess(ctx, connectionID)
}

// seedInternalDomain records the synced mailbox's own domain as a workspace
// email domain (ADR-0063's colleagues gate) — the connector wrote its
// mailbox identity into the cursor, and mail among addresses on this domain
// must never auto-create customers. Free-mail domains never seed: a
// gmail.com mailbox does not make gmail.com internal.
func (r *Registry) seedInternalDomain(ctx context.Context, tx pgx.Tx, cursor []byte) error {
	var identity struct {
		Email string `json:"email"`
	}
	if len(cursor) == 0 {
		return nil
	}
	if err := json.Unmarshal(cursor, &identity); err != nil {
		// A cursor that is not a JSON identity object simply seeds nothing —
		// the gate stays admin-fed for that connector, never a sync fault.
		return nil //nolint:nilerr // deliberate: an identity-less cursor is a no-op, not an error
	}
	_, domain, found := strings.Cut(strings.ToLower(strings.TrimSpace(identity.Email)), "@")
	if !found || domain == "" {
		return nil
	}
	if r.sink != nil && r.sink.freemail != nil && r.sink.freemail.IsFreemail(domain) {
		return nil
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO workspace_email_domain (workspace_id, domain)
		VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid, $1)
		ON CONFLICT DO NOTHING`, domain); err != nil {
		return fmt.Errorf("capture: seeding workspace email domain: %w", err)
	}
	return nil
}

// resolveCredential turns a stored connection's credential into the opaque
// Auth the connector expects. It PREFERS the vault ref; the legacy auth bytea
// column is read only for a row not yet backfilled onto the vault (during the
// additive transition, before that column is dropped).
func (r *Registry) resolveCredential(ctx context.Context, credentialRef *string, authBytes []byte) (connector.Auth, error) {
	if credentialRef != nil && *credentialRef != "" {
		if r.vault == nil {
			return nil, errors.New("capture: connection carries a credential ref but no keyvault is configured to resolve it")
		}
		ws, ok := principal.WorkspaceID(ctx)
		if !ok {
			return nil, errors.New("capture: credential resolution outside workspace context")
		}
		secret, err := r.vault.Get(ctx, ids.From[ids.WorkspaceKind](ws), keyvault.Ref(*credentialRef))
		if err != nil {
			return nil, fmt.Errorf("capture: resolving connector credential: %w", err)
		}
		return connector.Auth(secret), nil
	}
	// A row not yet backfilled: the credential still lives in the column.
	return connector.Auth(authBytes), nil
}

// BackfillCredentials migrates every legacy capture_connection row whose
// credential still lives in the auth bytea column onto the vault: it seals the
// bytes, records the credential_ref, and clears auth. It is idempotent — a row
// that already carries a ref is skipped — so a re-run or a crash-retry is
// safe, which is what lets it run on every boot. It walks every live workspace
// under that workspace's own GUC, since capture_connection is RLS-scoped.
// One workspace's failure must not starve the rest of the fleet (the same
// invariant retention and the close-date sweep hold): the walk continues past
// a failing workspace and returns the count migrated plus the joined errors.
func (r *Registry) BackfillCredentials(ctx context.Context) (int, error) {
	if r.vault == nil {
		return 0, errors.New("capture: cannot backfill connector credentials without a keyvault")
	}
	// rls-exempt: fleet enumeration — the workspace table is not workspace-scoped; this reads every tenant before entering each workspace's own GUC.
	rows, err := r.pool.Query(ctx, `SELECT id FROM workspace WHERE archived_at IS NULL ORDER BY created_at`)
	if err != nil {
		return 0, fmt.Errorf("capture: listing workspaces for credential backfill: %w", err)
	}
	workspaces, err := pgx.CollectRows(rows, pgx.RowTo[ids.UUID])
	if err != nil {
		return 0, err
	}
	total := 0
	var errs error
	for _, wsID := range workspaces {
		// The backfill's UPDATE runs under the workspace GUC only (a raw
		// relocation, not an audited domain write), so no actor/correlation
		// context is set — nothing here reads it.
		wsCtx := principal.WithWorkspaceID(ctx, wsID)
		migrated, err := r.backfillWorkspace(wsCtx, ids.From[ids.WorkspaceKind](wsID))
		if err != nil {
			errs = errors.Join(errs, fmt.Errorf("capture: backfilling workspace %s: %w", wsID, err))
			continue
		}
		total += migrated
	}
	return total, errs
}

// backfillWorkspace migrates one workspace's legacy rows. Each secret is
// sealed OUTSIDE the update tx (put-then-commit); the update then claims the
// row only if it still has no ref, so a concurrent backfill (two worker pods
// at boot) cannot double-migrate — the loser's sealed secret is a harmless
// orphan, never a corrupted row.
func (r *Registry) backfillWorkspace(ctx context.Context, ws ids.WorkspaceID) (int, error) {
	type legacyRow struct {
		id   ids.UUID
		auth []byte
	}
	var pending []legacyRow
	err := database.WithWorkspaceTx(ctx, r.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id, auth FROM capture_connection
			WHERE credential_ref IS NULL AND auth IS NOT NULL`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var l legacyRow
			if err := rows.Scan(&l.id, &l.auth); err != nil {
				return err
			}
			pending = append(pending, l)
		}
		return rows.Err()
	})
	if err != nil {
		return 0, err
	}

	migrated := 0
	for _, l := range pending {
		ref, err := r.vault.Put(ctx, ws, l.auth)
		if err != nil {
			return migrated, err
		}
		var claimed bool
		err = database.WithWorkspaceTx(ctx, r.pool, func(tx pgx.Tx) error {
			ct, err := tx.Exec(ctx, `
				UPDATE capture_connection SET credential_ref = $2, auth = NULL
				WHERE id = $1 AND credential_ref IS NULL`, l.id, string(ref))
			if err != nil {
				return err
			}
			claimed = ct.RowsAffected() == 1
			return nil
		})
		if err != nil {
			return migrated, err
		}
		if claimed {
			migrated++
		}
	}
	return migrated, nil
}

// connectorContext builds the acting principal: connector identity,
// the granting human's LIVE permissions and teams (connector ≤ human as
// a runtime property), full seat (capture is a write path by nature —
// the human's ability to grant it is what the scope check consumed).
func (r *Registry) connectorContext(ctx context.Context, name string, grantedBy ids.UserID) (context.Context, error) {
	wsID, ok := principal.WorkspaceID(ctx)
	if !ok {
		return nil, errors.New("capture: sync outside workspace context")
	}
	// The authz resolver and the principal seam are untyped (ids.UUID);
	// widen the typed granting-human id at each of those edges.
	rbac, err := r.authority.EffectiveRBAC(ctx, wsID, grantedBy.UUID)
	if err != nil {
		return nil, fmt.Errorf("capture: granting human no longer resolves — the grant dies with them: %w", err)
	}
	seat, err := r.authority.SeatType(ctx, wsID, grantedBy.UUID)
	if err != nil {
		return nil, err
	}
	p := principal.Principal{
		Type:        principal.PrincipalConnector,
		ID:          connectorPrincipalID(name),
		UserID:      grantedBy.UUID,
		OnBehalfOf:  grantedBy.UUID,
		TeamIDs:     rbac.TeamIDs,
		SeatType:    seat,
		Permissions: rbac.Permissions,
	}
	runCtx := principal.WithActor(ctx, p)
	return principal.WithCorrelationID(runCtx, ids.NewV7()), nil
}

func (r *Registry) connector(name string) (connector.Connector, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.connectors[name]
	if !ok {
		return nil, fmt.Errorf("capture: connector %q is not compiled in: %w", name, apperrors.ErrNotFound)
	}
	return c, nil
}
