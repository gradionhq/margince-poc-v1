// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The list sort/cursor machinery's branch-coverage sweep (arc 2a-ii,
// task 4): customfields_vocab_integration_test.go proves the cf_
// vocabulary's day-one behavior; this file reaches the storekit
// listquery.go branches only a less common shape exercises — the
// default-sort cursor continuation, a genuinely malformed (non-decoding)
// token, a cursor reused under a DIFFERENT custom sort (not just the
// default), the NULL-tail-to-NULL-tail continuation, a descending
// custom sort's own cursor walk, and the two core vocabulary kinds the
// day-one suite never sorts or filters by (owner_id's uuid, created_at's
// timestamptz) plus the currency/date/boolean filter-value refusals.

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/modules/customfields"
	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// TestCustomFieldVocab_SortErrorMessageIsActionable: SortError.Error()
// names both the offending field and the machine code, not just the
// bare code — the message a log line or a debugging session actually
// reads.
func TestCustomFieldVocab_SortErrorMessageIsActionable(t *testing.T) {
	f := setupCFV(t)
	bogus := "not_a_real_field"
	_, _, err := f.store.ListPeople(f.ctx, people.ListPeopleInput{Sort: &bogus})
	var sortErr *storekit.SortError
	if !errors.As(err, &sortErr) {
		t.Fatalf("err = %v, want SortError", err)
	}
	msg := sortErr.Error()
	if !strings.Contains(msg, bogus) || !strings.Contains(msg, storekit.CodeSortFieldNotAllowed) {
		t.Fatalf("Error() = %q, want it to name the field and the code", msg)
	}
}

// TestCustomFieldVocab_DefaultSortPaginatesWithCursor: the DEFAULT sort
// (-created_at,id, no explicit Sort) also keyset-paginates through its
// own cursor shape — the nil-ListSort branch of EncodePageCursor and
// KeysetClause that a cf_-sorted walk never exercises.
func TestCustomFieldVocab_DefaultSortPaginatesWithCursor(t *testing.T) {
	f := setupCFV(t)
	first, err := f.store.CreatePerson(f.ctx, people.CreatePersonInput{FullName: "First", Source: "ui"})
	if err != nil {
		t.Fatalf("CreatePerson: %v", err)
	}
	second, err := f.store.CreatePerson(f.ctx, people.CreatePersonInput{FullName: "Second", Source: "ui"})
	if err != nil {
		t.Fatalf("CreatePerson: %v", err)
	}

	one := 1
	page1, info1, err := f.store.ListPeople(f.ctx, people.ListPeopleInput{Limit: &one})
	if err != nil {
		t.Fatalf("ListPeople page 1: %v", err)
	}
	if len(page1) != 1 || page1[0].Id != second.Id || !info1.HasMore {
		t.Fatalf("page 1 = %+v (more=%v), want [second] with more", page1, info1.HasMore)
	}

	page2, info2, err := f.store.ListPeople(f.ctx, people.ListPeopleInput{Limit: &one, Cursor: &info1.NextCursor})
	if err != nil {
		t.Fatalf("ListPeople page 2: %v", err)
	}
	if len(page2) != 1 || page2[0].Id != first.Id || info2.HasMore {
		t.Fatalf("page 2 = %+v (more=%v), want [first] with no more", page2, info2.HasMore)
	}
}

// TestCustomFieldVocab_MalformedTokenAnswersDecodeError: a cursor string
// that does not even base64url-decode is the same client fault as one
// whose sort key mismatches its column's type — refused before the query
// ever runs, never a 500.
func TestCustomFieldVocab_MalformedTokenAnswersDecodeError(t *testing.T) {
	f := setupCFV(t)
	garbage := "not-a-valid-base64url-token!!"
	_, _, err := f.store.ListPeople(f.ctx, people.ListPeopleInput{Cursor: &garbage})
	var malformed *storekit.MalformedCursorError
	if !errors.As(err, &malformed) {
		t.Fatalf("err = %v, want MalformedCursorError", err)
	}
}

// TestCustomFieldVocab_CursorMintedUnderOneCustomSortRefusedUnderAnother:
// a token minted under a cf_-sorted list is refused not only when reused
// on the default list (already covered), but also when reused under a
// DIFFERENT custom sort — same field, opposite direction — since the
// keyset tuple it carries cannot continue that ordering either.
func TestCustomFieldVocab_CursorMintedUnderOneCustomSortRefusedUnderAnother(t *testing.T) {
	f := setupDealCFV(t)
	score := f.defineDealField(t, customfields.FieldSpec{Object: "deal", Label: "Score", Type: customfields.TypeNumber, Source: "ui"})
	f.seedScoredDeal(t, "A", map[string]any{score: float64(1)})
	f.seedScoredDeal(t, "B", map[string]any{score: float64(2)})

	one := 1
	asc := score
	_, page := f.listDealIDs(t, deals.ListDealsInput{Sort: &asc, Limit: &one})
	if !page.HasMore || page.NextCursor == "" {
		t.Fatalf("expected a sorted next-page cursor, got %+v", page)
	}

	desc := "-" + score
	_, _, err := f.store.ListDeals(f.ctx, deals.ListDealsInput{Sort: &desc, Cursor: &page.NextCursor})
	var mismatch *storekit.CursorSortMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("cursor reused under the opposite direction err = %v, want CursorSortMismatchError", err)
	}
}

// TestCustomFieldVocab_SortPaginatesThroughNullTailContinuation: with two
// NULL-valued rows in the tail, the cursor minted at the FIRST null row
// (whose own sort key is nil) must still continue correctly into the
// second — the one continuation shape TestCustomFieldVocab_SortPaginatesStably
// (a single trailing NULL) cannot reach, since there is no further page
// once the lone NULL row is served.
func TestCustomFieldVocab_SortPaginatesThroughNullTailContinuation(t *testing.T) {
	f := setupDealCFV(t)
	score := f.defineDealField(t, customfields.FieldSpec{Object: "deal", Label: "Score", Type: customfields.TypeNumber, Source: "ui"})
	a := f.seedScoredDeal(t, "A", map[string]any{score: float64(1)})
	b := f.seedScoredDeal(t, "B", nil)
	c := f.seedScoredDeal(t, "C", nil)
	// NULLS LAST; the two NULL rows tie-break created_at DESC (newest
	// first), so c (created after b) precedes b in the tail.
	want := []ids.UUID{a, c, b}

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
		cursor = &info.NextCursor
	}
	assertIDOrder(t, walked, want, "double-null-tail continuation")
}

// TestCustomFieldVocab_DescendingSortPaginatesStably: SortPaginatesStably
// only walks an ASCENDING cf_ sort; a descending sort's own cursor walk
// takes the other comparison direction in KeysetClause's continuation
// clause, so it needs its own coverage.
func TestCustomFieldVocab_DescendingSortPaginatesStably(t *testing.T) {
	f := setupDealCFV(t)
	score := f.defineDealField(t, customfields.FieldSpec{Object: "deal", Label: "Score", Type: customfields.TypeNumber, Source: "ui"})
	a := f.seedScoredDeal(t, "A", map[string]any{score: float64(1)})
	b := f.seedScoredDeal(t, "B", map[string]any{score: float64(2)})
	c := f.seedScoredDeal(t, "C", map[string]any{score: float64(3)})
	want := []ids.UUID{c, b, a}

	desc := "-" + score
	one := 1
	var walked []ids.UUID
	var cursor *string
	for page := 0; ; page++ {
		if page > len(want) {
			t.Fatalf("pagination did not terminate after %d pages (walked %v)", page, walked)
		}
		got, info := f.listDealIDs(t, deals.ListDealsInput{Sort: &desc, Limit: &one, Cursor: cursor})
		walked = append(walked, got...)
		if !info.HasMore {
			break
		}
		cursor = &info.NextCursor
	}
	assertIDOrder(t, walked, want, "descending paginated walk")
}

// TestCustomFieldVocab_FilterByCurrencyDateAndBooleanInvalidValues:
// FilterEqualityPerType already proves text/number/boolean equality;
// this proves the currency and date equality matches, plus the
// invalid-value 422 for currency, date, and boolean — each exercises a
// vocabulary kind branch FilterEqualityPerType and
// MalformedFilterValueRefused (number-only) never reach.
func TestCustomFieldVocab_FilterByCurrencyDateAndBooleanInvalidValues(t *testing.T) {
	f := setupDealCFV(t)
	eur := "EUR"
	budget := f.defineDealField(t, customfields.FieldSpec{Object: "deal", Label: "Budget", Type: customfields.TypeCurrency, Currency: &eur, Source: "ui"})
	renewal := f.defineDealField(t, customfields.FieldSpec{Object: "deal", Label: "Renewal", Type: customfields.TypeDate, Source: "ui"})
	strategic := f.defineDealField(t, customfields.FieldSpec{Object: "deal", Label: "Strategic", Type: customfields.TypeBoolean, Source: "ui"})

	match := f.seedScoredDeal(t, "Match", map[string]any{budget: float64(129900), renewal: "2026-07-11", strategic: true})
	f.seedScoredDeal(t, "NoMatch", map[string]any{budget: float64(50000), renewal: "2020-01-01", strategic: false})

	t.Run("currency equality", func(t *testing.T) {
		got, _ := f.listDealIDs(t, deals.ListDealsInput{CustomFilters: map[string]string{budget: "129900"}})
		assertIDOrder(t, got, []ids.UUID{match}, "currency equality")
	})
	t.Run("date equality", func(t *testing.T) {
		got, _ := f.listDealIDs(t, deals.ListDealsInput{CustomFilters: map[string]string{renewal: "2026-07-11"}})
		assertIDOrder(t, got, []ids.UUID{match}, "date equality")
	})

	invalid := map[string]map[string]string{
		"currency invalid value": {budget: "not-a-number"},
		"date invalid value":     {renewal: "not-a-date"},
		"boolean invalid value":  {strategic: "yes"},
	}
	for name, filters := range invalid {
		t.Run(name, func(t *testing.T) {
			_, _, err := f.store.ListDeals(f.ctx, deals.ListDealsInput{CustomFilters: filters})
			var pred *storekit.PredicateError
			if !errors.As(err, &pred) || pred.Code != storekit.CodeFilterValueInvalid {
				t.Fatalf("err = %v, want PredicateError %s", err, storekit.CodeFilterValueInvalid)
			}
		})
	}
}

// TestCustomFieldVocab_SortByOwnerIDWithCursor: owner_id is the one core
// vocabulary field of kind uuid (DM-VOCAB-1); sorting by it and
// continuing the page exercises the uuid branch of parsesAsKind and
// listBindCast in KeysetClause, which no cf_ column (the six closed
// types) ever reaches.
func TestCustomFieldVocab_SortByOwnerIDWithCursor(t *testing.T) {
	f := setupCFV(t)
	owner := ids.From[ids.UserKind](f.e.Rep1)
	first, err := f.store.CreatePerson(f.ctx, people.CreatePersonInput{FullName: "First", Source: "ui", OwnerID: &owner})
	if err != nil {
		t.Fatalf("CreatePerson: %v", err)
	}
	second, err := f.store.CreatePerson(f.ctx, people.CreatePersonInput{FullName: "Second", Source: "ui", OwnerID: &owner})
	if err != nil {
		t.Fatalf("CreatePerson: %v", err)
	}

	sortField := "owner_id"
	one := 1
	page1, info1, err := f.store.ListPeople(f.ctx, people.ListPeopleInput{Sort: &sortField, Limit: &one})
	if err != nil {
		t.Fatalf("ListPeople sort=owner_id page 1: %v", err)
	}
	// Both rows share the same owner_id, so the house tie-break
	// (created_at DESC) decides: second (created later) first.
	if len(page1) != 1 || page1[0].Id != second.Id || !info1.HasMore {
		t.Fatalf("page 1 = %+v (more=%v), want [second] with more", page1, info1.HasMore)
	}

	page2, info2, err := f.store.ListPeople(f.ctx, people.ListPeopleInput{Sort: &sortField, Limit: &one, Cursor: &info1.NextCursor})
	if err != nil {
		t.Fatalf("ListPeople sort=owner_id page 2: %v", err)
	}
	if len(page2) != 1 || page2[0].Id != first.Id || info2.HasMore {
		t.Fatalf("page 2 = %+v (more=%v), want [first] with no more", page2, info2.HasMore)
	}
}

// TestCustomFieldVocab_SortByCreatedAtWithCursor: created_at is a core
// vocabulary field of kind timestamptz sorted OUTSIDE the documented
// default spelling (a plain ascending "created_at", not "-created_at,id")
// — the one shape that drives a real Postgres-emitted timestamptz text
// key through parsesAsKind/listBindCast's timestamp branch.
func TestCustomFieldVocab_SortByCreatedAtWithCursor(t *testing.T) {
	f := setupCFV(t)
	first, err := f.store.CreatePerson(f.ctx, people.CreatePersonInput{FullName: "First", Source: "ui"})
	if err != nil {
		t.Fatalf("CreatePerson: %v", err)
	}
	second, err := f.store.CreatePerson(f.ctx, people.CreatePersonInput{FullName: "Second", Source: "ui"})
	if err != nil {
		t.Fatalf("CreatePerson: %v", err)
	}

	sortField := "created_at"
	one := 1
	page1, info1, err := f.store.ListPeople(f.ctx, people.ListPeopleInput{Sort: &sortField, Limit: &one})
	if err != nil {
		t.Fatalf("ListPeople sort=created_at page 1: %v", err)
	}
	if len(page1) != 1 || page1[0].Id != first.Id || !info1.HasMore {
		t.Fatalf("page 1 = %+v (more=%v), want [first] with more", page1, info1.HasMore)
	}

	page2, info2, err := f.store.ListPeople(f.ctx, people.ListPeopleInput{Sort: &sortField, Limit: &one, Cursor: &info1.NextCursor})
	if err != nil {
		t.Fatalf("ListPeople sort=created_at page 2: %v", err)
	}
	if len(page2) != 1 || page2[0].Id != second.Id || info2.HasMore {
		t.Fatalf("page 2 = %+v (more=%v), want [second] with no more", page2, info2.HasMore)
	}
}

// TestCustomFieldVocab_MalformedTimestampCursorKeyIsClientFault: a
// crafted cursor sorted by a timestamptz core field whose key does not
// parse under ANY of parsesAsTimestamptz's layouts is a malformed
// cursor at the store — proving the timestamp branch's failure path
// (parsesAsKind's other branches already prove theirs via the number
// suite's CraftedCursorKeyIsClientFault).
func TestCustomFieldVocab_MalformedTimestampCursorKeyIsClientFault(t *testing.T) {
	f := setupCFV(t)
	badKey := "not-a-timestamp"
	crafted := craftCursor(t, storekit.Cursor{
		CreatedAt: time.Now().UTC(), ID: ids.NewV7(), SortField: "created_at", SortKey: &badKey,
	})
	sortField := "created_at"
	_, _, err := f.store.ListPeople(f.ctx, people.ListPeopleInput{Sort: &sortField, Cursor: &crafted})
	var malformed *storekit.MalformedCursorError
	if !errors.As(err, &malformed) {
		t.Fatalf("crafted timestamp sort key err = %v, want MalformedCursorError", err)
	}
}
