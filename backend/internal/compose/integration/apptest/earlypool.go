// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package apptest

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/database"
)

// EarlyPool opens a pool onto the app database BEFORE the harness boots, for the
// one thing that has to exist first: a suite that needs a row in the vault (a
// stored Google credential, say) must write it before SetupAppWithOptions builds
// the composition that reads it.
//
// It lives here because both this package's callers and the suite packages split
// out of internal/compose/integration need it, and a fixture keyed on nothing but
// *testing.T has no reason to sit in either one of them.
func EarlyPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	appDSN := os.Getenv("MARGINCE_TEST_APP_DSN")
	if appDSN == "" {
		t.Fatal("MARGINCE_TEST_APP_DSN not set — run `make db-up` (integration tests fail loudly, they never skip)")
	}
	pool, err := database.NewPool(context.Background(), appDSN)
	if err != nil {
		t.Fatalf("opening the pre-boot pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}
