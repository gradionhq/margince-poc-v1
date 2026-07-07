// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// Dynamic (smart) lists + saved views (B-E15.11 / B-E15.12) against real
// rows. The headline is the security one: a dynamic list's membership is
// the LIVE evaluation of its stored filter through the ONE predicate
// engine, and that evaluation composes the caller's row-scope clause — so
// a team-scoped rep never sees a matching record another team owns. Saved
// views round-trip their column/sort/filter state exactly and are
// strictly per-user.

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/collections"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// collectionsPerms extends the rep fixture with the list + saved_view
// object grants the segmentation surface needs, without mutating the
// shared RepPerms map.
func collectionsPerms() principal.Permissions {
	p := RepPerms
	obj := map[string]principal.ObjectGrant{}
	for k, v := range RepPerms.Objects {
		obj[k] = v
	}
	full := principal.ObjectGrant{Create: true, Read: true, Update: true, Delete: true}
	obj["list"] = full
	obj["saved_view"] = full
	p.Objects = obj
	return p
}

// adminCollectionsCtx is an unbounded (row_scope=all) principal that can
// read lists and people — the cross-team oracle the scope assertions
// measure the rep's narrowed view against.
func (e *Env) adminCollectionsCtx() context.Context {
	return e.As(ids.NewV7(), nil, principal.Permissions{
		RoleKeys: []string{"admin"},
		Objects: map[string]principal.ObjectGrant{
			"person": {Read: true},
			"list":   {Create: true, Read: true, Update: true, Delete: true},
		},
		RowScope: principal.RowScopeAll,
	})
}

func TestDynamicList_membershipIsRowScopedToTheCaller(t *testing.T) {
	e := Setup(t)
	store := collections.NewStore(e.Pool)

	mine := e.SeedPerson(t, "Mine Renewal", &e.Rep1)
	foreign := e.SeedPerson(t, "Foreign Renewal", &e.Rep3)

	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, collectionsPerms())

	// One filter, matching BOTH teams' owners.
	created, err := store.CreateList(rep, collections.CreateListInput{
		Name: "Owned by rep1 or rep3", EntityType: "person", ListType: "dynamic",
		Definition: map[string]any{
			"field": "owner_id", "op": "in",
			"value": []any{e.Rep1.String(), e.Rep3.String()},
		},
	})
	if err != nil {
		t.Fatalf("create dynamic list: %v", err)
	}

	members := func(ctx context.Context) map[ids.UUID]bool {
		t.Helper()
		rows, _, err := store.ListMembers(ctx, created.ID, 50, "")
		if err != nil {
			t.Fatalf("list members: %v", err)
		}
		got := map[ids.UUID]bool{}
		for _, m := range rows {
			got[m.EntityID] = true
		}
		return got
	}

	// The team-scoped rep's segment includes their own match and EXCLUDES
	// the foreign-team match, even though the filter names its owner — the
	// predicate narrows visibility, it never widens it.
	got := members(rep)
	if !got[mine] || got[foreign] {
		t.Errorf("rep segment = %v, want mine=%s present, foreign=%s absent", got, mine, foreign)
	}

	// The unbounded oracle sees both: the delta IS the scope clause working.
	oracle := members(e.adminCollectionsCtx())
	if !oracle[mine] || !oracle[foreign] {
		t.Errorf("admin segment = %v, want both teams' matches", oracle)
	}
}

func TestDynamicList_reEvaluatesLiveAsRecordsChange(t *testing.T) {
	e := Setup(t)
	store := collections.NewStore(e.Pool)
	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, collectionsPerms())

	created, err := store.CreateList(rep, collections.CreateListInput{
		Name: "Owned by rep1", EntityType: "person", ListType: "dynamic",
		Definition: map[string]any{"field": "owner_id", "op": "eq", "value": e.Rep1.String()},
	})
	if err != nil {
		t.Fatalf("create dynamic list: %v", err)
	}
	has := func(id ids.UUID) bool {
		t.Helper()
		rows, _, err := store.ListMembers(rep, created.ID, 50, "")
		if err != nil {
			t.Fatalf("list members: %v", err)
		}
		for _, m := range rows {
			if m.EntityID == id {
				return true
			}
		}
		return false
	}

	p1 := e.SeedPerson(t, "P1", &e.Rep1)
	if !has(p1) {
		t.Fatalf("a matching record is not in the segment without a refresh")
	}

	// Add a second matching record — it enters the segment live.
	p2 := e.SeedPerson(t, "P2", &e.Rep1)
	if !has(p2) {
		t.Errorf("a newly created matching record did not enter the segment")
	}

	// Reassign p1 so it no longer matches the filter (rep2 is on the same
	// team, so p1 stays VISIBLE — it leaves by the filter, not the scope).
	if _, err := e.People.UpdatePerson(e.Admin(), personIDOf(p1), people.UpdatePersonInput{OwnerID: userIDPtr(&e.Rep2)}); err != nil {
		t.Fatalf("reassign p1: %v", err)
	}
	if has(p1) {
		t.Errorf("a no-longer-matching record stayed in the segment")
	}
	if !has(p2) {
		t.Errorf("p2 should still match owner_id=rep1")
	}
}

func TestDynamicList_rejectsInvalidDefinition(t *testing.T) {
	e := Setup(t)
	store := collections.NewStore(e.Pool)
	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, collectionsPerms())

	assertCode := func(name string, def map[string]any, wantCode string) {
		t.Helper()
		_, err := store.CreateList(rep, collections.CreateListInput{
			Name: name, EntityType: "person", ListType: "dynamic", Definition: def,
		})
		var pe *storekit.PredicateError
		if !errors.As(err, &pe) {
			t.Fatalf("%s: err = %v, want PredicateError", name, err)
		}
		if pe.Code != wantCode {
			t.Errorf("%s: code = %q, want %q", name, pe.Code, wantCode)
		}
	}

	// A field outside the person vocabulary is rejected (422).
	assertCode("unknown field",
		map[string]any{"field": "secret_salary", "op": "eq", "value": "x"},
		storekit.CodeFilterFieldNotAllowed)

	// A tree nested past the bounded depth is rejected (422).
	deep := map[string]any{"field": "owner_id", "op": "eq", "value": e.Rep1.String()}
	for i := 0; i < storekit.PredicateMaxDepth+1; i++ {
		deep = map[string]any{"and": []any{deep}}
	}
	assertCode("too deep", deep, storekit.CodeFilterTooDeep)
}

func TestSavedView_roundTripsAndIsPerUser(t *testing.T) {
	e := Setup(t)
	store := collections.NewStore(e.Pool)
	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, collectionsPerms())

	query := map[string]any{
		"columns": []any{"full_name", "owner_id"},
		"sort":    []any{map[string]any{"field": "full_name", "dir": "asc"}},
		"filter":  map[string]any{"field": "owner_id", "op": "eq", "value": e.Rep1.String()},
	}
	created, err := store.CreateSavedView(rep, collections.CreateSavedViewInput{
		Resource: "people", Name: "My people", Query: query,
	})
	if err != nil {
		t.Fatalf("create saved view: %v", err)
	}

	// A save→reload restores columns, sort, and filter EXACTLY.
	got, err := store.GetSavedView(rep, created.ID)
	if err != nil {
		t.Fatalf("get saved view: %v", err)
	}
	if !jsonEqual(t, query, got.Query) {
		t.Errorf("reloaded query = %v, want %v", got.Query, query)
	}

	// Per-user: another user cannot see it (existence-hidden as 404).
	other := e.As(e.Rep3, []ids.UUID{e.Team2}, collectionsPerms())
	if _, err := store.GetSavedView(other, created.ID); !errors.Is(err, apperrors.ErrNotFound) {
		t.Errorf("cross-user get → %v, want ErrNotFound", err)
	}
	// …and their default list is unaffected by rep1's view.
	otherViews, _, err := store.ListSavedViews(other, nil, storekit.LiveOnly)
	if err != nil {
		t.Fatalf("other list views: %v", err)
	}
	if len(otherViews) != 0 {
		t.Errorf("another user sees %d views, want 0", len(otherViews))
	}

	// Update round-trips a new name under the optimistic-concurrency version.
	newName := "My renamed people"
	v := created.Version
	updated, err := store.UpdateSavedView(rep, created.ID, collections.UpdateSavedViewInput{
		Name: &newName, IfVersion: &v,
	})
	if err != nil {
		t.Fatalf("update saved view: %v", err)
	}
	if updated.Name != newName || updated.Version <= created.Version {
		t.Errorf("update = {name:%q version:%d}, want {%q, >%d}", updated.Name, updated.Version, newName, created.Version)
	}
	// A stale version is rejected, no change made.
	if _, err := store.UpdateSavedView(rep, created.ID, collections.UpdateSavedViewInput{
		Name: &newName, IfVersion: &v,
	}); !errors.Is(err, apperrors.ErrVersionSkew) {
		t.Errorf("stale update → %v, want ErrVersionSkew", err)
	}

	// Archive removes it from the owner's default list.
	if _, err := store.ArchiveSavedView(rep, created.ID); err != nil {
		t.Fatalf("archive saved view: %v", err)
	}
	live, _, err := store.ListSavedViews(rep, nil, storekit.LiveOnly)
	if err != nil {
		t.Fatalf("list views: %v", err)
	}
	for _, vw := range live {
		if vw.ID == created.ID {
			t.Errorf("archived view %s still appears in the live list", created.ID)
		}
	}

	// Write shape: create + update + archive each left an audit row.
	for _, action := range []string{"create", "update", "archive"} {
		n := e.WsCount(t, `SELECT count(*) FROM audit_log WHERE entity_type = 'saved_view' AND entity_id = $1 AND action = $2`,
			created.ID, action)
		if n == 0 {
			t.Errorf("no %q audit row for saved_view %s", action, created.ID)
		}
	}
}

// jsonEqual compares two values by their canonical JSON encoding, so a
// jsonb round-trip (which re-types numbers/arrays) does not defeat the
// exact-restore assertion.
func jsonEqual(t *testing.T, a, b map[string]any) bool {
	t.Helper()
	ab, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	bb, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	return string(ab) == string(bb)
}
