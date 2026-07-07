// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package database is the shared Postgres platform layer: the configured
// connection pool and the tenant-scoped transaction helper every store
// uses. It is the ONE place the RLS GUC contract (data-model §1.3) is
// implemented — no store issues its own SET LOCAL.
package database

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// NewPool opens a pgxpool with explicit operational limits (a defaultless
// pool under load exhausts Postgres connections and hides slow queries).
// Each limit is a fallback, not a mandate: an operator who sized the pool
// in the DSN (pool_max_conns=…) knows their Postgres better than a
// hardcoded 16 does, so a DSN-provided value always wins.
func NewPool(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("pg: parsing DSN: %w", err)
	}
	// ParseConfig already applied any pool_* DSN parameters; only fill
	// the ones the DSN left unset.
	if !strings.Contains(dsn, "pool_max_conns") {
		cfg.MaxConns = 16
	}
	if !strings.Contains(dsn, "pool_min_conns") {
		cfg.MinConns = 2
	}
	if !strings.Contains(dsn, "pool_max_conn_lifetime") {
		cfg.MaxConnLifetime = 30 * time.Minute
	}
	if !strings.Contains(dsn, "pool_max_conn_idle_time") {
		cfg.MaxConnIdleTime = 5 * time.Minute
	}
	if !strings.Contains(dsn, "pool_health_check_period") {
		cfg.HealthCheckPeriod = time.Minute
	}
	// Typed entity ids ride uuid/uuid[] on every connection.
	cfg.AfterConnect = func(_ context.Context, conn *pgx.Conn) error {
		RegisterIDTypes(conn)
		return nil
	}

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("pg: opening pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("pg: ping: %w", err)
	}
	return pool, nil
}

// ErrNoWorkspace means a domain query was attempted outside a workspace
// context — a programming error, surfaced before any SQL runs.
var ErrNoWorkspace = errors.New("pg: no workspace bound to context")

// WithWorkspaceTx runs fn inside a transaction whose app.workspace_id GUC
// is SET LOCAL to the context's workspace, which is what the RLS policies
// key on. SET LOCAL is transaction-scoped — it resets at COMMIT/ROLLBACK,
// so a pooled connection can never leak one tenant's GUC to the next
// checkout (the §1.3 pool-reuse rule). Every domain read and write goes
// through here; there is no raw-pool path for tenant data.
func WithWorkspaceTx(ctx context.Context, pool *pgxpool.Pool, fn func(pgx.Tx) error) error {
	wsID, ok := principal.WorkspaceID(ctx)
	if !ok {
		return ErrNoWorkspace
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("pg: begin: %w", err)
	}
	// The deferred rollback only matters on the error path; after a
	// successful Commit it answers ErrTxClosed by design, and on the error
	// path the fn/commit error is the one the caller must see.
	//craft:ignore swallowed-errors rollback after commit is a designed no-op; on the error path the fn error supersedes it
	defer func() { _ = tx.Rollback(ctx) }()

	// Parameterized set_config, never string-built SET LOCAL.
	if _, err := tx.Exec(ctx, `SELECT set_config('app.workspace_id', $1, true)`, wsID.String()); err != nil {
		return fmt.Errorf("pg: binding workspace GUC: %w", err)
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// WithInfraTx runs fn in a transaction WITHOUT a tenant GUC — for the
// narrow infra paths that legitimately cross tenants (workspace bootstrap,
// session lookup by token hash, the outbox relay). Under the deny-on-unset
// policies such a transaction reads zero tenant rows unless the owning
// role bypasses RLS, which keeps misuse loud in tests.
func WithInfraTx(ctx context.Context, pool *pgxpool.Pool, fn func(pgx.Tx) error) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("pg: begin: %w", err)
	}
	// Error-path safety net only: once Commit succeeded this rollback is
	// pgx's ErrTxClosed no-op, and a genuine failure already left through fn.
	//craft:ignore swallowed-errors deferred rollback of a committed infra tx cannot fail meaningfully; real failures surface via fn or Commit
	defer func() { _ = tx.Rollback(ctx) }()

	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
