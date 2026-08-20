// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// What `ON CONFLICT DO NOTHING` reports, over a real database.
//
// This cannot be proven against a fake transaction: the whole question is what
// Postgres puts in the command tag when the conflict clause fires, and a fake
// Exec would be supplying the answer under test rather than checking it.

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/identity"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/settings"
)

// The SECOND attempt is the defect. The first insert proves nothing — it stores,
// as it would whatever the discard reporting did.
//
// Both directions are asserted, and separately: "the value did not change"
// passes against code that reports nothing, and "a discard was reported" passes
// against code that also overwrote. Only the pair says what happened.
func TestASecondSeedKeepsTheStoredValueAndSaysItDiscardedTheNewOne(t *testing.T) {
	e := Setup(t)
	ctx := e.Admin()

	const first, second = "The First Installation", "The Second Installation"

	var storedFirst, storedSecond bool
	if err := database.WithWorkspaceTx(ctx, e.Pool, func(tx pgx.Tx) error {
		// Through the real writer. A hand-inserted row would prove nothing about
		// the path bootstrap actually takes.
		if _, err := tx.Exec(context.Background(), `DELETE FROM setting WHERE key = $1`, identity.Name.Key()); err != nil {
			return err
		}
		var err error
		if storedFirst, err = settings.SeedValue(context.Background(), tx, identity.Name, first); err != nil {
			return err
		}
		storedSecond, err = settings.SeedValue(context.Background(), tx, identity.Name, second)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	if !storedFirst {
		t.Fatal("the first seed reported a discard on a key with no row; the fixture is wrong, not the product")
	}
	// Direction 1: the stored value survived. Read straight off the row rather
	// than through a Store, because the question is what the table holds, not
	// what a registry resolves.
	var got string
	if err := database.WithWorkspaceTx(ctx, e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT value #>> '{}' FROM setting WHERE key = $1`, identity.Name.Key()).Scan(&got)
	}); err != nil {
		t.Fatalf("reading the seeded name back: %v", err)
	}
	if got != first {
		t.Errorf("the second seed overwrote the stored value: got %q, want %q — DO NOTHING is deliberate and must stay", got, first)
	}
	// Direction 2: and it SAID so. This is the half #863 is about; without it an
	// operator whose margince.yaml was ignored has nothing to read.
	if storedSecond {
		t.Error("the second seed reported that it stored a value it did not store, so a caller cannot tell a discard from a write")
	}
}
