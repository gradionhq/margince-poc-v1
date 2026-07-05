// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package storekit

// The predicate engine's compile contract, spelled as golden SQL: every
// operator, the grouping grammar, the grammar bounds, and — because a
// filter is user input on a security-sensitive surface — the rejection
// paths and LIKE-metacharacter escaping.

import (
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
)

// dealFields mirrors the deals slice of the data-model §13.5 allow-list,
// widened with one field per type so every typed branch has a column.
var dealFields = map[string]Field{
	"owner_id":            {Expr: "t.owner_id", Type: FieldID},
	"stage_id":            {Expr: "t.stage_id", Type: FieldID},
	"status":              {Expr: "t.status", Type: FieldPicklist},
	"title":               {Expr: "t.title", Type: FieldText},
	"amount_minor":        {Expr: "t.amount_minor", Type: FieldCurrency},
	"probability":         {Expr: "t.probability", Type: FieldNumber},
	"expected_close_date": {Expr: "t.expected_close_date", Type: FieldDate},
	"is_hot":              {Expr: "t.is_hot", Type: FieldBoolean},
}

const ownerUUID = "018f4a5e-0000-7000-8000-000000000001"

func compile(t *testing.T, p Predicate) (string, []any) {
	t.Helper()
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	sql, err := CompilePredicate(p, dealFields, arg)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	return sql, args
}

func leaf(field, op string, value any) Predicate {
	return Predicate{Field: field, Op: op, Value: value}
}

func TestCompileGoldenSQLPerOperator(t *testing.T) {
	cases := []struct {
		name     string
		p        Predicate
		wantSQL  string
		wantArgs []any
	}{
		{"eq id", leaf("owner_id", OpEq, ownerUUID), "t.owner_id = $1", []any{ownerUUID}},
		{"neq picklist", leaf("status", OpNeq, "lost"), "t.status <> $1", []any{"lost"}},
		{"gt currency", leaf("amount_minor", OpGt, 5000.0), "t.amount_minor > $1", []any{5000.0}},
		{"gte number int accepted", leaf("probability", OpGte, 40), "t.probability >= $1", []any{40.0}},
		{"lt date", leaf("expected_close_date", OpLt, "2026-09-01"), "t.expected_close_date < $1", []any{"2026-09-01"}},
		{"lte number", leaf("probability", OpLte, 90.5), "t.probability <= $1", []any{90.5}},
		{"in picklist", leaf("status", OpIn, []any{"open", "won"}), "t.status = ANY($1)", []any{[]string{"open", "won"}}},
		{"in currency", leaf("amount_minor", OpIn, []any{100.0, 200.0}), "t.amount_minor = ANY($1)", []any{[]float64{100, 200}}},
		{"contains text", leaf("title", OpContains, "acme"), "t.title ILIKE $1", []any{"%acme%"}},
		{"exists true", leaf("owner_id", OpExists, true), "t.owner_id IS NOT NULL", nil},
		{"exists false", leaf("owner_id", OpExists, false), "t.owner_id IS NULL", nil},
		{"eq boolean", leaf("is_hot", OpEq, true), "t.is_hot = $1", []any{true}},
		{"eq date", leaf("expected_close_date", OpEq, "2026-12-31"), "t.expected_close_date = $1", []any{"2026-12-31"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sql, args := compile(t, tc.p)
			if sql != tc.wantSQL {
				t.Errorf("sql = %q, want %q", sql, tc.wantSQL)
			}
			if !reflect.DeepEqual(args, tc.wantArgs) {
				t.Errorf("args = %#v, want %#v", args, tc.wantArgs)
			}
		})
	}
}

func TestCompileNestedGroupsGolden(t *testing.T) {
	p := Predicate{And: []Predicate{
		leaf("status", OpEq, "open"),
		{Or: []Predicate{
			leaf("amount_minor", OpGt, 100000.0),
			{And: []Predicate{
				leaf("owner_id", OpExists, false),
				leaf("title", OpContains, "renewal"),
			}},
		}},
	}}
	sql, args := compile(t, p)
	want := "(t.status = $1 AND (t.amount_minor > $2 OR (t.owner_id IS NULL AND t.title ILIKE $3)))"
	if sql != want {
		t.Errorf("sql = %q, want %q", sql, want)
	}
	wantArgs := []any{"open", 100000.0, "%renewal%"}
	if !reflect.DeepEqual(args, wantArgs) {
		t.Errorf("args = %#v, want %#v", args, wantArgs)
	}
}

func TestCompileComposesWithPreexistingArgs(t *testing.T) {
	// Callers accumulate scope/base args in the same list; positions must
	// continue from wherever the caller already is, never restart at $1.
	args := []any{"already-bound"}
	arg := func(v any) int { args = append(args, v); return len(args) }
	sql, err := CompilePredicate(leaf("status", OpEq, "open"), dealFields, arg)
	if err != nil {
		t.Fatal(err)
	}
	if sql != "t.status = $2" {
		t.Errorf("sql = %q, want t.status = $2", sql)
	}
}

func TestCompileIsDeterministic(t *testing.T) {
	p := Predicate{Or: []Predicate{
		leaf("status", OpIn, []any{"open", "won"}),
		leaf("amount_minor", OpGte, 100.0),
	}}
	first, firstArgs := compile(t, p)
	second, secondArgs := compile(t, p)
	if first != second || !reflect.DeepEqual(firstArgs, secondArgs) {
		t.Errorf("same tree compiled differently: %q / %q", first, second)
	}
}

func TestContainsEscapesLikeMetacharacters(t *testing.T) {
	_, args := compile(t, leaf("title", OpContains, `100%_of\it`))
	want := `%100\%\_of\\it%`
	if len(args) != 1 || args[0] != want {
		t.Errorf("bound operand = %#v, want %q", args, want)
	}
}

func TestCompileDepthBound(t *testing.T) {
	// Depth 4 (three nested groups around a leaf's group) is the last
	// admitted shape; one more group is rejected as too deep.
	nested := func(depth int) Predicate {
		p := Predicate{And: []Predicate{leaf("status", OpEq, "open")}}
		for i := 1; i < depth; i++ {
			p = Predicate{And: []Predicate{p}}
		}
		return p
	}
	if _, err := CompilePredicate(nested(PredicateMaxDepth), dealFields, discardArg()); err != nil {
		t.Errorf("depth %d rejected: %v, want accepted", PredicateMaxDepth, err)
	}
	requirePredicateError(t, nested(PredicateMaxDepth+1), CodeFilterTooDeep)
}

func TestCompileLeafCountBound(t *testing.T) {
	wide := func(n int) Predicate {
		p := Predicate{And: make([]Predicate, n)}
		for i := range p.And {
			p.And[i] = leaf("status", OpEq, "open")
		}
		return p
	}
	if _, err := CompilePredicate(wide(PredicateMaxLeaves), dealFields, discardArg()); err != nil {
		t.Errorf("%d leaves rejected: %v, want accepted", PredicateMaxLeaves, err)
	}
	requirePredicateError(t, wide(PredicateMaxLeaves+1), CodeFilterTooLarge)
}

func TestCompileRejectsInvalidShapes(t *testing.T) {
	cases := []struct {
		name string
		p    Predicate
		code string
	}{
		{"unknown field", leaf("password_hash", OpEq, "x"), CodeFilterFieldNotAllowed},
		{"unknown operator", leaf("status", "regex", "x"), CodeFilterOpNotAllowed},
		{"op wrong for type: gt on picklist", leaf("status", OpGt, "open"), CodeFilterOpNotAllowed},
		{"op wrong for type: contains on number", leaf("probability", OpContains, "4"), CodeFilterOpNotAllowed},
		{"op wrong for type: in on date", leaf("expected_close_date", OpIn, []any{"2026-01-01"}), CodeFilterOpNotAllowed},
		{"empty node", Predicate{}, CodeFilterShapeInvalid},
		{"empty group", Predicate{And: []Predicate{}, Or: nil, Field: "", Op: "", Value: nil}, CodeFilterShapeInvalid},
		{"empty nested group", Predicate{And: []Predicate{{Or: []Predicate{}}}}, CodeFilterShapeInvalid},
		{"both and+or", Predicate{And: []Predicate{leaf("status", OpEq, "x")}, Or: []Predicate{leaf("status", OpEq, "y")}}, CodeFilterShapeInvalid},
		{"group mixed with leaf", Predicate{And: []Predicate{leaf("status", OpEq, "x")}, Field: "status", Op: OpEq, Value: "y"}, CodeFilterShapeInvalid},
		{"eq with nil value", leaf("status", OpEq, nil), CodeFilterValueInvalid},
		{"eq string on number", leaf("probability", OpEq, "high"), CodeFilterValueInvalid},
		{"eq number on text", leaf("title", OpEq, 7.0), CodeFilterValueInvalid},
		{"malformed uuid", leaf("owner_id", OpEq, "not-a-uuid"), CodeFilterValueInvalid},
		{"malformed date", leaf("expected_close_date", OpEq, "31/12/2026"), CodeFilterValueInvalid},
		{"NaN number", leaf("probability", OpEq, nan()), CodeFilterValueInvalid},
		{"exists non-bool", leaf("owner_id", OpExists, "yes"), CodeFilterValueInvalid},
		{"boolean non-bool", leaf("is_hot", OpEq, "true"), CodeFilterValueInvalid},
		{"in empty list", leaf("status", OpIn, []any{}), CodeFilterValueInvalid},
		{"in non-array", leaf("status", OpIn, "open"), CodeFilterValueInvalid},
		{"in mixed types", leaf("status", OpIn, []any{"open", 3.0}), CodeFilterValueInvalid},
		{"in bad uuid member", leaf("owner_id", OpIn, []any{ownerUUID, "nope"}), CodeFilterValueInvalid},
		{"contains empty string", leaf("title", OpContains, ""), CodeFilterValueInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			requirePredicateError(t, tc.p, tc.code)
		})
	}
}

func TestCompileInListSizeBound(t *testing.T) {
	over := make([]any, PredicateMaxInValues+1)
	for i := range over {
		over[i] = "v"
	}
	requirePredicateError(t, leaf("status", OpIn, over), CodeFilterTooLarge)
}

// TestCompileNeverInlinesValues sweeps every accepted golden case: no
// user-supplied value may appear in the SQL text — the injection gate.
func TestCompileNeverInlinesValues(t *testing.T) {
	hostile := `'; DROP TABLE deal; --`
	sql, args := compile(t, Predicate{And: []Predicate{
		leaf("title", OpContains, hostile),
		leaf("status", OpEq, hostile),
	}})
	if strings.Contains(sql, "DROP") {
		t.Fatalf("value leaked into SQL text: %q", sql)
	}
	if len(args) != 2 {
		t.Fatalf("args = %d, want 2 bind parameters", len(args))
	}
}

func requirePredicateError(t *testing.T, p Predicate, wantCode string) {
	t.Helper()
	_, err := CompilePredicate(p, dealFields, discardArg())
	var perr *PredicateError
	if !errors.As(err, &perr) {
		t.Fatalf("err = %v, want a *PredicateError(%s)", err, wantCode)
	}
	if perr.Code != wantCode {
		t.Errorf("code = %s, want %s", perr.Code, wantCode)
	}
	if perr.Message == "" {
		t.Error("validation message is empty — the 422 body would be useless")
	}
}

func discardArg() func(any) int {
	n := 0
	return func(any) int { n++; return n }
}

func nan() float64 { return math.NaN() }
