// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package apptest

import (
	"os"
	"testing"
)

// AppDSN is the app-role DSN the lane hands each package, for a suite or fixture
// that opens its own connection rather than riding a pool it was given.
//
// Fatal rather than skipped when unset, like every other entry point into this
// lane: a security suite that skipped because its DSN was missing would look
// exactly like one that passed.
//
// It lives in apptest rather than in internal/compose/integration because this is
// the only home every caller can reach, apptest's own fixtures included. Nothing
// here may import that package: its suites are `package integration` and import
// this one, so the edge back would close a cycle in their own test binary. A copy
// kept over there would therefore leave EarlyPool below with a second spelling of
// the same invariant.
func AppDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("MARGINCE_TEST_APP_DSN")
	if dsn == "" {
		t.Fatal("MARGINCE_TEST_APP_DSN not set — run `make db-up` (integration tests fail loudly, they never skip)")
	}
	return dsn
}
