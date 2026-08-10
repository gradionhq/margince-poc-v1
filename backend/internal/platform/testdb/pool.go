// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package testdb

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/database"
)

// This file gives the lane one pool per DSN per test PROCESS, rather than one
// per test. The connections are the cost: a package's tests run sequentially
// against one clone database, and each test was opening a pool that eagerly
// dialled MinConns backends, using them once and closing them — while the lane
// runs several packages at once against one server with max_connections=100.

// ErrSchemaNotReady is returned when a pool is asked for before EnsureSchema has
// migrated the database.
//
// It is an error rather than a comment because the blast radius is process-wide.
// A caller that gets a pool first queries a schema the migration is about to DROP,
// and every later test in the package inherits whatever that leaves behind: rows
// in tables the migration recreates, or a connection that watched its search_path
// change underneath it. One test's mistake, the whole package's problem.
var ErrSchemaNotReady = errors.New(
	"testdb: the shared pool was requested before EnsureSchema migrated the database — call Setup (or EnsureSchema) first, or a pooled connection outlives the DROP SCHEMA that migration begins with")

var (
	poolsMu sync.Mutex
	pools   = map[string]*pgxpool.Pool{}
)

// testPoolParams are this pool's declared differences from the one cmd/api
// opens, and the only ones. Everything else — jit=off, the typed-id
// registration, the lifetime and health-check limits, the opening Ping that
// makes an unusable DSN a loud setup failure — comes from database.NewPool
// below, so the lane exercises the product's pool with a named delta rather than
// a second constructor that drifts from it.
//
//	pool_min_conns=0          nothing is dialled until a test asks. The old
//	                          per-test pools dialled MinConns eagerly at every
//	                          Setup, which is the cost this change removes;
//	                          MaxConns stays at database.NewPool's 16, the same
//	                          ceiling each per-test pool already had, so the
//	                          lane's peak connection count cannot grow.
//	pool_max_conn_idle_time   a package that spiked hands its connections back
//	                          promptly instead of holding its high-water mark
//	                          for the rest of the run.
//	default_query_exec_mode   the only mode that is both cache-free and
//	                          server-typed, which is what THIS pool's connections
//	                          need in order to outlive a schema change — see the
//	                          note on Pool.
//
// The first two change how many connections exist; the third is the only one that
// changes how a query is executed, and so the only place this pool's behaviour
// parts from the api's.
var testPoolParams = map[string]string{
	"pool_min_conns":          "0",
	"pool_max_conn_idle_time": "30s",
	"default_query_exec_mode": "describe_exec",
}

// Pool returns the process-wide pool for dsn, opening it on first use. Keyed by
// DSN, so the app-role and owner-role pools are distinct pools with the same
// lifetime.
//
// Deliberately NOT closed per test: it is closed when the test process exits,
// which is what makes it shared. Callers must not close it — a closed shared pool
// fails every later test in the package with a use-after-close that names nothing.
//
// What sharing costs is the ability to cache a statement, and describe_exec is
// how that is paid. It asks the server to describe the statement on every
// execution, so no statement cache spans a schema change.
//
// That is narrower than "nothing is cached", and the difference is the next trap.
// The per-connection pgtype.Map still memoizes encode plans, keyed by OID and
// invalidated only by a type registration — never by DDL. It is harmless today
// only because nothing here registers a schema-derived type: RegisterIDTypes maps
// to the fixed system uuid OIDs, and no caller uses conn.LoadType or
// RegisterType, so an extension's type falls to the same format-independent plan
// whatever its OID is. The first caller to LoadType("vector") or register a
// composite reopens this, and this mode alone will not cover it.
//
// This is also a property of THIS pool, not of the lane. Every other connection
// the suites open — the owner pgx.Conn each fixture dials, SchemaPool, and the
// dozen direct database.NewPool pools — still runs pgx's default cache_statement.
// Those close with their test, so a stale plan costs one test rather than the
// process, which is why they are left alone.
//
// pgx offers four other modes and none of them will do here. The two that cache —
// cache_statement, which is production's default, and cache_describe — both
// document that the first execution after a schema change may fail; a cached
// NAMED PLAN fails as SQLSTATE 0A000 "cached plan must not change result type",
// and a cached PARAMETER OID fails as XX000 "cache lookup failed for type N" once
// that type is recreated. The other two, exec and simple_protocol, cache nothing
// and cost no extra round trip, but they infer parameter types from the Go values
// instead of asking the server, which cannot encode the typed-id slices the record
// stores bind through ANY($n) — pool_integration_test.go pins that bind for this
// reason. describe_exec is the only mode that is both cache-free and server-typed.
//
// One of those failures was observed rather than reasoned about, which is the
// distinction that matters here: cache_describe reproduced the XX000 form under
// INTEGRATION_SHARD=7/12, in reembed's baseline seed, because search's embedding
// upsert binds $5::vector and the server therefore types that parameter as
// pgvector's — an OID that perfbench's benchDatabase changes when it drops the
// schema and remigrates (the one migrate-again waiver in
// backend/integrationmigrateonce_test.go, same process and same clone database).
// The 0A000 form is what pgx documents for a named plan; this lane has not seen
// it, and #772 is whether production has.
//
// The delta from production is narrower than swapping a mode sounds. Parameter
// OIDs and result formats still come from the server, so the encode and decode
// paths the lane exercises are the ones cmd/api uses; what differs is one extra
// round trip per statement that takes arguments, and the absent cache. That cost
// is real on a lane bound by round trips — 244s against the 190s the unsound
// cache_describe managed, same sitting, Postgres restarted before each run.
// Clearing the caches at each reset instead of forgoing them measured no cheaper
// (pgxpool.Reset 254s; DeallocateAll 215s, but it acquires every idle connection
// per reset and surfaced the straggler in #770).
//
// Two things this does not survive, neither of which applies: a connection pooler
// that switches the underlying connection between the two round trips, and a
// caller that queries before EnsureSchema has run — see ErrSchemaNotReady.
func Pool(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	if !schemaReady.Load() {
		return nil, ErrSchemaNotReady
	}
	poolsMu.Lock()
	defer poolsMu.Unlock()
	if pool, ok := pools[dsn]; ok {
		return pool, nil
	}
	tuned, err := withTestPoolParams(dsn)
	if err != nil {
		return nil, err
	}
	pool, err := database.NewPool(ctx, tuned)
	if err != nil {
		return nil, fmt.Errorf("opening the shared test pool: %w", err)
	}
	pools[dsn] = pool
	return pool, nil
}

// withTestPoolParams adds testPoolParams to dsn without disturbing what is
// already there — a DSN that names one of them keeps its own value, matching
// database.NewPool's rule that whoever sized the pool explicitly wins.
func withTestPoolParams(dsn string) (string, error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return "", fmt.Errorf("parsing the test DSN: %w", err)
	}
	q := u.Query()
	for k, v := range testPoolParams {
		if !q.Has(k) {
			q.Set(k, v)
		}
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// AssertPoolsQuiesced fails the test if it ends while still holding a pooled
// connection.
//
// Closing the pool per test used to do this job by accident: a goroutine the test
// left running — a River client whose Stop timed out, a relay that outlived its
// context — started failing the moment its pool went away. A shared pool has no
// such moment, so that goroutine would go on claiming jobs and writing rows into
// the database the NEXT test just reset, and the wrong suite would report it.
//
// Register it in the fixture that hands out the pool, immediately, so it runs
// last: t.Cleanup is LIFO, and every cleanup a test adds later — the ones that
// stop the runners — must have already run before this can honestly claim the
// package is quiet.
func AssertPoolsQuiesced(t *testing.T) {
	t.Helper()
	poolsMu.Lock()
	defer poolsMu.Unlock()
	for dsn, pool := range pools {
		if n := pool.Stat().AcquiredConns(); n != 0 {
			t.Errorf("the test ended holding %d connection(s) on the shared pool for %s — something it started is still running, and the next test resets the database under it",
				n, redactDSN(dsn))
		}
	}
}

// redactDSN keeps the credentials in a test DSN out of a failure message: the
// lane's logs are CI artifacts, and the role name is what identifies the pool.
func redactDSN(dsn string) string {
	u, err := url.Parse(dsn)
	if err != nil {
		return "the test database"
	}
	if u.User != nil {
		u.User = url.User(u.User.Username())
	}
	return u.Redacted()
}
