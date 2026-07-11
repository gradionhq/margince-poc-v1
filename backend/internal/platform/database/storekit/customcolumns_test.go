// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package storekit

// The customcolumns helper matrix: every one of the six closed field
// types, both directions (SQLValue on write, ExtractValues on read), the
// drop-on-mismatch rule, and the empty/missing-key edge cases — the
// unit-test half of the fieldcatalog cross-module seam (the catalog-read
// half is modules/customfields' integration suite).

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/gradionhq/margince/backend/internal/shared/ports/fieldcatalog"
)

func col(name, typ string) fieldcatalog.Column { return fieldcatalog.Column{Name: name, Type: typ} }

// numericFromString builds the pgtype.Numeric a real pgx read of a
// numeric column would hand ExtractValues, without a database — Numeric
// implements database/sql.Scanner over a decimal string.
func numericFromString(t *testing.T, s string) pgtype.Numeric {
	t.Helper()
	var n pgtype.Numeric
	if err := n.Scan(s); err != nil {
		t.Fatalf("numericFromString(%q): %v", s, err)
	}
	return n
}

func TestSelectSuffix(t *testing.T) {
	if got := SelectSuffix(nil); got != "" {
		t.Fatalf("empty columns: got %q, want empty string", got)
	}
	got := SelectSuffix([]fieldcatalog.Column{col("cf_a", fieldcatalog.TypeText), col("cf_b", fieldcatalog.TypeNumber)})
	want := `, "cf_a", "cf_b"`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestSelectSuffix_QuotesIdentifiers(t *testing.T) {
	// A column name that collides with a reserved word still round-trips
	// safely quoted — the identifier is always server-derived, but the
	// quoting still runs (defense in depth, matching modules/customfields'
	// own posture).
	got := SelectSuffix([]fieldcatalog.Column{col("cf_order", fieldcatalog.TypeText)})
	if got != `, "cf_order"` {
		t.Fatalf("got %q", got)
	}
}

func TestInsertColumns(t *testing.T) {
	active := []fieldcatalog.Column{
		col("cf_amount", fieldcatalog.TypeCurrency),
		col("cf_notes", fieldcatalog.TypeText),
		col("cf_score", fieldcatalog.TypeNumber),
	}
	rawExtra := map[string]any{
		"cf_amount":  float64(1250),
		"cf_notes":   "hello",
		"cf_unknown": "dropped: no matching active column",
		"cf_score":   "not-a-number", // present but wrong shape: dropped
	}
	cols, placeholders, args := InsertColumns(active, rawExtra, 3)
	wantCols := []string{`"cf_amount"`, `"cf_notes"`}
	if !reflect.DeepEqual(cols, wantCols) {
		t.Fatalf("cols = %v, want %v", cols, wantCols)
	}
	wantPlaceholders := []string{"$3", "$4"}
	if !reflect.DeepEqual(placeholders, wantPlaceholders) {
		t.Fatalf("placeholders = %v, want %v", placeholders, wantPlaceholders)
	}
	wantArgs := []any{int64(1250), "hello"}
	if !reflect.DeepEqual(args, wantArgs) {
		t.Fatalf("args = %v, want %v", args, wantArgs)
	}
}

func TestInsertColumns_EmptyWhenNoActiveColumnsOrNoMatches(t *testing.T) {
	cols, placeholders, args := InsertColumns(nil, map[string]any{"cf_x": "y"}, 1)
	if cols != nil || placeholders != nil || args != nil {
		t.Fatalf("no active columns must yield nil everything, got %v %v %v", cols, placeholders, args)
	}
	active := []fieldcatalog.Column{col("cf_missing", fieldcatalog.TypeText)}
	cols, placeholders, args = InsertColumns(active, map[string]any{}, 1)
	if cols != nil || placeholders != nil || args != nil {
		t.Fatalf("a rawExtra with no matching key must yield nil everything, got %v %v %v", cols, placeholders, args)
	}
}

func TestUpdateSetClauses(t *testing.T) {
	active := []fieldcatalog.Column{
		col("cf_active", fieldcatalog.TypeBoolean),
		col("cf_due", fieldcatalog.TypeDate),
	}
	updates := map[string]any{"cf_active": true, "cf_due": "2026-07-11"}
	clauses, args := UpdateSetClauses(active, updates, 5)
	wantClauses := []string{`"cf_active" = $5`, `"cf_due" = $6`}
	if !reflect.DeepEqual(clauses, wantClauses) {
		t.Fatalf("clauses = %v, want %v", clauses, wantClauses)
	}
	wantArgs := []any{true, "2026-07-11"}
	if !reflect.DeepEqual(args, wantArgs) {
		t.Fatalf("args = %v, want %v", args, wantArgs)
	}
}

func TestUpdateSetClauses_MismatchedShapeDropped(t *testing.T) {
	active := []fieldcatalog.Column{col("cf_active", fieldcatalog.TypeBoolean)}
	clauses, args := UpdateSetClauses(active, map[string]any{"cf_active": "not-a-bool"}, 1)
	if clauses != nil || args != nil {
		t.Fatalf("a wrong-shaped value must be dropped, got clauses=%v args=%v", clauses, args)
	}
}

func TestScanDests_LengthAndSettable(t *testing.T) {
	active := []fieldcatalog.Column{col("cf_a", fieldcatalog.TypeText), col("cf_b", fieldcatalog.TypeText)}
	dests := ScanDests(active)
	if len(dests) != 2 {
		t.Fatalf("len(dests) = %d, want 2", len(dests))
	}
	p, ok := dests[0].(*any)
	if !ok {
		t.Fatalf("dest is %T, want *any", dests[0])
	}
	*p = "scanned"
	if *p != "scanned" {
		t.Fatal("dest must be a settable *any")
	}
}

// TestSQLValue_RoundTrip drives every one of the six types through
// SQLValue (write) then, using the shape a real read would hand back,
// through ExtractValues (read) — proving the wire value survives the
// full round trip.
func TestSQLValue_RoundTrip(t *testing.T) {
	cases := []struct {
		name     string
		typ      string
		wire     any // the JSON-decoded value SQLValue receives
		wantBind any // SQLValue's bind-value output
		scanned  any // what a real driver Scan would place in the *any dest
		wantWire any // ExtractValues' output, read back
	}{
		{
			name: "currency", typ: fieldcatalog.TypeCurrency,
			wire: float64(150000), wantBind: int64(150000),
			scanned: int64(150000), wantWire: int64(150000),
		},
		{
			name: "number as float64", typ: fieldcatalog.TypeNumber,
			wire: float64(42.5), wantBind: float64(42.5),
			scanned: numericFromString(t, "42.5"), wantWire: json.Number("42.5"),
		},
		{
			name: "number as json.Number", typ: fieldcatalog.TypeNumber,
			wire: json.Number("99.990"), wantBind: "99.990",
			scanned: numericFromString(t, "99.990"), wantWire: json.Number("99.990"),
		},
		{
			name: "date", typ: fieldcatalog.TypeDate,
			wire: "2026-07-11", wantBind: "2026-07-11",
			scanned: time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC), wantWire: "2026-07-11",
		},
		{
			name: "boolean", typ: fieldcatalog.TypeBoolean,
			wire: true, wantBind: true,
			scanned: true, wantWire: true,
		},
		{
			name: "text", typ: fieldcatalog.TypeText,
			wire: "hello", wantBind: "hello",
			scanned: "hello", wantWire: "hello",
		},
		{
			name: "picklist", typ: fieldcatalog.TypePicklist,
			wire: "red", wantBind: "red",
			scanned: "red", wantWire: "red",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			column := col("cf_x", c.typ)
			bind, ok := SQLValue(column, c.wire)
			if !ok {
				t.Fatalf("SQLValue: expected ok=true for %v", c.wire)
			}
			if !reflect.DeepEqual(bind, c.wantBind) {
				t.Fatalf("SQLValue = %#v (%T), want %#v (%T)", bind, bind, c.wantBind, c.wantBind)
			}

			dest := c.scanned
			extracted := ExtractValues([]fieldcatalog.Column{column}, []any{&dest})
			if !reflect.DeepEqual(extracted["cf_x"], c.wantWire) {
				t.Fatalf("ExtractValues = %#v (%T), want %#v (%T)", extracted["cf_x"], extracted["cf_x"], c.wantWire, c.wantWire)
			}
		})
	}
}

func TestSQLValue_DropOnMismatch(t *testing.T) {
	cases := []struct {
		name string
		typ  string
		wire any
	}{
		{"currency given a string", fieldcatalog.TypeCurrency, "150000"},
		{"number given a bool", fieldcatalog.TypeNumber, true},
		{"number given an unparseable string", fieldcatalog.TypeNumber, "not-a-number"},
		{"date given a number", fieldcatalog.TypeDate, float64(20260711)},
		{"boolean given a string", fieldcatalog.TypeBoolean, "true"},
		{"text given a number", fieldcatalog.TypeText, float64(1)},
		{"picklist given a number", fieldcatalog.TypePicklist, float64(1)},
		{"unknown type", "unknown", "anything"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, ok := SQLValue(col("cf_x", c.typ), c.wire); ok {
				t.Fatalf("SQLValue(%q, %#v): expected ok=false (drop-on-mismatch)", c.typ, c.wire)
			}
		})
	}
}

func TestExtractValues_NullOmitted(t *testing.T) {
	active := []fieldcatalog.Column{col("cf_present", fieldcatalog.TypeText), col("cf_null", fieldcatalog.TypeText)}
	var present, isNull any
	present = "value"
	isNull = nil
	got := ExtractValues(active, []any{&present, &isNull})
	if _, ok := got["cf_null"]; ok {
		t.Fatalf("a NULL column must be omitted entirely, got %v", got)
	}
	if got["cf_present"] != "value" {
		t.Fatalf("cf_present = %v, want %q", got["cf_present"], "value")
	}
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1 (only the non-NULL key)", len(got))
	}
}

func TestExtractValues_NumericNaNDropped(t *testing.T) {
	active := []fieldcatalog.Column{col("cf_score", fieldcatalog.TypeNumber)}
	var dest any = pgtype.Numeric{NaN: true, Valid: true}
	got := ExtractValues(active, []any{&dest})
	if _, ok := got["cf_score"]; ok {
		t.Fatalf("a NaN numeric has no JSON-number wire shape and must be dropped, got %v", got)
	}
}

func TestExtractValues_MoreColumnsThanDests(t *testing.T) {
	// A caller must never build a dests slice shorter than active, but
	// ExtractValues degrades to "extract what it can" rather than
	// panicking — the honest-hard-case guard the craft rules ask for.
	active := []fieldcatalog.Column{col("cf_a", fieldcatalog.TypeText), col("cf_b", fieldcatalog.TypeText)}
	var a any = "only one dest"
	got := ExtractValues(active, []any{&a})
	if len(got) != 1 || got["cf_a"] != "only one dest" {
		t.Fatalf("got %v", got)
	}
}
