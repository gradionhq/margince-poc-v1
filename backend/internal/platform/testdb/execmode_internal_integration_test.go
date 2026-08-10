// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package testdb

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
)

// TestEveryPooledDSNRunsTheCacheFreeExecMode covers every pool this package has
// handed out, not the one a test happened to ask for.
//
// Pool is keyed by DSN and memoizes two process-lived pools in practice: the app
// role's, and the owner role's that ApplyRiverSchema takes. Their DSNs come from
// DIFFERENT env vars (MARGINCE_TEST_APP_DSN, MARGINCE_TEST_DSN) whose query
// strings scripts/lib-testdb.sh copies into every clone independently — so an
// exported owner DSN carrying a caching mode would leave the owner pool caching
// named statements across ApplyRiverSchema's DDL with an app-pool-only assertion
// still green. Asserting over the map is what makes a third pooled DSN covered the
// day it appears rather than the day someone remembers.
//
// Internal to the package because the map is; the caller-facing behaviour is
// pinned by TestSharedPoolSurvivesATypeItsParametersName.
func TestEveryPooledDSNRunsTheCacheFreeExecMode(t *testing.T) {
	// ownerConn migrates, which is the precondition Pool refuses to run before.
	// Opening both DSNs here means the map holds what the lane's fixtures hold,
	// rather than whichever one a neighbouring test happened to ask for first.
	ownerConn(t)
	for _, env := range []string{"MARGINCE_TEST_APP_DSN", "MARGINCE_TEST_DSN"} {
		dsn := os.Getenv(env)
		if dsn == "" {
			t.Fatalf("%s not set — run `make db-up` (integration tests fail loudly, they never skip)", env)
		}
		if _, err := Pool(context.Background(), dsn); err != nil {
			t.Fatalf("opening the shared pool for %s: %v", env, err)
		}
	}

	poolsMu.Lock()
	defer poolsMu.Unlock()
	if len(pools) == 0 {
		t.Fatal("no pooled DSN to check — this gate would pass on a package that never shared a pool")
	}
	for dsn, pool := range pools {
		if got := pool.Config().ConnConfig.DefaultQueryExecMode; got != pgx.QueryExecModeDescribeExec {
			t.Errorf("the shared pool for %s runs exec mode %v, want describe_exec — a caching mode lets a connection outlive the schema it cached against, and a DSN's own parameter wins over testPoolParams, so check the env var that DSN comes from",
				redactDSN(dsn), got)
		}
	}
}
