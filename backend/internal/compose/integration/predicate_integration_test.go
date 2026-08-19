// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

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
	e := Setup(t)
	mineMatch := e.SeedPerson(t, "Anna Renewal", &e.Rep1)
	foreignMatch := e.SeedPerson(t, "Bruno Renewal", &e.Rep3)
	// A person is readable by every seat with the grant; capture privacy
	// is what keeps this one inside Rep3's row scope alone.
	e.MakeCapturePrivate(t, "person", foreignMatch, e.Rep3)
	mineOther := e.SeedPerson(t, "Clara Support", &e.Rep1)
	mineLiteral := e.SeedPerson(t, "Dora 100% Renewal", &e.Rep1)

	engine := storekit.Query{
		Table:     "person",
		Fields:    personFilterFields,
		BaseWhere: "t.archived_at IS NULL",
	}
	selectIDs := func(ctx context.Context, p storekit.Predicate) map[ids.UUID]bool {
		t.Helper()
		got := map[ids.UUID]bool{}
		err := database.WithWorkspaceTx(ctx, e.Pool, func(tx pgx.Tx) error {
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

	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, RepPerms)
	// The private capture's owner reads both: their own private row and the
	// colleague's shared one.
	captor := e.As(e.Rep3, []ids.UUID{e.Team2}, AdminPerms)
	contains := func(s string) storekit.Predicate {
		return storekit.Predicate{Field: "full_name", Op: storekit.OpContains, Value: s}
	}

	// Team-scoped rep: the filter matches three live rows, but the
	// capture-private match stays invisible — scope composes with the
	// predicate, the predicate never overrides it.
	got := selectIDs(rep, storekit.Predicate{And: []storekit.Predicate{contains("renewal")}})
	for id, want := range map[ids.UUID]bool{
		mineMatch: true, mineLiteral: true, foreignMatch: false, mineOther: false,
	} {
		if got[id] != want {
			t.Errorf("team-scoped contains(renewal) visibility of %s = %v, want %v", id, got[id], want)
		}
	}

	// The captor sees the same filter across both rows — the delta against
	// the rep's result IS the scope clause doing its work.
	if got := selectIDs(captor, contains("renewal")); !got[foreignMatch] || !got[mineMatch] {
		t.Errorf("captor contains(renewal) = %v, want both matches", got)
	}

	// A filter that names only the private row (owner_id = rep3) returns
	// nothing for the team-scoped rep: no out-seeing via filter.
	byForeignOwner := storekit.Predicate{Field: "owner_id", Op: storekit.OpEq, Value: e.Rep3.String()}
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
			{Field: "owner_id", Op: storekit.OpEq, Value: e.Rep1.String()},
			contains("anna"),
		}},
	}}
	got = selectIDs(rep, nested)
	if !got[mineOther] || !got[mineMatch] || got[foreignMatch] || got[mineLiteral] {
		t.Errorf("nested OR = %v, want {%s, %s}", got, mineOther, mineMatch)
	}

	// The COUNT is scoped the same way the page is, and it needs saying
	// separately: a count is an existence oracle. An unscoped one would answer
	// "812 rows match owner_id = <another team's rep>" while the page it labels
	// showed none, which tells the caller precisely what the row scope exists to
	// withhold. The rep-versus-captor delta below IS the scope clause, exactly
	// as it is for SelectIDs above.
	countMatching := func(ctx context.Context, p storekit.Predicate) int {
		t.Helper()
		var n int
		err := database.WithWorkspaceTx(ctx, e.Pool, func(tx pgx.Tx) error {
			var err error
			n, err = engine.CountMatching(ctx, tx, p)
			return err
		})
		if err != nil {
			t.Fatalf("predicate count: %v", err)
		}
		return n
	}

	if n := countMatching(rep, contains("renewal")); n != 2 {
		t.Errorf("team-scoped count(renewal) = %d, want 2 — the private match must not be counted", n)
	}
	if n := countMatching(captor, contains("renewal")); n != 3 {
		t.Errorf("captor count(renewal) = %d, want 3 across both owners", n)
	}
	if n := countMatching(rep, byForeignOwner); n != 0 {
		t.Errorf("team-scoped count on a foreign owner = %d, want 0 — a count must not confirm rows the caller cannot read", n)
	}
}
