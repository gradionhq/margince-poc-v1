// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package capture

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/database"
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
}

func NewRegistry(pool *pgxpool.Pool, sink *Sink, authority authz.Resolver) *Registry {
	return &Registry{
		connectors: map[string]connector.Connector{},
		pool:       pool,
		sink:       sink,
		authority:  authority,
	}
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
	var id ids.UUID
	err = database.WithWorkspaceTx(ctx, r.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			INSERT INTO connector_connection (workspace_id, connector, granted_by, scopes, auth)
			VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid, $1, $2, $3, $4)
			ON CONFLICT (workspace_id, connector, granted_by)
			DO UPDATE SET auth = EXCLUDED.auth, status = 'active', last_error = NULL
			RETURNING id`,
			name, actor.UserID, scopes, []byte(auth)).Scan(&id)
	})
	if err != nil {
		return ids.Nil, fmt.Errorf("capture: storing connection: %w", err)
	}
	return id, nil
}

// RunTransient runs ONE sync of an already-authenticated connector under
// the CALLING human's live authority, WITHOUT persisting a connection: no
// connector_connection row, no stored credentials, no cursor. It is the
// one-shot pull path — the connector holds its live provider session and
// its own credentials; the registry contributes the run-time connector
// principal built from the human's LIVE RBAC. Authority is capped exactly
// where every capture write is: the Sink's per-entry RBAC gate against that
// principal (a human lacking activity:create cannot land a row), and the
// REST admission layer already refused a read-seat human on this POST. The
// write lands through the same Sink, so audit + outbox hold.
func (r *Registry) RunTransient(ctx context.Context, c connector.Connector, auth connector.Auth) error {
	actor, ok := principal.Actor(ctx)
	if !ok || actor.Type != principal.PrincipalHuman {
		return errors.New("capture: only a human runs a one-shot connector pull")
	}
	runCtx, err := r.connectorContext(ctx, c.Descriptor().Name, actor.UserID)
	if err != nil {
		return err
	}
	// A one-shot pull has no persisted cursor to advance; the connector
	// bounds the pull itself (last N messages).
	if _, err := c.Sync(runCtx, auth, nil, r.sink); err != nil {
		return err
	}
	return nil
}

// SyncOnce runs one incremental sync for a connection: builds the
// connector principal from the granting human's live authority, hands
// the connector the sink, and advances the stored cursor only when the
// sync succeeded end to end.
func (r *Registry) SyncOnce(ctx context.Context, connectionID ids.UUID) error {
	var (
		name      string
		grantedBy ids.UUID
		authBytes []byte
		cursor    []byte
	)
	err := database.WithWorkspaceTx(ctx, r.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT connector, granted_by, auth, cursor FROM connector_connection
			WHERE id = $1 AND status = 'active'`, connectionID).
			Scan(&name, &grantedBy, &authBytes, &cursor)
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

	runCtx, err := r.connectorContext(ctx, name, grantedBy)
	if err != nil {
		return err
	}
	next, syncErr := c.Sync(runCtx, connector.Auth(authBytes), connector.Cursor(cursor), r.sink)
	if syncErr != nil {
		if markErr := r.markError(ctx, connectionID, syncErr); markErr != nil {
			return errors.Join(syncErr, markErr)
		}
		return syncErr
	}
	return database.WithWorkspaceTx(ctx, r.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			UPDATE connector_connection SET cursor = $2, last_health_at = now(), last_error = NULL
			WHERE id = $1`, connectionID, []byte(next))
		return err
	})
}

// connectorContext builds the acting principal: connector identity,
// the granting human's LIVE permissions and teams (connector ≤ human as
// a runtime property), full seat (capture is a write path by nature —
// the human's ability to grant it is what the scope check consumed).
func (r *Registry) connectorContext(ctx context.Context, name string, grantedBy ids.UUID) (context.Context, error) {
	wsID, ok := principal.WorkspaceID(ctx)
	if !ok {
		return nil, errors.New("capture: sync outside workspace context")
	}
	rbac, err := r.authority.EffectiveRBAC(ctx, wsID, grantedBy)
	if err != nil {
		return nil, fmt.Errorf("capture: granting human no longer resolves — the grant dies with them: %w", err)
	}
	seat, err := r.authority.SeatType(ctx, wsID, grantedBy)
	if err != nil {
		return nil, err
	}
	p := principal.Principal{
		Type:        principal.PrincipalConnector,
		ID:          connectorPrincipalID(name),
		UserID:      grantedBy,
		OnBehalfOf:  grantedBy,
		TeamIDs:     rbac.TeamIDs,
		SeatType:    seat,
		Permissions: rbac.Permissions,
	}
	runCtx := principal.WithActor(ctx, p)
	return principal.WithCorrelationID(runCtx, ids.NewV7()), nil
}

func (r *Registry) markError(ctx context.Context, connectionID ids.UUID, syncErr error) error {
	return database.WithWorkspaceTx(ctx, r.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			UPDATE connector_connection SET status = 'error', last_error = $2 WHERE id = $1`,
			connectionID, syncErr.Error())
		return err
	})
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
