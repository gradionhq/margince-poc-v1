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
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
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
	// JIT is off for this workload, and the row-scope predicates are why.
	// Every list, search and timeline query composes the caller's
	// visibility clause — nested EXISTS over person, organization, deal,
	// activity_link and record_grant — which inflates the plan's ESTIMATED
	// cost past jit_above_cost while the query itself stays an indexed
	// OLTP read. Postgres then spends longer in LLVM than in the query:
	// the /search union measured 12ms of work behind 475ms of JIT
	// generation, inlining, optimization and emission.
	//
	// What crosses the threshold is the row-scope TIER, not data volume,
	// so a rep pays it on a query an unbounded admin runs for free. JIT
	// earns its keep on long analytical scans; this product runs none on
	// the request path. A DSN that names jit itself still wins.
	if !strings.Contains(dsn, "jit") {
		if cfg.ConnConfig.RuntimeParams == nil {
			cfg.ConnConfig.RuntimeParams = map[string]string{}
		}
		cfg.ConnConfig.RuntimeParams["jit"] = "off"
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

// ErrNoWorkspace means a domain query was attempted with no workspace to bind
// — before bootstrap has resolved the installation, or from a context that
// carries none while none is bound process-wide. Surfaced before any SQL runs.
var ErrNoWorkspace = errors.New("pg: no workspace bound to context")

// installation holds the singleton organization this process serves, resolved
// once at boot (ADR-0061 §3). It is the fallback WithWorkspaceTx binds when a
// context carries no workspace of its own — the first step of ADR-0091 §9,
// which retires the per-request tenant selector without touching the schema.
//
// Process-global because the thing it names is: one installation serves one
// organization, and every transaction in this process binds the same id. It is
// written once, by the boot path, and read by every transaction after.
//
// Nil until BindInstallation runs, so a query before bootstrap still fails
// loudly rather than guessing. That is deliberate: pre-bootstrap there is no
// installation to guess AT, and the worker polls this state until the API
// creates one.
var installation atomic.Pointer[ids.UUID]

// resolveWorkspace decides which workspace a transaction binds: the context's
// own if it carries one, otherwise the installation this process serves.
//
// Named and separated because it is the decision, not a detail of opening a
// transaction — it is what ADR-0091 §9 step 3 changes, and it is provable
// without a database precisely because it happens before any SQL.
func resolveWorkspace(ctx context.Context) (ids.UUID, error) {
	if wsID, ok := principal.WorkspaceID(ctx); ok {
		return wsID, nil
	}
	bound := installation.Load()
	if bound == nil {
		return ids.UUID{}, ErrNoWorkspace
	}
	return *bound, nil
}

// BindInstallation names the singleton organization every transaction in this
// process binds. Called once, by the boot path, after the installation is
// resolved or created.
//
// What this changes, stated where a reader meets it: a context that carries no
// workspace now reads the installation's data instead of failing with
// ErrNoWorkspace. With one tenant there is nothing to leak ACROSS — the
// fallback can only ever bind the id every request already binds — but the
// loud failure that used to catch a path forgetting its workspace is gone.
// ADR-0091 §4 accepts that trade for the schema phase; it arrives here because
// the fallback is what lets the fleet loops and their rls-exempt hatches
// collapse. RBAC is unaffected: auth.Require gates every store entry point
// regardless of which workspace is bound.
func BindInstallation(wsID ids.UUID) { installation.Store(&wsID) }

// WithWorkspaceTx runs fn inside a transaction whose app.workspace_id GUC
// is SET LOCAL to the context's workspace, which is what the RLS policies
// key on. SET LOCAL is transaction-scoped — it resets at COMMIT/ROLLBACK,
// so a pooled connection can never leak one tenant's GUC to the next
// checkout (the §1.3 pool-reuse rule). Every domain read and write goes
// through here; there is no raw-pool path for tenant data.
func WithWorkspaceTx(ctx context.Context, pool *pgxpool.Pool, fn func(pgx.Tx) error) error {
	wsID, err := resolveWorkspace(ctx)
	if err != nil {
		return err
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
