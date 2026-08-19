// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// Migration 0240 replaces activity.kind's and person_channel_identity.provider's
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
	// The narrowed vocabulary (ADR-0107/A158): six INTERACTION kinds, and no
	// transport among them. telegram and whatsapp left this table at the
	// narrowing —
	// telegram had already become a channel_provider row, whatsapp becomes one
	// there — so a name appearing in both lists again would mean the axes had
	// been re-conflated.
	want := []string{"call", "email", "meeting", "message", "note", "task"}
	if len(kinds) != len(want) {
		t.Fatalf("activity_kind seeded %v, want %v", kinds, want)
	}
	for i, k := range want {
		if kinds[i] != k {
			t.Fatalf("activity_kind seeded %v, want %v", kinds, want)
		}
	}

	var providers []string
	provRows, err := owner.Query(ctx, `SELECT provider || '/' || transport FROM channel_provider ORDER BY provider`)
	if err != nil {
		t.Fatalf("querying channel_provider: %v", err)
	}
	for provRows.Next() {
		var p string
		if err := provRows.Scan(&p); err != nil {
			t.Fatalf("scanning channel_provider row: %v", err)
		}
		providers = append(providers, p)
	}
	provRows.Close()
	// whatsapp is registered but composes no connector: a transport with no
	// sender yet, which is the honest description while A103's WhatsApp
	// connector is still coming. Registration is what lets a hand-logged
	// WhatsApp message name its transport; sendability is a separate question
	// the send pre-flight answers.
	wantProviders := []string{"telegram/core", "whatsapp/core"}
	if len(providers) != len(wantProviders) {
		t.Fatalf("channel_provider seeded %v, want %v", providers, wantProviders)
	}
	for i, p := range wantProviders {
		if providers[i] != p {
			t.Fatalf("channel_provider seeded %v, want %v", providers, wantProviders)
		}
	}
}

// The FK is what does the real work: an unregistered kind is refused by the
// database, not by an application-side list somebody has to remember.
func TestActivityKindFKRefusesAnUnregisteredKind(t *testing.T) {
	ctx := context.Background()

	_, err := integration.OwnerConn(t).Exec(ctx, `
		INSERT INTO activity (kind, source, captured_by)
		VALUES ('dispact', 'manual', 'test')`)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.ConstraintName != "activity_kind_fkey" {
		t.Fatalf("insert failed with %v, want a foreign_key_violation on activity_kind_fkey specifically — "+
			"any other failure (a bad column, an RLS refusal) would pass this test for the wrong reason", err)
	}
}
