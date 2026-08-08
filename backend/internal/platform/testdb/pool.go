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
// migrated the database. It is an error rather than a comment because the blast
// radius is process-wide: a connection opened before the migration's DROP SCHEMA
// holds descriptions of relations that no longer exist, and a shared pool would
// carry that for the whole package rather than one test.
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
//	default_query_exec_mode   see the note on the statement cache below.
var testPoolParams = map[string]string{
	"pool_min_conns":          "0",
	"pool_max_conn_idle_time": "30s",
	"default_query_exec_mode": "cache_describe",
}

// Pool returns the process-wide pool for dsn, opening it on first use. Keyed by
// DSN, so the app-role and owner-role pools are distinct pools with the same
// lifetime.
//
// Deliberately NOT closed per test: it is closed when the test process exits,
// which is what makes it shared. Callers must not close it — a closed shared pool
// fails every later test in the package with a use-after-close that names nothing.
//
// The exec mode is the price of sharing. pgx's default prepares each statement on
// the server under a name and reuses that plan for the life of the connection,
// which is safe only while a connection cannot outlive the schema it was planned
// against. Here it can, and this schema changes constantly: the customfields
// engine ALTERs record tables mid-test, the reset drops those columns again, and
// ApplyRiverSchema creates River's tables the first time a suite asks for a real
// worker. A named plan that survives any of those draws SQLSTATE 0A000 "cached
// plan must not change result type" in whichever suite runs next — a failure with
// nothing in it pointing back at the DDL that caused it.
//
// cache_describe holds no server-side plan, so that error has nowhere to come
// from. What it caches is the client-side statement description — parameter OIDs
// and result format codes — while Parse, Bind, Describe(portal) and Execute still
// go to the server on every execution, so result field descriptions are always
// the live ones. pgx documents the cached description itself as assumed stable
// (a "SELECT *" whose column count changes under it may fail); that is a loud
// bind or decode error rather than a wrong row, and no store in this tree selects
// a star.
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
