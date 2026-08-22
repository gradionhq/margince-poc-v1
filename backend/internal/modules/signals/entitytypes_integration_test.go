// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package signals

// The store pins the subject types a signal may carry (signalEntityTables)
// and the schema pins the same set in signal_entity_type_check. Neither is
// derived from the other, so this holds them together: a type the store
// admits and the CHECK refuses is a 500 on POST /signals, and one the CHECK
// admits that the store refuses is a subject no writer can ever file.

import (
	"context"
	"os"
	"regexp"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/testdb"
)

func TestSignalEntityTablesMatchTheSchemaCheck(t *testing.T) {
	dsn := os.Getenv("MARGINCE_TEST_DSN")
	if dsn == "" {
		t.Fatal("MARGINCE_TEST_DSN not set — run `make db-up` (integration tests fail loudly, they never skip)")
	}
	ctx := context.Background()
	owner, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := owner.Close(context.Background()); err != nil {
			t.Errorf("closing owner connection: %v", err)
		}
	})
	if err := testdb.EnsureSchema(ctx, owner); err != nil {
		t.Fatal(err)
	}
	var def string
	if err := owner.QueryRow(ctx, `
		SELECT pg_get_constraintdef(oid) FROM pg_constraint
		 WHERE conrelid = 'signal'::regclass AND conname = 'signal_entity_type_check'`).Scan(&def); err != nil {
		t.Fatalf("reading the subject-type constraint: %v", err)
	}
	// Postgres renders the set as `ARRAY['deal'::text, ...]`; the literals
	// inside it are the subject types the schema admits.
	var admitted []string
	for _, match := range regexp.MustCompile(`'([a-z_]+)'::text`).FindAllStringSubmatch(def, -1) {
		admitted = append(admitted, match[1])
	}
	slices.Sort(admitted)
	if pinned := SignalEntityTables(); !slices.Equal(admitted, pinned) {
		t.Fatalf("the schema CHECK admits %v and the store pins %v — widen both in one change:\n%s", admitted, pinned, def)
	}
}
