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
	"regexp"
	"testing"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/collections"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/platform/database"
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

func TestDynamicList_membershipIsRowScopedToTheCaller(t *testing.T) {
	e := Setup(t)
	store := collections.NewStore(e.DB())

	mine := e.SeedPerson(t, "Mine Renewal", &e.Rep1)
	// A teammate's PRIVATE capture: a person who is merely owned is readable
	// by every seat with the grant, so capture privacy is what keeps this one
	// inside Rep2's row scope alone — and Rep2 shares Rep1's team, so the
	// list itself stays readable to both.
	private := e.SeedPerson(t, "Private Renewal", &e.Rep2)
	e.MakeCapturePrivate(t, "person", private, e.Rep2)

	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, collectionsPerms())

	// One filter, matching BOTH owners.
	created, err := store.CreateList(rep, collections.CreateListInput{
		Name: "Owned by rep1 or rep2", EntityType: "person", ListType: "dynamic",
		Definition: map[string]any{
			"field": "owner_id", "op": "in",
			"value": []any{e.Rep1.String(), e.Rep2.String()},
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

	// Rep1's segment includes their own match and EXCLUDES the private
	// capture, even though the filter names its owner — the predicate
	// narrows visibility, it never widens it.
	got := members(rep)
	if !got[mine] || got[private] {
		t.Errorf("rep segment = %v, want mine=%s present, private=%s absent", got, mine, private)
	}

	// The captor sees both: the delta IS the scope clause working.
	captor := members(e.As(e.Rep2, []ids.UUID{e.Team1}, collectionsPerms()))
	if !captor[mine] || !captor[private] {
		t.Errorf("captor segment = %v, want both matches", captor)
	}
}

func TestDynamicList_reEvaluatesLiveAsRecordsChange(t *testing.T) {
	e := Setup(t)
	store := collections.NewStore(e.DB())
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
	if _, err := e.People.UpdatePerson(e.Admin(), PersonIDOf(p1), people.UpdatePersonInput{OwnerID: userIDPtr(&e.Rep2)}); err != nil {
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
	store := collections.NewStore(e.DB())
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
	store := collections.NewStore(e.DB())
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

// A saved view's filter is checked when it is WRITTEN: a view naming a field
// that is not filterable is refused by the surface that accepts it, not by an
// export the author reaches much later, if ever.
//
// Both write paths, because a create-time gate one PATCH walks around is not a
// gate. The other obligation these paths carry — that the vocabulary is
// resolved BEFORE the write transaction opens — is NOT pinned here, for two
// reasons that both have to hold: this store is built without a field
// catalogue, so SegmentEngine never reaches for a second connection at all,
// AND e.DB() is the shared multi-connection harness pool, so even wiring a
// catalogue in would not make a nested acquire fail. Both are why
// dynamicsegment_singleconn_integration_test.go owns that claim, over a pool
// capped at one connection.
func TestSavedView_filterIsValidatedWhenWrittenNotWhenExported(t *testing.T) {
	e := Setup(t)
	store := collections.NewStore(e.DB())
	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, collectionsPerms())
	unfilterable := map[string]any{"field": "secret_salary", "op": "eq", "value": "x"}

	assertRefused := func(what string, err error) {
		t.Helper()
		var pe *storekit.PredicateError
		if !errors.As(err, &pe) {
			t.Fatalf("%s: err = %v, want a PredicateError (422)", what, err)
		}
		if pe.Code != storekit.CodeFilterFieldNotAllowed {
			t.Errorf("%s: code = %q, want %q", what, pe.Code, storekit.CodeFilterFieldNotAllowed)
		}
		if pe.Field != "secret_salary" {
			t.Errorf("%s: field = %q, want the offending field named", what, pe.Field)
		}
	}

	_, err := store.CreateSavedView(rep, collections.CreateSavedViewInput{
		Resource: "people", Name: "Overpaid", Query: map[string]any{"filter": unfilterable},
	})
	assertRefused("create", err)

	// A view saved with a good filter cannot be PATCHed onto a bad one.
	good, err := store.CreateSavedView(rep, collections.CreateSavedViewInput{
		Resource: "people", Name: "Mine",
		Query: map[string]any{"filter": map[string]any{"field": "owner_id", "op": "eq", "value": e.Rep1.String()}},
	})
	if err != nil {
		t.Fatalf("create a valid view: %v", err)
	}
	replacement := map[string]any{"filter": unfilterable}
	_, err = store.UpdateSavedView(rep, good.ID, collections.UpdateSavedViewInput{Query: &replacement})
	assertRefused("update", err)

	// And the refusal left the stored view alone.
	reloaded, err := store.GetSavedView(rep, good.ID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if !jsonEqual(t, good.Query, reloaded.Query) {
		t.Errorf("the refused update still changed the stored query: %v", reloaded.Query)
	}
}

// An ARCHIVED view refuses a query replacement as not-found, and does it BEFORE
// the filter is examined. The order is the point: refusing the filter first
// would answer 422 for a view whose existence the caller is not entitled to
// learn, so a 422 here would be an existence oracle for archived views.
//
// This is the branch the update path's own comment claims and nothing exercised.
func TestSavedView_anArchivedViewIsNotFoundBeforeItsFilterIsJudged(t *testing.T) {
	e := Setup(t)
	store := collections.NewStore(e.DB())
	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, collectionsPerms())

	view, err := store.CreateSavedView(rep, collections.CreateSavedViewInput{
		Resource: "people", Name: "Retired",
		Query: map[string]any{"filter": map[string]any{"field": "owner_id", "op": "eq", "value": e.Rep1.String()}},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := store.ArchiveSavedView(rep, view.ID); err != nil {
		t.Fatalf("archive: %v", err)
	}

	// A filter that would ALSO be refused on its own merits, so a 422 could
	// only mean the archived check was skipped.
	unfilterable := map[string]any{"filter": map[string]any{"field": "secret_salary", "op": "eq", "value": "x"}}
	_, err = store.UpdateSavedView(rep, view.ID, collections.UpdateSavedViewInput{Query: &unfilterable})

	if !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound — a 422 here tells the caller an archived view exists", err)
	}
}

// The shape refusals on both write surfaces, each naming its own wire field.
//
// They are one line of validation apiece and were unexercised, which is how the
// wrong field name survived in them for as long as it did: a refusal nobody
// reads is a refusal nobody notices is wrong.
func TestListAndSavedViewShapeRefusalsNameTheirOwnField(t *testing.T) {
	e := Setup(t)
	store := collections.NewStore(e.DB())
	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, collectionsPerms())
	filter := map[string]any{"field": "owner_id", "op": "eq", "value": e.Rep1.String()}

	assertField := func(what string, err error, want string) {
		t.Helper()
		var bad *collections.BadInputError
		if !errors.As(err, &bad) {
			t.Fatalf("%s: err = %v, want a BadInputError", what, err)
		}
		if bad.Field != want {
			t.Errorf("%s: field = %q, want %q", what, bad.Field, want)
		}
	}

	// A dynamic list IS its definition, and a static one must not carry one —
	// the pair is what rules out a half-and-half list.
	_, err := store.CreateList(rep, collections.CreateListInput{
		Name: "Dynamic with nothing to evaluate", EntityType: "person", ListType: "dynamic",
	})
	assertField("a dynamic list with no definition", err, "definition")

	_, err = store.CreateList(rep, collections.CreateListInput{
		Name: "Static carrying a filter", EntityType: "person", ListType: "static",
		Definition: filter,
	})
	assertField("a static list carrying a definition", err, "definition")

	// A view's query is the whole view; null is not the same as an empty object,
	// which is a legitimate view with no state yet.
	_, err = store.CreateSavedView(rep, collections.CreateSavedViewInput{
		Resource: "people", Name: "No query at all",
	})
	assertField("a view with a null query", err, "query")

	// And a query replacement on a view the caller cannot see is not-found
	// rather than a validation error — existence-hiding survives the new
	// pre-read that the update path does to learn the resource.
	replacement := map[string]any{"filter": filter}
	_, err = store.UpdateSavedView(rep, ids.From[ids.SavedViewKind](ids.NewV7()),
		collections.UpdateSavedViewInput{Query: &replacement})
	if !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("updating an unknown view = %v, want ErrNotFound", err)
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

// checkLiteral pulls the single-quoted values out of a CHECK constraint
// definition; a column or type name never matches, only the literals do.
var checkLiteral = regexp.MustCompile(`'([a-z_]+)'`)

// The resource vocabulary against its real authority — the DDL — because the
// unit lane can only compare the contract enum to a Go map, and those two are
// not the ones that disagree.
//
// The two sets must be IDENTICAL in both directions. A value the CHECK stores
// and the contract does not declare is a row no client can name; a value the
// contract declares and the CHECK refuses passes contract validation, resolves
// its segment engine, validates its filter — and then trips the CHECK on
// INSERT, answering the caller that a value the schema they were handed calls
// legal is not allowed. `projects` was that second case until the CHECK was
// widened.
func TestSavedViewResourceCheckAgainstTheContractEnum(t *testing.T) {
	e := Setup(t)

	var def string
	err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(e.Admin(), `
			SELECT pg_get_constraintdef(oid) FROM pg_constraint
			 WHERE conrelid = 'saved_view'::regclass AND contype = 'c'
			   AND pg_get_constraintdef(oid) LIKE '%resource%'`).Scan(&def)
	})
	if err != nil {
		t.Fatalf("reading saved_view's own resource CHECK: %v", err)
	}
	storable := map[string]bool{}
	for _, m := range checkLiteral.FindAllStringSubmatch(def, -1) {
		storable[m[1]] = true
	}

	declared := map[string]bool{}
	for _, r := range []crmcontracts.SavedViewResource{
		crmcontracts.SavedViewResourceActivities, crmcontracts.SavedViewResourceDeals,
		crmcontracts.SavedViewResourceLeads, crmcontracts.SavedViewResourceOrganizations,
		crmcontracts.SavedViewResourcePartners, crmcontracts.SavedViewResourcePeople,
		crmcontracts.SavedViewResourceProjects,
	} {
		declared[string(r)] = true
	}

	for name := range storable {
		if !declared[name] {
			t.Errorf("the CHECK stores resource %q, which the contract does not declare — a row no client can name", name)
		}
	}
	for name := range declared {
		if !storable[name] {
			t.Errorf("the contract declares resource %q, which the CHECK refuses — a legal body that trips the INSERT", name)
		}
	}
}

// A view over the projects list is written by the real writer and read back:
// the filter is judged by the project segment engine and the row clears the
// resource CHECK.
func TestSavedViewOverProjectsIsStoredAndReadBack(t *testing.T) {
	e := Setup(t)
	store := collections.NewStore(e.DB())
	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, collectionsPerms())

	query := map[string]any{
		"columns": []any{"name", "key", "phase"},
		"filter":  map[string]any{"field": "phase", "op": "eq", "value": "delivering"},
	}
	created, err := store.CreateSavedView(rep, collections.CreateSavedViewInput{
		Resource: string(crmcontracts.SavedViewResourceProjects), Name: "In delivery", Query: query,
	})
	if err != nil {
		t.Fatalf("create a saved view over projects: %v", err)
	}
	got, err := store.GetSavedView(rep, created.ID)
	if err != nil {
		t.Fatalf("read the view back: %v", err)
	}
	if got.Resource != string(crmcontracts.SavedViewResourceProjects) {
		t.Errorf("resource = %q, want projects", got.Resource)
	}
	if !jsonEqual(t, query, got.Query) {
		t.Errorf("reloaded query = %v, want %v", got.Query, query)
	}
}
