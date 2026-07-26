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
	scopes, err := grantedScopes(c, actor, name)
	if err != nil {
		return ids.Nil, err
	}
	ws, ok := principal.WorkspaceID(ctx)
	if !ok {
		return ids.Nil, errors.New("capture: connector grant outside workspace context")
	}
	if r.vault == nil {
		return ids.Nil, errors.New("capture: no keyvault configured — a connector credential cannot be sealed")
	}
	// Put-then-commit (like blobstore): seal the credential in the vault
	// first, then commit the row that names it. The row stores the opaque ref;
	// the bytes never touch it. A transaction that fails after the seal strands
	// the blob — nothing references it, so nothing will ever collect it. It is
	// inert (unreferenced and encrypted at rest) and removed by the operational
	// sweep, but it is a stranded secret, not a non-event.
	ref, err := r.vault.Put(ctx, ids.From[ids.WorkspaceKind](ws), []byte(auth))
	if err != nil {
		return ids.Nil, fmt.Errorf("capture: sealing connector credential: %w", err)
	}
	row := connectionUpsert{
		userID:       actor.UserID,
		provider:     name,
		scopes:       scopes,
		ref:          ref,
		accountLabel: accountLabelFor(ctx, c, auth, name),
	}
	var id ids.UUID
	var priorRef *string
	if err := database.WithWorkspaceTx(ctx, r.pool, func(tx pgx.Tx) error {
		var err error
		id, priorRef, err = upsertConnection(ctx, tx, row)
		return err
	}); err != nil {
		return ids.Nil, fmt.Errorf("capture: storing connection: %w", err)
	}
	// The row now names the fresh ref; a prior ref (a genuine reconnect over
	// an existing row) is unreachable from any row from here on and must be
	// destroyed — the same invariant Disconnect enforces, on the overwrite
	// path rather than the withdraw path. A first-time connect has no prior
	// ref: nothing to delete. The delete runs AFTER commit (put-then-commit's
	// mirror: the row is already safely repointed at the new secret before the
	// old one is destroyed), so it must outlive the request and must not fail
	// it — the reconnect is committed and there is nothing left to undo.
	if priorRef != nil {
		keyvault.DeleteDetached(ctx, r.vault, slog.Default(), ws, keyvault.Ref(*priorRef), "reconnect")
	}
	return id, nil
}

// grantedScopes runs the grant-time scope intersection and returns the
// connector's declared scopes as the strings the row freezes. A connector
// demanding a scope the granting human does not hold is refused here rather
// than discovered at 3am mid-sync.
func grantedScopes(c connector.Connector, actor principal.Principal, name string) ([]string, error) {
	declared := c.Descriptor().Scopes
	out := make([]string, 0, len(declared))
	for _, scope := range declared {
		if !actor.Scopes.Has(scope) {
			return nil, fmt.Errorf("capture: connector %s needs scope %q the granting human does not hold: %w",
				name, scope, apperrors.ErrScopeExceeded)
		}
		out = append(out, string(scope))
	}
	return out, nil
}

// accountLabelFor asks the connector to name the account it just authorized.
// Display-only; a connector that cannot name its account simply does not
// implement the seam. This must not fail the connect — a missing label is a
// blank line in the UI, not a lost connection.
func accountLabelFor(ctx context.Context, c connector.Connector, auth connector.Auth, name string) *string {
	labeler, ok := c.(connector.AccountLabeler)
	if !ok {
		return nil
	}
	label, err := labeler.AccountLabel(auth)
	if err != nil {
		slog.WarnContext(ctx, "capture: connector could not name its account", "provider", name, "err", err)
		return nil
	}
	if label == "" {
		return nil
	}
	return &label
}

// connectionUpsert is one connect transaction's write: the granting human, the
// provider and the scopes frozen at grant time, the ref naming the freshly
// sealed credential, and the display-only account label.
type connectionUpsert struct {
	userID       ids.UUID
	provider     string
	scopes       []string
	ref          keyvault.Ref
	accountLabel *string
}

// upsertConnection writes (or re-points) the caller's connection row and
// audits the change inside the connect transaction. It returns the row id and
// the credential ref this write superseded — nil for a first-time connect —
// which the caller destroys once the transaction has committed.
func upsertConnection(ctx context.Context, tx pgx.Tx, in connectionUpsert) (ids.UUID, *string, error) {
	// Capture what this (re)connect is about to overwrite, if a row for this
	// (workspace, user, provider) already exists. The FOR UPDATE lock holds the
	// row still for the span of this transaction, so a concurrent
	// disconnect/reconnect on the same row serializes behind this one rather
	// than racing it. Without the credential_ref read the upsert below would
	// silently orphan the previous secret in the vault — every reconnect
	// (including the reauth_required → Reconnect flow) would leak the prior
	// credential; status and account_label are the audit before-image, which
	// only this read can supply.
	var priorRef, priorStatus, priorLabel *string
	if err := tx.QueryRow(ctx, `
		SELECT credential_ref, status, account_label FROM capture_connection
		 WHERE workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid
		   AND user_id = $1 AND provider = $2
		   FOR UPDATE`,
		in.userID, in.provider).Scan(&priorRef, &priorStatus, &priorLabel); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return ids.Nil, nil, err
	}
	var id ids.UUID
	if err := tx.QueryRow(ctx, `
		INSERT INTO capture_connection (workspace_id, provider, user_id, scopes, credential_ref, status, account_label)
		VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid, $1, $2, $3, $4, 'connected', $5)
		ON CONFLICT (workspace_id, user_id, provider)
		DO UPDATE SET credential_ref = EXCLUDED.credential_ref, auth = NULL, status = 'connected', archived_at = NULL,
		              account_label = EXCLUDED.account_label
		RETURNING id`,
		in.provider, in.userID, in.scopes, string(in.ref), in.accountLabel).Scan(&id); err != nil {
		return ids.Nil, nil, err
	}
	// A grant is a human's deliberate act over their own mailbox, so it is
	// attributed like any other record mutation. A row that already existed is
	// a reconnect (an update over the same connection), never a second create.
	// The images carry the connection, never the credential ref or auth bytes.
	verb, before := "create", map[string]any(nil)
	if priorStatus != nil {
		verb = "update"
		before = map[string]any{"provider": in.provider, "status": *priorStatus, "account_label": priorLabel}
	}
	if err := auditLifecycle(ctx, tx, verb, captureConnectionObject, id, before,
		map[string]any{"provider": in.provider, "status": "connected", "account_label": in.accountLabel}); err != nil {
		return ids.Nil, nil, err
	}
	// A (re)connect starts the scheduling ladder clean: a row parked by
	// reauth_required or degraded by backoff is due immediately with a fresh
	// credential (ADR-0063).
	if _, err := tx.Exec(ctx, `
		UPDATE capture_sync_state
		SET next_sync_at = now(), consecutive_failures = 0, last_error_class = NULL
		WHERE connection_id = $1`, id); err != nil {
		return ids.Nil, nil, err
	}
	return id, priorRef, nil
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
