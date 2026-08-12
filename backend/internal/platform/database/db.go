// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package database

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// DB is a pool that knows which workspace it binds — the installation's, the
// only one there is (ADR-0061/A107).
//
// It exists to take that binding OFF the request context. WithWorkspaceTx
// reads the workspace from the caller's ctx, which means every request path,
// every job and every fixture has to put it there first, and a path that
// forgets fails at the SQL rather than at the seam. ADR-0091 §9 step 3 is the
// collapse: one helper, the singleton resolved once, and the GUC still set
// from it, so RLS stays armed and the tenant-isolation suite remains the proof
// that the edit was faithful.
//
// The workspace arrives as a resolver rather than a value because bootstrap
// has not necessarily happened when the server is assembled — the worker polls
// until the API bootstraps. identity's resolver caches its first success, so
// "resolved once" is a property of that resolver, not of a package-level
// variable this type would otherwise have to own.
type DB struct {
	pool      *pgxpool.Pool
	workspace func(context.Context) (ids.WorkspaceID, error)
}

// Bind returns a handle that resolves its workspace through resolve.
func Bind(pool *pgxpool.Pool, resolve func(context.Context) (ids.WorkspaceID, error)) *DB {
	return &DB{pool: pool, workspace: resolve}
}

// BindTo returns a handle pinned to one workspace, for the callers that
// already hold it: bootstrap, which is creating the installation the resolver
// would look for, and the cross-tenant suites, which seed a second workspace
// precisely to prove one cannot read the other.
func BindTo(pool *pgxpool.Pool, ws ids.WorkspaceID) *DB {
	return &DB{pool: pool, workspace: func(context.Context) (ids.WorkspaceID, error) { return ws, nil }}
}

// Workspace reports which workspace this handle binds, for the callers that
// need to name it rather than run in it — a job asserting it was wired to the
// tenant its args declare, above all.
func (d *DB) Workspace(ctx context.Context) (ids.WorkspaceID, error) {
	return d.workspace(ctx)
}

// Pool exposes the underlying pool for the paths that do not run a
// transaction — the outbox relay's listener, the health probe.
//
// Nil-safe on a nil handle, because construction reaches it: a store built
// from an un-injected handle would otherwise panic where it is WIRED rather
// than where it is used, and the unit tests that build a handler with no
// database at all — to assert a gate that answers before any query — are
// exactly the callers that would crash.
func (d *DB) Pool() *pgxpool.Pool {
	if d == nil {
		return nil
	}
	return d.pool
}

// Tx runs fn inside a transaction with app.workspace_id bound, which is what
// the RLS policies key on. Same contract as WithWorkspaceTx, minus the
// requirement that the caller have put the workspace in ctx.
func (d *DB) Tx(ctx context.Context, fn func(pgx.Tx) error) error {
	if d == nil {
		// A store built without a handle, answered with the sentinel that
		// already means "no workspace could be bound, and no SQL ran".
		//
		// It returns rather than panicking because that is how this degraded
		// before the collapse, and the sentinel is kept because callers key on
		// it: the gate tests distinguish "the read gate denied me" from "the
		// gate admitted me and the probe reached a database it could not bind"
		// by exactly this error, and that distinction is the thing they exist
		// to protect.
		return fmt.Errorf("%w: no database handle was injected; "+
			"construct this store through compose, which binds the installation's pool",
			ErrNoWorkspace)
	}
	ws, err := d.workspace(ctx)
	if err != nil {
		return fmt.Errorf("pg: resolving the installation's workspace: %w", err)
	}
	return withBoundTx(ctx, d.pool, ws, fn)
}
