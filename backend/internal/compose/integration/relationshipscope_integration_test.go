// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// An endpoint the visibility conjunction forgets is an endpoint that leaks.
//
// A relationship owns no owner_id: it is visible when EVERY non-null endpoint is
// visible, which auth.RelationshipEndpointScope renders as one EXISTS per endpoint
// column. That clause is the only thing standing between an edge and the records it
// names — an edge answered to someone who cannot read one of its ends discloses
// that record's existence AND its link to the other.
//
// The clause enumerates its columns, and it has to: each needs the table it points
// at, which no schema introspection supplies. So the hazard is a migration adding a
// sixth endpoint column that the clause never learns about — the edge would then be
// scoped by four of its five ends, and rows reachable through the new one would be
// visible to anyone. Nothing in the unit-test lane can see that, because the column
// list and the clause are the same source.
//
// This gate reads the DATABASE instead: every foreign key on `relationship` that
// points at a row-scoped record table must appear in the clause. It fails on the
// migration, not on the leak.

import (
	"sort"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// scopedEndpointTables are the record tables whose rows carry row scope, so a
// relationship column pointing at one of them MUST be in the conjunction. A
// foreign key to anything else (a workspace, a user, a catalog row) is not an
// endpoint whose visibility an edge inherits.
var scopedEndpointTables = map[string]bool{
	"person": true, "organization": true, "deal": true, "project": true, "lead": true,
}

// tenantColumn rides in every foreign key on a tenant table: the FKs here are
// composite, `(workspace_id, <endpoint>) REFERENCES <table>(workspace_id, id)`, so
// that a row can never point across tenants. It is not an endpoint — RLS owns it,
// and the conjunction would gain nothing by re-checking the workspace the GUC
// already binds — so it is excluded by name rather than by being forgotten.
const tenantColumn = "workspace_id"

func TestEveryScopedEndpointColumnIsInTheVisibilityConjunction(t *testing.T) {
	e := Setup(t)

	// Read the real foreign keys off the live schema. pg_constraint is the
	// authority — a column added by a migration appears here the moment it exists,
	// which is the whole point of asking the database rather than the Go list.
	type fk struct{ column, target string }
	var keys []fk
	ctx := e.Admin()
	if err := database.WithWorkspaceTx(ctx, e.Pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT att.attname, cl.relname
			  FROM pg_constraint con
			  JOIN pg_class src ON src.oid = con.conrelid
			  JOIN pg_class cl ON cl.oid = con.confrelid
			  JOIN unnest(con.conkey) AS k(attnum) ON true
			  JOIN pg_attribute att ON att.attrelid = con.conrelid AND att.attnum = k.attnum
			 WHERE con.contype = 'f' AND src.relname = 'relationship'`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var got fk
			if err := rows.Scan(&got.column, &got.target); err != nil {
				return err
			}
			keys = append(keys, got)
		}
		return rows.Err()
	}); err != nil {
		t.Fatalf("reading relationship's foreign keys: %v", err)
	}
	if len(keys) == 0 {
		t.Fatal("relationship declares no foreign keys — the introspection is reading the wrong table")
	}

	// The clause, rendered for a row-scoped caller so it is not the empty string
	// an unbounded principal gets.
	scoped := e.As(e.Rep1, []ids.UUID{e.Team1}, relationshipReaderPerms())
	var args []any
	clause, err := auth.RelationshipEndpointScope(scoped, "r", func(v any) int {
		args = append(args, v)
		return len(args)
	})
	if err != nil {
		t.Fatalf("rendering the endpoint scope: %v", err)
	}
	if clause == "" {
		t.Fatal("a row-scoped caller rendered an EMPTY clause — every edge would be visible to them")
	}

	missing := []string{}
	endpoints := 0
	for _, key := range keys {
		if !scopedEndpointTables[key.target] || key.column == tenantColumn {
			continue
		}
		endpoints++
		// The column has to be constrained BY NAME in the clause. Substring is
		// enough and deliberately crude: the clause is generated SQL, and what
		// this asserts is that the column was not forgotten, not how it is spelled.
		if !strings.Contains(clause, "r."+key.column) {
			missing = append(missing, key.column+" → "+key.target)
		}
	}
	// The walk must have SEEN the endpoints, or a rename that emptied it would read
	// as a clean pass. Five columns point at four record tables today; the floor is
	// that there is more than one, since a conjunction of one is not a conjunction.
	if endpoints < 2 {
		t.Fatalf("only %d scoped endpoint column(s) found on relationship — the introspection is not "+
			"reading the foreign keys this gate is about", endpoints)
	}
	sort.Strings(missing)
	for _, gap := range missing {
		t.Errorf("relationship.%s is a foreign key to a row-scoped table and does NOT appear in "+
			"auth.RelationshipEndpointScope's clause. An edge carrying it would be scoped by its other "+
			"endpoints only, so a caller who cannot read that row could still read the edge naming it — "+
			"and learn the row exists. Add the column to relationshipEndpointColumns.", gap)
	}
}
