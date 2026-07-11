// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The sort/filter vocabulary extension (CF-T05 AC-OPEN-1, arc 2a-ii T3):
// ACTIVE custom columns join their object's sortable/filterable list
// vocabulary — sorting by a cf_ column orders and stable-paginates
// through the extended keyset cursor, filtering is a typed equality
// match, and a retired/unknown cf_ field is refused with the contract's
// 422 codes while the core vocabulary keeps working.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/modules/customfields"
	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// craftCursor forges the opaque page token a hostile client could send:
// the Cursor JSON shape, base64url-encoded — bypassing the store's own
// minting so the sort key can carry arbitrary text.
func craftCursor(t *testing.T, c storekit.Cursor) string {
	t.Helper()
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("marshaling crafted cursor: %v", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

// seedScoredDeal creates one deal carrying the given custom-field values.
func (f dealCFVFixture) seedScoredDeal(t *testing.T, name string, cf map[string]any) ids.UUID {
	t.Helper()
	d, err := f.store.CreateDeal(f.ctx, deals.CreateDealInput{
		Name: name, PipelineID: f.pipeline, StageID: f.stage, Source: "ui",
		CustomFields: cf,
	})
	if err != nil {
		t.Fatalf("CreateDeal %s: %v", name, err)
	}
	return ids.UUID(d.Id)
}

// listDealIDs runs one ListDeals call and returns the row ids in order.
func (f dealCFVFixture) listDealIDs(t *testing.T, in deals.ListDealsInput) ([]ids.UUID, storekit.Page) {
	t.Helper()
	rows, page, err := f.store.ListDeals(f.ctx, in)
	if err != nil {
		t.Fatalf("ListDeals(%+v): %v", in, err)
	}
	got := make([]ids.UUID, len(rows))
	for i, d := range rows {
		got[i] = ids.UUID(d.Id)
	}
	return got, page
}

func assertIDOrder(t *testing.T, got, want []ids.UUID, label string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: %d rows, want %d (%v)", label, len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s: row %d = %s, want %s (full: %v want %v)", label, i, got[i], want[i], got, want)
		}
	}
}

// TestCustomFieldVocab_SortByCustomColumn: sorting by an active number
// cf_ column orders by the typed column value — NULLS LAST under both
// directions, equal keys tie-broken by the house (created_at DESC, id
// DESC) tuple.
func TestCustomFieldVocab_SortByCustomColumn(t *testing.T) {
	f := setupDealCFV(t)
	score := f.defineDealField(t, customfields.FieldSpec{Object: "deal", Label: "Score", Type: customfields.TypeNumber, Source: "ui"})

	a := f.seedScoredDeal(t, "A", map[string]any{score: float64(2)})
	b := f.seedScoredDeal(t, "B", map[string]any{score: float64(2)})
	c := f.seedScoredDeal(t, "C", map[string]any{score: float64(10)})
	d := f.seedScoredDeal(t, "D", nil) // NULL: the tail under both directions

	asc := score
	got, _ := f.listDealIDs(t, deals.ListDealsInput{Sort: &asc})
	// Equal scores (a, b) tie-break created_at DESC: the later row first.
	assertIDOrder(t, got, []ids.UUID{b, a, c, d}, "ascending")

	desc := "-" + score
	got, _ = f.listDealIDs(t, deals.ListDealsInput{Sort: &desc})
	assertIDOrder(t, got, []ids.UUID{c, b, a, d}, "descending")
}

// TestCustomFieldVocab_SortPaginatesStably: walking a cf_-sorted list
// one row per page through the extended keyset cursor visits every row
// exactly once, in order — including the equal-key tie and the NULL
// tail (the cursor carries the sort key, so no row repeats or vanishes
// at a page boundary).
func TestCustomFieldVocab_SortPaginatesStably(t *testing.T) {
	f := setupDealCFV(t)
	score := f.defineDealField(t, customfields.FieldSpec{Object: "deal", Label: "Score", Type: customfields.TypeNumber, Source: "ui"})

	a := f.seedScoredDeal(t, "A", map[string]any{score: float64(2)})
	b := f.seedScoredDeal(t, "B", map[string]any{score: float64(2)})
	c := f.seedScoredDeal(t, "C", map[string]any{score: float64(10)})
	d := f.seedScoredDeal(t, "D", nil)
	want := []ids.UUID{b, a, c, d}

	one := 1
	var walked []ids.UUID
	var cursor *string
	for page := 0; ; page++ {
		if page > len(want) {
			t.Fatalf("pagination did not terminate after %d pages (walked %v)", page, walked)
		}
		got, info := f.listDealIDs(t, deals.ListDealsInput{Sort: &score, Limit: &one, Cursor: cursor})
		walked = append(walked, got...)
		if !info.HasMore {
			break
		}
		if info.NextCursor == "" {
			t.Fatal("HasMore without a next cursor")
		}
		cursor = &info.NextCursor
	}
	assertIDOrder(t, walked, want, "paginated walk")
}

// TestCustomFieldVocab_FilterEqualityPerType: cf_ filters are typed
// equality matches — text, number, and boolean — and compose with each
// other AND-wise.
func TestCustomFieldVocab_FilterEqualityPerType(t *testing.T) {
	f := setupDealCFV(t)
	tier := f.defineDealField(t, customfields.FieldSpec{Object: "deal", Label: "Tier", Type: customfields.TypeText, Source: "ui"})
	score := f.defineDealField(t, customfields.FieldSpec{Object: "deal", Label: "Score", Type: customfields.TypeNumber, Source: "ui"})
	strategic := f.defineDealField(t, customfields.FieldSpec{Object: "deal", Label: "Strategic", Type: customfields.TypeBoolean, Source: "ui"})

	gold := f.seedScoredDeal(t, "Gold", map[string]any{tier: "gold", score: float64(1.5), strategic: true})
	silver := f.seedScoredDeal(t, "Silver", map[string]any{tier: "silver", score: float64(2), strategic: false})

	cases := map[string]struct {
		filters map[string]string
		want    []ids.UUID
	}{
		"text equality":          {map[string]string{tier: "gold"}, []ids.UUID{gold}},
		"number equality":        {map[string]string{score: "2"}, []ids.UUID{silver}},
		"boolean equality":       {map[string]string{strategic: "true"}, []ids.UUID{gold}},
		"filters compose ANDed":  {map[string]string{tier: "gold", strategic: "false"}, nil},
		"no match is empty page": {map[string]string{tier: "bronze"}, nil},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, _ := f.listDealIDs(t, deals.ListDealsInput{CustomFilters: tc.filters})
			assertIDOrder(t, got, tc.want, name)
		})
	}
}

// TestCustomFieldVocab_RetiredAndUnknownRefused: a retired column leaves
// ActiveColumns — and therefore the vocabulary — so sorting or filtering
// by it answers the same typed 422 codes an unknown cf_ field gets.
func TestCustomFieldVocab_RetiredAndUnknownRefused(t *testing.T) {
	f := setupCFV(t)
	field, err := f.svc.Create(f.ctx, customfields.FieldSpec{Object: "person", Label: "Legacy Tier", Type: customfields.TypeText, Source: "ui"})
	if err != nil {
		t.Fatalf("defining field: %v", err)
	}
	col := *field.ColumnName
	if _, err := f.svc.Retire(f.ctx, ids.UUID(field.Id)); err != nil {
		t.Fatalf("Retire: %v", err)
	}

	for name, cf := range map[string]string{"retired": col, "unknown": "cf_never_defined"} {
		t.Run(name+" sort refused", func(t *testing.T) {
			_, _, err := f.store.ListPeople(f.ctx, people.ListPeopleInput{Sort: &cf})
			var sortErr *storekit.SortError
			if !errors.As(err, &sortErr) || sortErr.Code != storekit.CodeSortFieldNotAllowed {
				t.Fatalf("sort=%s err = %v, want SortError %s", cf, err, storekit.CodeSortFieldNotAllowed)
			}
		})
		t.Run(name+" filter refused", func(t *testing.T) {
			_, _, err := f.store.ListPeople(f.ctx, people.ListPeopleInput{CustomFilters: map[string]string{cf: "x"}})
			var pred *storekit.PredicateError
			if !errors.As(err, &pred) || pred.Code != storekit.CodeFilterFieldNotAllowed {
				t.Fatalf("filter %s err = %v, want PredicateError %s", cf, err, storekit.CodeFilterFieldNotAllowed)
			}
		})
	}
}

// vocabResource pairs one list's data-model §13.5 DM-VOCAB spec-fields
// table with the store call that sorts it.
type vocabResource struct {
	specFields []string
	list       func(sort string) error
}

// coreVocabResources builds the DM-VOCAB-1..3 sort tables, verbatim from
// the spec, each paired with its store's list call.
func coreVocabResources(dealCtx context.Context, f cfvFixture, dealStore *deals.Store) map[string]vocabResource {
	return map[string]vocabResource{
		"people (DM-VOCAB-1)": {
			specFields: []string{"created_at", "updated_at", "full_name", "owner_id"},
			list: func(sort string) error {
				_, _, err := f.store.ListPeople(f.ctx, people.ListPeopleInput{Sort: &sort})
				return err
			},
		},
		"organizations (DM-VOCAB-2)": {
			specFields: []string{"created_at", "updated_at", "display_name", "owner_id"},
			list: func(sort string) error {
				_, _, err := f.store.ListOrganizations(f.ctx, people.ListOrganizationsInput{Sort: &sort})
				return err
			},
		},
		"deals (DM-VOCAB-3)": {
			specFields: []string{"created_at", "updated_at", "amount_minor", "expected_close_date", "last_activity_at"},
			list: func(sort string) error {
				_, _, err := dealStore.ListDeals(dealCtx, deals.ListDealsInput{Sort: &sort})
				return err
			},
		},
	}
}

// assertSpecFieldsSortable proves every spec-listed field on every
// resource sorts in both directions.
func assertSpecFieldsSortable(t *testing.T, resources map[string]vocabResource) {
	t.Helper()
	for resource, r := range resources {
		for _, field := range r.specFields {
			for _, spec := range []string{field, "-" + field} {
				if err := r.list(spec); err != nil {
					t.Fatalf("%s sort=%s: %v — the spec table lists this field as sortable", resource, spec, err)
				}
			}
		}
	}
}

// assertFullNameAndDefaultSort proves full_name ordering (evidence the
// ORDER BY really runs) and that the documented default spelling stays
// accepted and newest-first.
func assertFullNameAndDefaultSort(t *testing.T, f cfvFixture) {
	t.Helper()
	zoe, err := f.store.CreatePerson(f.ctx, people.CreatePersonInput{FullName: "Zoe Last", Source: "ui"})
	if err != nil {
		t.Fatalf("CreatePerson: %v", err)
	}
	ada, err := f.store.CreatePerson(f.ctx, people.CreatePersonInput{FullName: "Ada First", Source: "ui"})
	if err != nil {
		t.Fatalf("CreatePerson: %v", err)
	}

	byName := "full_name"
	rows, _, err := f.store.ListPeople(f.ctx, people.ListPeopleInput{Sort: &byName})
	if err != nil {
		t.Fatalf("ListPeople sort=full_name: %v", err)
	}
	if len(rows) != 2 || rows[0].Id != ada.Id || rows[1].Id != zoe.Id {
		t.Fatalf("full_name ascending order wrong: %v", rows)
	}

	defaultSpelling := "-created_at,id"
	rows, _, err = f.store.ListPeople(f.ctx, people.ListPeopleInput{Sort: &defaultSpelling})
	if err != nil {
		t.Fatalf("ListPeople with the documented default spelling: %v", err)
	}
	if len(rows) != 2 || rows[0].Id != ada.Id {
		t.Fatalf("default sort must stay -created_at,id (newest first), got %v", rows)
	}
}

// TestCustomFieldVocab_CoreVocabulary: each list's core sortable
// vocabulary is exactly its data-model §13.5 DM-VOCAB table — every
// spec-listed field sorts in both directions (full_name ordering proves
// the ORDER BY), the documented default spelling stays accepted, and a
// real column the tables do not list — or a multi-field spec — is
// refused.
func TestCustomFieldVocab_CoreVocabulary(t *testing.T) {
	f := setupCFV(t)
	// The deal list shares f's Env (Setup rebuilds the schema, so one
	// per test) — its store rides the same pool and field catalog.
	dealStore := deals.NewStore(f.e.Pool).WithFieldCatalog(f.svc)
	dealCtx := f.e.As(f.e.Rep1, nil, dealCFVPerms)

	resources := coreVocabResources(dealCtx, f, dealStore)
	assertSpecFieldsSortable(t, resources)
	assertFullNameAndDefaultSort(t, f)

	// A real column DM-VOCAB-3 does not list stays outside the vocabulary:
	// deal name sorted in poc-1, and the spec table dropped it.
	dealName := "name"
	var sortErr *storekit.SortError
	if err := resources["deals (DM-VOCAB-3)"].list(dealName); !errors.As(err, &sortErr) || sortErr.Code != storekit.CodeSortFieldNotAllowed {
		t.Fatalf("deals sort=name err = %v, want SortError %s", err, storekit.CodeSortFieldNotAllowed)
	}

	multi := "-created_at,full_name"
	_, _, err := f.store.ListPeople(f.ctx, people.ListPeopleInput{Sort: &multi})
	if !errors.As(err, &sortErr) || sortErr.Code != storekit.CodeSortUnsupported {
		t.Fatalf("multi-field sort err = %v, want SortError %s", err, storekit.CodeSortUnsupported)
	}
}

// TestCustomFieldVocab_MalformedFilterValueRefused: a filter value that
// does not parse as the column's type is a typed 422, never a query
// error.
func TestCustomFieldVocab_MalformedFilterValueRefused(t *testing.T) {
	f := setupDealCFV(t)
	score := f.defineDealField(t, customfields.FieldSpec{Object: "deal", Label: "Score", Type: customfields.TypeNumber, Source: "ui"})

	_, _, err := f.store.ListDeals(f.ctx, deals.ListDealsInput{CustomFilters: map[string]string{score: "not-a-number"}})
	var pred *storekit.PredicateError
	if !errors.As(err, &pred) || pred.Code != storekit.CodeFilterValueInvalid {
		t.Fatalf("err = %v, want PredicateError %s", err, storekit.CodeFilterValueInvalid)
	}
}

// TestCustomFieldVocab_SortedCursorRefusedUnderOtherSort: a cursor
// minted under one ordering cannot continue another — the token is
// refused with the sort-mismatch type (the contract's
// cursor_param_mismatch) instead of silently mis-paging.
func TestCustomFieldVocab_SortedCursorRefusedUnderOtherSort(t *testing.T) {
	f := setupDealCFV(t)
	score := f.defineDealField(t, customfields.FieldSpec{Object: "deal", Label: "Score", Type: customfields.TypeNumber, Source: "ui"})
	f.seedScoredDeal(t, "A", map[string]any{score: float64(1)})
	f.seedScoredDeal(t, "B", map[string]any{score: float64(2)})

	one := 1
	_, page := f.listDealIDs(t, deals.ListDealsInput{Sort: &score, Limit: &one})
	if !page.HasMore || page.NextCursor == "" {
		t.Fatalf("expected a sorted next-page cursor, got %+v", page)
	}

	_, _, err := f.store.ListDeals(f.ctx, deals.ListDealsInput{Cursor: &page.NextCursor})
	var mismatch *storekit.CursorSortMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("sorted cursor on the default list err = %v, want CursorSortMismatchError", err)
	}
}

// TestCustomFieldVocab_CraftedCursorKeyIsClientFault: a well-formed
// token whose sort key does not parse as the sort column's type is
// refused as a malformed cursor at the store — it never reaches the
// typed bind cast where Postgres would fail the query (22P02 → 500).
func TestCustomFieldVocab_CraftedCursorKeyIsClientFault(t *testing.T) {
	f := setupDealCFV(t)
	score := f.defineDealField(t, customfields.FieldSpec{Object: "deal", Label: "Score", Type: customfields.TypeNumber, Source: "ui"})
	f.seedScoredDeal(t, "A", map[string]any{score: float64(1)})

	badKey := "abc"
	crafted := craftCursor(t, storekit.Cursor{
		CreatedAt: time.Now().UTC(), ID: ids.NewV7(), SortField: score, SortKey: &badKey,
	})
	_, _, err := f.store.ListDeals(f.ctx, deals.ListDealsInput{Sort: &score, Cursor: &crafted})
	var malformed *storekit.MalformedCursorError
	if !errors.As(err, &malformed) {
		t.Fatalf("crafted numeric sort key err = %v, want MalformedCursorError", err)
	}
}
