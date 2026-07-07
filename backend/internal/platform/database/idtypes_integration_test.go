// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package database

// The typed-id ↔ pgx contract, proven against a real connection BEFORE
// any module migrates onto ID[K]: scalar bind + scan, NULL through the
// pointer form, and the `= ANY($1)` slice idiom that needs the
// registered uuid[] OID.

import (
	"context"
	"os"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func TestTypedIDsRoundTripThroughPgx(t *testing.T) {
	dsn := os.Getenv("MARGINCE_TEST_DSN")
	if dsn == "" {
		t.Fatal("MARGINCE_TEST_DSN is not set — run `make db-up` and try again (integration tests fail loudly, they never skip)")
	}
	ctx := context.Background()
	pool, err := NewPool(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	want := ids.New[ids.PersonKind]()

	// Scalar bind + scan.
	var got ids.PersonID
	if err := pool.QueryRow(ctx, `SELECT $1::uuid`, want).Scan(&got); err != nil {
		t.Fatalf("scalar round-trip: %v", err)
	}
	if got != want {
		t.Fatalf("scalar round-trip: got %s want %s", got, want)
	}

	// NULL through the pointer form.
	var null *ids.PersonID
	if err := pool.QueryRow(ctx, `SELECT NULL::uuid`).Scan(&null); err != nil {
		t.Fatalf("NULL scan: %v", err)
	}
	if null != nil {
		t.Fatalf("NULL scan: got %v, want nil", null)
	}
	if err := pool.QueryRow(ctx, `SELECT $1::uuid`, &want).Scan(&got); err != nil || got != want {
		t.Fatalf("pointer bind: %v (got %s)", err, got)
	}

	// The ANY($1) slice idiom — the reason RegisterIDTypes exists.
	other := ids.New[ids.PersonKind]()
	var matched int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM (SELECT unnest(ARRAY[$1::uuid, $2::uuid]) AS id) rows WHERE rows.id = ANY($3)`,
		want, other, []ids.PersonID{want, other}).Scan(&matched); err != nil {
		t.Fatalf("slice ANY bind: %v", err)
	}
	if matched != 2 {
		t.Fatalf("slice ANY bind matched %d rows, want 2", matched)
	}
}
