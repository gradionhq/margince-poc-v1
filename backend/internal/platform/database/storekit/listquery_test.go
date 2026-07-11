// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package storekit

// The list sort/filter vocabulary mechanics (CF-T05 AC-OPEN-1): parsing
// and refusing sort specs against a per-resource vocabulary, the
// NULLS-LAST keyset clause a sorted page continues with, the sorted
// cursor round trip, and the cf_* equality filter clauses — every
// refusal a typed 422 code, every identifier quoted, every value a bind
// parameter.

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/fieldcatalog"
)

var testVocab = SortVocabulary(map[string]string{
	"created_at": KindTimestamp,
	"full_name":  fieldcatalog.TypeText,
}, []fieldcatalog.Column{
	{Name: "cf_score", Type: fieldcatalog.TypeNumber},
	{Name: "cf_strategic", Type: fieldcatalog.TypeBoolean},
})

func sortSpec(s string) *string { return &s }

func TestParseListSort_DefaultSpellings(t *testing.T) {
	for _, spec := range []*string{nil, sortSpec(""), sortSpec("-created_at,id")} {
		got, err := ParseListSort(spec, testVocab)
		if err != nil || got != nil {
			t.Fatalf("ParseListSort(%v) = %v, %v — want the nil default sort", spec, got, err)
		}
	}
}

func TestParseListSort_SingleField(t *testing.T) {
	asc, err := ParseListSort(sortSpec("cf_score"), testVocab)
	if err != nil || asc == nil || asc.desc || asc.name != "cf_score" {
		t.Fatalf("ascending cf_score: got %+v, %v", asc, err)
	}
	desc, err := ParseListSort(sortSpec("-full_name"), testVocab)
	if err != nil || desc == nil || !desc.desc || desc.name != "full_name" {
		t.Fatalf("descending full_name: got %+v, %v", desc, err)
	}
}

func TestParseListSort_OutOfVocabularyRefused(t *testing.T) {
	for _, spec := range []string{"owner_id", "cf_retired_or_unknown", "-cf_retired_or_unknown", "-"} {
		_, err := ParseListSort(sortSpec(spec), testVocab)
		var sortErr *SortError
		if !errors.As(err, &sortErr) || sortErr.Code != CodeSortFieldNotAllowed {
			t.Fatalf("ParseListSort(%q) err = %v, want SortError %s", spec, err, CodeSortFieldNotAllowed)
		}
	}
}

func TestParseListSort_MultiFieldRefused(t *testing.T) {
	_, err := ParseListSort(sortSpec("-created_at,full_name"), testVocab)
	var sortErr *SortError
	if !errors.As(err, &sortErr) || sortErr.Code != CodeSortUnsupported {
		t.Fatalf("multi-field sort err = %v, want SortError %s", err, CodeSortUnsupported)
	}
}

func TestListSort_OrderBy(t *testing.T) {
	var def *ListSort
	if got := def.OrderBy(); got != " ORDER BY created_at DESC, id DESC" {
		t.Fatalf("default OrderBy = %q", got)
	}
	asc, err := ParseListSort(sortSpec("cf_score"), testVocab)
	if err != nil {
		t.Fatal(err)
	}
	if got := asc.OrderBy(); got != ` ORDER BY "cf_score" ASC NULLS LAST, created_at DESC, id DESC` {
		t.Fatalf("ascending OrderBy = %q", got)
	}
	desc, err := ParseListSort(sortSpec("-cf_score"), testVocab)
	if err != nil {
		t.Fatal(err)
	}
	if got := desc.OrderBy(); got != ` ORDER BY "cf_score" DESC NULLS LAST, created_at DESC, id DESC` {
		t.Fatalf("descending OrderBy = %q", got)
	}
}

func TestListSort_CursorKeySuffix(t *testing.T) {
	var def *ListSort
	if got := def.CursorKeySuffix(); got != "" {
		t.Fatalf("default CursorKeySuffix = %q, want empty", got)
	}
	s, err := ParseListSort(sortSpec("cf_score"), testVocab)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.CursorKeySuffix(); got != `, ("cf_score")::text AS "__cursor_key"` {
		t.Fatalf("CursorKeySuffix = %q", got)
	}
}

// keysetArgs is the arg-registering closure every clause builder takes.
func keysetArgs() (func(any) int, *[]any) {
	args := &[]any{}
	return func(v any) int { *args = append(*args, v); return len(*args) }, args
}

func TestKeysetClause_DefaultCursorRoundTrip(t *testing.T) {
	at := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	id := ids.NewV7()
	var def *ListSort
	token := def.EncodePageCursor(nil, at, id)

	arg, args := keysetArgs()
	clause, err := def.KeysetClause(token, arg)
	if err != nil {
		t.Fatal(err)
	}
	if clause != "(created_at, id) < ($1, $2)" {
		t.Fatalf("clause = %q", clause)
	}
	if len(*args) != 2 || !(*args)[0].(time.Time).Equal(at) || (*args)[1] != id {
		t.Fatalf("args = %v", *args)
	}
}

func TestKeysetClause_SortedCursorRoundTrip(t *testing.T) {
	s, err := ParseListSort(sortSpec("cf_score"), testVocab)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	id := ids.NewV7()
	key := "42.5"
	token := s.EncodePageCursor(&key, at, id)

	arg, args := keysetArgs()
	clause, err := s.KeysetClause(token, arg)
	if err != nil {
		t.Fatal(err)
	}
	want := `("cf_score" > $1::numeric OR ("cf_score" = $1::numeric AND (created_at, id) < ($2, $3)) OR "cf_score" IS NULL)`
	if clause != want {
		t.Fatalf("clause = %q, want %q", clause, want)
	}
	if len(*args) != 3 || (*args)[0] != "42.5" {
		t.Fatalf("args = %v", *args)
	}
}

func TestKeysetClause_DescendingComparesBelow(t *testing.T) {
	s, err := ParseListSort(sortSpec("-cf_score"), testVocab)
	if err != nil {
		t.Fatal(err)
	}
	key := "10"
	token := s.EncodePageCursor(&key, time.Now().UTC(), ids.NewV7())
	arg, _ := keysetArgs()
	clause, err := s.KeysetClause(token, arg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(clause, `"cf_score" < $1::numeric`) {
		t.Fatalf("descending clause must continue BELOW the key, got %q", clause)
	}
}

func TestKeysetClause_NullKeyContinuesInsideNullTail(t *testing.T) {
	s, err := ParseListSort(sortSpec("cf_score"), testVocab)
	if err != nil {
		t.Fatal(err)
	}
	token := s.EncodePageCursor(nil, time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC), ids.NewV7())
	arg, _ := keysetArgs()
	clause, err := s.KeysetClause(token, arg)
	if err != nil {
		t.Fatal(err)
	}
	if clause != `("cf_score" IS NULL AND (created_at, id) < ($1, $2))` {
		t.Fatalf("null-key clause = %q", clause)
	}
}

// A cursor minted under one sort must not continue a differently-sorted
// list — the keyset tuple would be meaningless, so the token is refused
// with the mismatch's own type (the contract's cursor_param_mismatch,
// distinct from an undecodable token) rather than silently misordering
// the page.
func TestKeysetClause_SortMismatchIsTypedMismatch(t *testing.T) {
	sorted, err := ParseListSort(sortSpec("cf_score"), testVocab)
	if err != nil {
		t.Fatal(err)
	}
	descending, err := ParseListSort(sortSpec("-cf_score"), testVocab)
	if err != nil {
		t.Fatal(err)
	}
	var def *ListSort
	key := "1"
	at, id := time.Now().UTC(), ids.NewV7()

	cases := map[string]struct {
		mintedBy *ListSort
		readBy   *ListSort
	}{
		"sorted cursor on a default list":       {sorted, def},
		"default cursor on a sorted list":       {def, sorted},
		"ascending cursor on a descending sort": {sorted, descending},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			token := c.mintedBy.EncodePageCursor(&key, at, id)
			arg, _ := keysetArgs()
			_, err := c.readBy.KeysetClause(token, arg)
			var mismatch *CursorSortMismatchError
			if !errors.As(err, &mismatch) {
				t.Fatalf("err = %v, want CursorSortMismatchError", err)
			}
		})
	}
}

// A token whose JSON decodes but whose sort key does not parse as the
// sort column's kind is a malformed cursor — a crafted key must answer
// the client-fault type, never reach the typed bind cast where Postgres
// would fail the whole query (22P02 → 500).
func TestKeysetClause_UnparseableSortKeyIsMalformed(t *testing.T) {
	vocab := SortVocabulary(map[string]string{
		"created_at": KindTimestamp,
		"owner_id":   KindUUID,
	}, []fieldcatalog.Column{
		{Name: "cf_score", Type: fieldcatalog.TypeNumber},
		{Name: "cf_budget", Type: fieldcatalog.TypeCurrency},
		{Name: "cf_renewal", Type: fieldcatalog.TypeDate},
		{Name: "cf_strategic", Type: fieldcatalog.TypeBoolean},
	})
	at, id := time.Now().UTC(), ids.NewV7()

	for field, badKey := range map[string]string{
		"cf_score":     "abc",
		"cf_budget":    "12.50",
		"cf_renewal":   "July 11",
		"cf_strategic": "maybe",
		"owner_id":     "not-a-uuid",
		"created_at":   "yesterday",
	} {
		t.Run(field, func(t *testing.T) {
			s, err := ParseListSort(sortSpec(field), vocab)
			if err != nil {
				t.Fatal(err)
			}
			token := mintCursorToken(Cursor{CreatedAt: at, ID: id, SortField: field, SortKey: &badKey})
			arg, _ := keysetArgs()
			_, err = s.KeysetClause(token, arg)
			var malformed *MalformedCursorError
			if !errors.As(err, &malformed) {
				t.Fatalf("sort key %q under %s: err = %v, want MalformedCursorError", badKey, field, err)
			}
		})
	}
}

// The kinds the custom-column filter grammar does not cover — uuid and
// timestamptz core columns — accept exactly their Postgres text forms as
// cursor keys and bind under their own casts, so a legitimately minted
// token round-trips.
func TestKeysetClause_CoreKindKeysRoundTrip(t *testing.T) {
	vocab := SortVocabulary(map[string]string{
		"updated_at": KindTimestamp,
		"owner_id":   KindUUID,
	}, nil)
	at, id := time.Now().UTC(), ids.NewV7()

	for field, tc := range map[string]struct{ key, wantCast string }{
		"owner_id":   {key: ids.NewV7().String(), wantCast: "::uuid"},
		"updated_at": {key: "2026-07-11 12:00:00.123456+00", wantCast: "::timestamptz"},
	} {
		t.Run(field, func(t *testing.T) {
			s, err := ParseListSort(sortSpec(field), vocab)
			if err != nil {
				t.Fatal(err)
			}
			key := tc.key
			token := s.EncodePageCursor(&key, at, id)
			arg, _ := keysetArgs()
			clause, err := s.KeysetClause(token, arg)
			if err != nil {
				t.Fatalf("KeysetClause(%s key %q): %v", field, key, err)
			}
			if !strings.Contains(clause, "$1"+tc.wantCast) {
				t.Fatalf("clause = %q, want the %s bind cast", clause, tc.wantCast)
			}
		})
	}
}

func TestCustomFilterClauses_EqualityPerType(t *testing.T) {
	active := []fieldcatalog.Column{
		{Name: "cf_tier", Type: fieldcatalog.TypeText},
		{Name: "cf_score", Type: fieldcatalog.TypeNumber},
		{Name: "cf_strategic", Type: fieldcatalog.TypeBoolean},
		{Name: "cf_budget", Type: fieldcatalog.TypeCurrency},
		{Name: "cf_renewal", Type: fieldcatalog.TypeDate},
	}
	arg, args := keysetArgs()
	clauses, err := CustomFilterClauses(active, map[string]string{
		"cf_tier": "gold", "cf_score": "42.5", "cf_strategic": "true",
		"cf_budget": "129900", "cf_renewal": "2026-07-11",
	}, arg)
	if err != nil {
		t.Fatal(err)
	}
	// Deterministic (sorted) clause order, quoted identifiers, typed casts.
	want := []string{
		`"cf_budget" = $1::bigint`,
		`"cf_renewal" = $2::date`,
		`"cf_score" = $3::numeric`,
		`"cf_strategic" = $4::boolean`,
		`"cf_tier" = $5::text`,
	}
	if strings.Join(clauses, " AND ") != strings.Join(want, " AND ") {
		t.Fatalf("clauses = %v, want %v", clauses, want)
	}
	if len(*args) != 5 || (*args)[4] != "gold" {
		t.Fatalf("args = %v", *args)
	}
}

func TestCustomFilterClauses_UnknownOrRetiredRefused(t *testing.T) {
	arg, _ := keysetArgs()
	_, err := CustomFilterClauses([]fieldcatalog.Column{{Name: "cf_tier", Type: fieldcatalog.TypeText}},
		map[string]string{"cf_retired_or_unknown": "x"}, arg)
	var pred *PredicateError
	if !errors.As(err, &pred) || pred.Code != CodeFilterFieldNotAllowed {
		t.Fatalf("err = %v, want PredicateError %s", err, CodeFilterFieldNotAllowed)
	}
}

func TestCustomFilterClauses_MalformedValueRefused(t *testing.T) {
	active := []fieldcatalog.Column{
		{Name: "cf_score", Type: fieldcatalog.TypeNumber},
		{Name: "cf_strategic", Type: fieldcatalog.TypeBoolean},
		{Name: "cf_budget", Type: fieldcatalog.TypeCurrency},
		{Name: "cf_renewal", Type: fieldcatalog.TypeDate},
	}
	for name, bad := range map[string]string{
		"cf_score": "not-a-number", "cf_strategic": "maybe",
		"cf_budget": "12.50", "cf_renewal": "July 11",
	} {
		arg, _ := keysetArgs()
		_, err := CustomFilterClauses(active, map[string]string{name: bad}, arg)
		var pred *PredicateError
		if !errors.As(err, &pred) || pred.Code != CodeFilterValueInvalid {
			t.Fatalf("%s=%q err = %v, want PredicateError %s", name, bad, err, CodeFilterValueInvalid)
		}
	}
}

func TestCustomFilterClauses_EmptyFilterIsNoClause(t *testing.T) {
	arg, _ := keysetArgs()
	clauses, err := CustomFilterClauses(nil, nil, arg)
	if err != nil || clauses != nil {
		t.Fatalf("empty filters: got %v, %v", clauses, err)
	}
}
