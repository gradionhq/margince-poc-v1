// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// The predicate engine against real rows (B-E15.10a/b): a compiled
// AND/OR filter composed with the caller's row-scope clause returns
// exactly the matching visible rows — and a filter that names only
// another team's records returns nothing, because a predicate can
// narrow visibility but never widen it.

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

var personFilterFields = map[string]storekit.Field{
	"full_name": {Expr: "t.full_name", Type: storekit.FieldText},
	"owner_id":  {Expr: "t.owner_id", Type: storekit.FieldID},
}

func TestPredicateEngineFiltersRealRowsWithinRowScope(t *testing.T) {
	e := setupAuthz(t)
	mineMatch := e.seedPerson(t, "Anna Renewal", &e.rep1)
	foreignMatch := e.seedPerson(t, "Bruno Renewal", &e.rep3)
	mineOther := e.seedPerson(t, "Clara Support", &e.rep1)
	mineLiteral := e.seedPerson(t, "Dora 100% Renewal", &e.rep1)

	engine := storekit.Query{
		Table:     "person",
		Fields:    personFilterFields,
		BaseWhere: "t.archived_at IS NULL",
	}
	selectIDs := func(ctx context.Context, p storekit.Predicate) map[ids.UUID]bool {
		t.Helper()
		got := map[ids.UUID]bool{}
		err := database.WithWorkspaceTx(ctx, e.pool, func(tx pgx.Tx) error {
			matched, err := engine.SelectIDs(ctx, tx, p, 100)
			for _, id := range matched {
				got[id] = true
			}
			return err
		})
		if err != nil {
			t.Fatalf("predicate select: %v", err)
		}
		return got
	}

	rep := e.as(e.rep1, []ids.UUID{e.team1}, repPerms)
	contains := func(s string) storekit.Predicate {
		return storekit.Predicate{Field: "full_name", Op: storekit.OpContains, Value: s}
	}

	// Team-scoped rep: the filter matches three live rows, but the
	// foreign team's match stays invisible — scope composes with the
	// predicate, the predicate never overrides it.
	got := selectIDs(rep, storekit.Predicate{And: []storekit.Predicate{contains("renewal")}})
	for id, want := range map[ids.UUID]bool{
		mineMatch: true, mineLiteral: true, foreignMatch: false, mineOther: false,
	} {
		if got[id] != want {
			t.Errorf("team-scoped contains(renewal) visibility of %s = %v, want %v", id, got[id], want)
		}
	}

	// The unbounded admin sees the same filter across teams — the delta
	// against the rep's result IS the scope clause doing its work.
	if got := selectIDs(e.admin(), contains("renewal")); !got[foreignMatch] || !got[mineMatch] {
		t.Errorf("admin contains(renewal) = %v, want both teams' matches", got)
	}

	// A filter that names only the other team's rows (owner_id = rep3)
	// returns nothing for the team-scoped rep: no out-seeing via filter.
	byForeignOwner := storekit.Predicate{Field: "owner_id", Op: storekit.OpEq, Value: e.rep3.String()}
	if got := selectIDs(rep, byForeignOwner); len(got) != 0 {
		t.Errorf("team-scoped filter on foreign owner returned %v, want none", got)
	}

	// LIKE metacharacters in the operand match literally against real
	// rows: "100%" finds "Dora 100% Renewal" only, not every name.
	got = selectIDs(rep, contains("100%"))
	if !got[mineLiteral] || len(got) != 1 {
		t.Errorf("contains(100%%) = %v, want exactly the literal match %s", got, mineLiteral)
	}

	// Nested OR across both branches, still scope-bound.
	nested := storekit.Predicate{Or: []storekit.Predicate{
		contains("support"),
		{And: []storekit.Predicate{
			{Field: "owner_id", Op: storekit.OpEq, Value: e.rep1.String()},
			contains("anna"),
		}},
	}}
	got = selectIDs(rep, nested)
	if !got[mineOther] || !got[mineMatch] || got[foreignMatch] || got[mineLiteral] {
		t.Errorf("nested OR = %v, want {%s, %s}", got, mineOther, mineMatch)
	}
}
