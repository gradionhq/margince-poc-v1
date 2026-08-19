// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// The transport axis is separate from the interaction axis, asserted against the
// schema rather than against anyone's memory of it. The behavioural halves live
// where the behaviour is — the send path's refusal in
// internal/modules/activities, the boot reconcile's in
// channelprovider_integration_test.go — and what is left here is the shape those
// depend on.

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
)

// A provider is a transport and names no interaction kind, so channel_provider
// must not reference activity_kind. The FK it originally shipped with asserted
// the opposite, and it is the reason boot had to mint an activity_kind row per
// provider — which would silently restore whatever a kind-narrowing migration
// removed, on the next boot.
//
// Introspection rather than a behavioural probe because the failure this guards
// is a RE-ADD: an FK restored by a later migration breaks nothing until an
// extension registers a provider that is not also a kind, which is far too late
// to learn it.
func TestChannelProviderDoesNotReferenceActivityKind(t *testing.T) {
	integration.Setup(t) // triggers EnsureSchema — the migrations this test asserts on
	owner := integration.OwnerConn(t)

	var referenced []string
	rows, err := owner.Query(context.Background(), `
		SELECT confrelid::regclass::text
		FROM pg_constraint
		WHERE conrelid = 'channel_provider'::regclass AND contype = 'f'`)
	if err != nil {
		t.Fatalf("querying channel_provider's foreign keys: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var target string
		if err := rows.Scan(&target); err != nil {
			t.Fatalf("scanning a foreign key row: %v", err)
		}
		referenced = append(referenced, target)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("reading channel_provider's foreign keys: %v", err)
	}

	for _, target := range referenced {
		if target == "activity_kind" {
			t.Fatal("channel_provider references activity_kind again — that FK asserts every transport is also an interaction kind, " +
				"which refuses any provider that names no kind, and forces boot to mint kind rows a narrowing migration would then have to remove twice")
		}
	}
}

// activity.channel_provider is the transport, and it must FK into the registry:
// the column's whole claim is that it names a provider this installation has,
// and without the reference it is a free-text field that agrees with the
// registry only for as long as every writer happens to.
func TestActivityChannelProviderReferencesTheRegistry(t *testing.T) {
	integration.Setup(t)
	owner := integration.OwnerConn(t)

	var target string
	if err := owner.QueryRow(context.Background(), `
		SELECT confrelid::regclass::text
		FROM pg_constraint
		WHERE conrelid = 'activity'::regclass AND contype = 'f'
		  AND conkey = ARRAY[(SELECT attnum FROM pg_attribute
		                      WHERE attrelid = 'activity'::regclass AND attname = 'channel_provider')]`).Scan(&target); err != nil {
		t.Fatalf("activity.channel_provider has no foreign key: %v", err)
	}
	if target != "channel_provider" {
		t.Fatalf("activity.channel_provider references %q, want channel_provider", target)
	}
}

// The registry refuses an unregistered transport on the activity itself, which is
// the guarantee the send path's read depends on: a provider read back off a row
// is a provider this installation actually composed.
//
// The provider name is one no unit could ever be called, deliberately: naming a
// real extension here would make the test pass or fail on whether that unit
// happens to be composed, which is a different question from the one it asks.
//
// The kind is `message` for a subtler reason: since ADR-0107/A158 a non-message
// carrying a provider trips activity_message_has_provider FIRST, so the insert
// would fail for the wrong reason and this test would report a passing FK it
// never actually reached.
func TestActivityChannelProviderFKRefusesAnUnregisteredProvider(t *testing.T) {

	_, err := integration.OwnerConn(t).Exec(context.Background(), `
		INSERT INTO activity (kind, channel_provider, source, captured_by)
		VALUES ('message', 'no_such_transport', 'manual', 'test')`)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.ConstraintName != "activity_channel_provider_fkey" {
		t.Fatalf("insert failed with %v, want a foreign_key_violation on activity_channel_provider_fkey specifically — "+
			"any other failure (a bad column, a grammar refusal) would pass this test for the wrong reason", err)
	}
}
