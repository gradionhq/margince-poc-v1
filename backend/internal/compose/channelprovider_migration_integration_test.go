// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// Migration 0242 replaces activity.kind's and person_channel_identity.provider's
// inline CHECKs with FKs into two new derived tables (DESIGN-SP4 §4). These
// tests prove the migration itself: the seed rows land, and the FK — not an
// application-side list — is what refuses an unregistered kind.

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
)

func TestActivityKindAndChannelProviderAreSeededByMigration(t *testing.T) {
	integration.Setup(t) // triggers EnsureSchema — the migration this test asserts on
	owner := integration.OwnerConn(t)
	ctx := context.Background()

	var kinds []string
	rows, err := owner.Query(ctx, `SELECT kind FROM activity_kind ORDER BY kind`)
	if err != nil {
		t.Fatalf("querying activity_kind: %v", err)
	}
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			t.Fatalf("scanning activity_kind row: %v", err)
		}
		kinds = append(kinds, k)
	}
	rows.Close()
	want := []string{"call", "email", "meeting", "note", "task", "telegram", "whatsapp"}
	if len(kinds) != len(want) {
		t.Fatalf("activity_kind seeded %v, want %v", kinds, want)
	}
	for i, k := range want {
		if kinds[i] != k {
			t.Fatalf("activity_kind seeded %v, want %v", kinds, want)
		}
	}

	var provider, transport string
	if err := owner.QueryRow(ctx, `SELECT provider, transport FROM channel_provider`).Scan(&provider, &transport); err != nil {
		t.Fatalf("querying channel_provider: %v", err)
	}
	if provider != "telegram" || transport != "core" {
		t.Fatalf("channel_provider seeded (%q, %q), want (telegram, core)", provider, transport)
	}
}

// The FK is what does the real work: an unregistered kind is refused by the
// database, not by an application-side list somebody has to remember.
func TestActivityKindFKRefusesAnUnregisteredKind(t *testing.T) {
	e := integration.Setup(t)
	ctx := context.Background()

	_, err := integration.OwnerConn(t).Exec(ctx, `
		INSERT INTO activity (workspace_id, kind, source, captured_by)
		VALUES ($1, 'dispact', 'manual', 'test')`, e.WS)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.ConstraintName != "activity_kind_fkey" {
		t.Fatalf("insert failed with %v, want a foreign_key_violation on activity_kind_fkey specifically — "+
			"any other failure (a bad column, an RLS refusal) would pass this test for the wrong reason", err)
	}
}
