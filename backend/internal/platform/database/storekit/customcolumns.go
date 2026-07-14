// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package storekit

// The pure SQL-fragment/value mechanics a record store (people, deals, …)
// drives its cf_* columns with, ported from poc-1's
// platform/customfields/{columns.go,active.go}. These functions own no
// domain and touch no database: they take the caller's already-fetched
// []fieldcatalog.Column in and return SQL fragments, bind args, or wire
// values out. The catalog READ that produces []fieldcatalog.Column lives
// in modules/customfields (the fieldcatalog.Reader implementation),
// reached only through the fieldcatalog seam — this file is the other
// half of the cross-module boundary the seam exists to keep clean
// (ADR-0054 §3 — every cross-module edge is injected in
// compose): platform may import shared, so storekit consumes
// fieldcatalog.Column directly, but neither storekit nor a record store
// ever imports modules/customfields.
//
// Every conversion is drop-on-mismatch, not validate-and-reject: a
// request body's additionalProperties carries no per-key shape contract
// (it is JSON Schema `additionalProperties: true`), so a value whose
// shape does not match its column's type is silently excluded rather
// than answered with a 422 — the same posture poc-1 shipped.

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/gradionhq/margince/backend/internal/shared/ports/fieldcatalog"
)

// quoteColumnIdentifier wraps a physical column name as a double-quoted
// Postgres identifier. pgx.Identifier.Sanitize is the ONE spelling this
// repo uses for identifier quoting (mirrors modules/customfields' own
// quoteIdentifier in engine.go) — every name here is server-derived
// (fieldcatalog.Column.Name, never request text), but the quoting still
// runs so a column name is never spliced in unquoted.
func quoteColumnIdentifier(name string) string {
	return pgx.Identifier{name}.Sanitize()
}

// SelectSuffix returns the comma-prefixed, quoted custom-column list to
// append to a fixed SELECT column list (empty when there are no active
// columns), so a read path fetches its cf_* values in the same round
// trip as its fixed columns. Shared by person/organization/deal Get/List
// queries: the column-list-building mechanics are identical across
// resources even though the surrounding SELECT/Scan shape (and the
// domain struct it feeds) stays per-resource.
func SelectSuffix(active []fieldcatalog.Column) string {
	if len(active) == 0 {
		return ""
	}
	parts := make([]string, len(active))
	for i, c := range active {
		parts[i] = quoteColumnIdentifier(c.Name)
	}
	return ", " + strings.Join(parts, ", ")
}

// InsertFragments returns the comma-prefixed quoted-column and $N
// placeholder fragments a literal INSERT statement splices in after its
// fixed columns/placeholders, plus the bind args — one fragment pair per
// active custom column present (with a type-matching value) in rawExtra.
// nextParam is the first free bind-parameter index. A key with no
// active-column match, or whose value shape does not match the column
// type, is silently dropped (see the package doc's drop-on-mismatch
// note). Empty strings when nothing matches, so the caller splices
// unconditionally. Shared by person/organization/deal Create.
func InsertFragments(active []fieldcatalog.Column, rawExtra map[string]any, nextParam int) (cols, placeholders string, args []any) {
	var names, holders []string
	for _, c := range active {
		v, present := rawExtra[c.Name]
		if !present {
			continue
		}
		sv, ok := SQLValue(c, v)
		if !ok {
			continue
		}
		names = append(names, quoteColumnIdentifier(c.Name))
		holders = append(holders, "$"+strconv.Itoa(nextParam+len(args)))
		args = append(args, sv)
	}
	if len(names) == 0 {
		return "", "", nil
	}
	return ", " + strings.Join(names, ", "), ", " + strings.Join(holders, ", "), args
}

// SetCustomFieldPatch folds an update's custom-field values into the
// patch — the same Patch that carries core columns, so the audit
// before/after and the version-guarded UPDATE include cf_ changes with
// no extra bookkeeping. current is the row's present wire values (the
// AdditionalProperties map of the pre-update read); an absent key diffs
// from nil. Same drop-on-mismatch rule as InsertFragments. The SQL SET
// fragment quotes the catalog-derived identifier (a core column name is
// a fixed literal in its store's SQL; a cf_ name is data and must never
// splice in unquoted), while the audit diff keeps the bare wire name.
func SetCustomFieldPatch(p *Patch, active []fieldcatalog.Column, updates, current map[string]any) {
	for _, c := range active {
		v, present := updates[c.Name]
		if !present {
			continue
		}
		sv, ok := SQLValue(c, v)
		if !ok {
			continue
		}
		p.setQuoted(c.Name, current[c.Name], sv)
	}
}

// ScanDests returns fresh scan destinations for active columns, in
// order — a caller appends these after its fixed-column destinations
// when Scan-ing a SelectSuffix row.
func ScanDests(active []fieldcatalog.Column) []any {
	dests := make([]any, len(active))
	for i := range active {
		var v any
		dests[i] = &v
	}
	return dests
}

// ExtractValues converts scanned custom-field values into wire values,
// keyed by column name. A NULL column is omitted from the map entirely
// (the contract's additionalProperties carries no per-key null; an
// absent key IS the wire spelling of NULL).
func ExtractValues(active []fieldcatalog.Column, dests []any) map[string]any {
	out := make(map[string]any, len(active))
	for i, c := range active {
		if i >= len(dests) || dests[i] == nil {
			continue
		}
		p, ok := dests[i].(*any)
		if !ok || p == nil || *p == nil {
			continue
		}
		if v, ok := extractValue(c.Type, *p); ok {
			out[c.Name] = v
		}
	}
	return out
}

// extractValue converts one driver-scanned value into its documented
// wire shape for typ, split out of ExtractValues to keep that loop's
// cyclomatic complexity within the project's lint budget.
//
// The TypeNumber case is the one place this deliberately diverges from
// poc-1's platform/customfields/active.go: poc-1 rode database/sql +
// lib/pq, which hands a numeric column back as []byte/string when
// scanned into interface{}. This repo's pgx/v5 driver instead decodes a
// numeric column into a pgtype.Numeric struct (confirmed empirically
// against a real Postgres 16 — see task-1-report.md) — a bare []byte/
// string switch on that value would silently drop-on-mismatch every
// number-typed field, never surfacing an error. pgtype.Numeric's
// MarshalJSON renders the exact decimal text Postgres stored (no
// float64 rounding), which is exactly the json.Number wire shape this
// package round-trips numbers through.
//
//craft:ignore naked-any raw is whatever Go type the pgx driver decoded a scanned column into (int64, bool, string, time.Time, pgtype.Numeric, …) — the type switch IS the conversion contract, so there is no narrower signature to give it
func extractValue(typ string, raw any) (any, bool) {
	switch typ {
	case fieldcatalog.TypeCurrency:
		v, ok := raw.(int64)
		return v, ok
	case fieldcatalog.TypeBoolean:
		v, ok := raw.(bool)
		return v, ok
	case fieldcatalog.TypeText, fieldcatalog.TypePicklist:
		switch v := raw.(type) {
		case []byte:
			return string(v), true
		case string:
			return v, true
		}
	case fieldcatalog.TypeDate:
		switch v := raw.(type) {
		case time.Time:
			return v.Format("2006-01-02"), true
		case string:
			return v, true
		}
	case fieldcatalog.TypeNumber:
		return extractNumber(raw)
	}
	return nil, false
}

// extractNumber renders a scanned numeric-column value as json.Number,
// split out of extractValue to document the pgtype.Numeric case (see
// extractValue's doc comment) without inflating that switch's branches.
//
//craft:ignore naked-any same driver-scanned-value contract as extractValue
func extractNumber(raw any) (any, bool) {
	switch v := raw.(type) {
	case pgtype.Numeric:
		if !v.Valid || v.NaN {
			return nil, false // NaN has no JSON-number wire shape
		}
		b, err := v.MarshalJSON()
		if err != nil {
			return nil, false
		}
		return json.Number(string(b)), true
	case []byte:
		return json.Number(string(v)), true
	case string:
		return json.Number(v), true
	}
	return nil, false
}

// SQLValue converts one JSON-decoded wire value into a database bind
// value for column c's type, or ok=false when v's shape does not match
// (the drop-on-mismatch rule the package doc describes).
//
//craft:ignore naked-any v is one raw value out of a request body's additionalProperties map[string]any (JSON Schema additionalProperties: true carries no per-key type) — the type switch on c.Type IS the conversion contract
func SQLValue(c fieldcatalog.Column, v any) (any, bool) {
	switch c.Type {
	case fieldcatalog.TypeCurrency:
		f, ok := v.(float64)
		if !ok {
			return nil, false
		}
		// A currency column is int64 minor units: a fractional amount
		// (int64(f) would silently truncate it) or a magnitude int64
		// cannot hold (int64(f) is undefined behavior for an
		// out-of-range float in Go) must drop rather than store garbage
		// money — same drop-on-mismatch contract as every other type
		// here, just enforced with a range/integrality check instead of
		// a type assertion.
		if f != math.Trunc(f) || f < math.MinInt64 || f > math.MaxInt64 {
			return nil, false
		}
		return int64(f), true
	case fieldcatalog.TypeNumber:
		return sqlNumber(v)
	case fieldcatalog.TypeDate:
		s, ok := v.(string)
		if !ok {
			return nil, false
		}
		return s, true
	case fieldcatalog.TypeBoolean:
		b, ok := v.(bool)
		if !ok {
			return nil, false
		}
		return b, true
	case fieldcatalog.TypeText, fieldcatalog.TypePicklist:
		s, ok := v.(string)
		if !ok {
			return nil, false
		}
		return s, true
	default:
		return nil, false
	}
}

// sqlNumber binds a number-typed field, accepting either shape a caller
// may hand it: the float64 the generated contract's default
// json.Unmarshal-into-interface{} produces today, or a json.Number/plain
// decimal string a caller decoding with json.Decoder.UseNumber (the
// precision-preserving path — see task-1-report.md's note on
// AdditionalProperties decoding) would produce instead. All three shapes
// bind directly to a numeric column parameter under pgx/v5 (confirmed
// empirically), so this returns each input in the shape that best
// preserves its own precision rather than normalizing through float64.
//
//craft:ignore naked-any same additionalProperties-value contract as SQLValue
func sqlNumber(v any) (any, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case json.Number:
		return string(t), true
	case string:
		if _, err := strconv.ParseFloat(t, 64); err != nil {
			return nil, false
		}
		return t, true
	default:
		return nil, false
	}
}
