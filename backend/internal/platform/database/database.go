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
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// NewPool opens a pgxpool with explicit operational limits (a defaultless
// pool under load exhausts Postgres connections and hides slow queries).
func NewPool(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("pg: parsing DSN: %w", err)
	}
	cfg.MaxConns = 16
	cfg.MinConns = 2
	cfg.MaxConnLifetime = 30 * time.Minute
	cfg.MaxConnIdleTime = 5 * time.Minute
	cfg.HealthCheckPeriod = time.Minute

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
	defer func() { _ = tx.Rollback(ctx) }() // no-op after commit

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
	defer func() { _ = tx.Rollback(ctx) }()

	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
