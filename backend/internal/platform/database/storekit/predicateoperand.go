// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package storekit

// A predicate leaf names a field and an operator but carries its value
// schemaless off the wire (a JSON scalar or array); this is where that
// value earns the right to become a bind parameter — checked against
// the field's declared type, rejected with a PredicateError otherwise,
// and never allowed to shape the query text itself.

import (
	"fmt"
	"math"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// comparisonSQL is closed over the operator constants above; compileLeaf
// only reaches it after the operator passed the typed matrix.
var comparisonSQL = map[string]string{
	OpEq: "=", OpNeq: "<>", OpGt: ">", OpGte: ">=", OpLt: "<", OpLte: "<=",
}

// existsOperand validates the operand of an exists operator: must be
// a boolean. Returns the decoded value or a PredicateError with
// CodeFilterValueInvalid.
func existsOperand(p Predicate) (bool, error) {
	present, ok := p.Value.(bool)
	if !ok {
		return false, &PredicateError{
			Field: p.Field, Code: CodeFilterValueInvalid,
			Message: "exists takes true or false",
		}
	}
	return present, nil
}

// inOperand validates an `in` list: a non-empty, bounded array of
// scalars each valid for the field's type, returned as a uniformly
// typed slice pgx can bind as one array parameter.
//
//craft:ignore naked-any the return is a bind parameter — []float64 or []string per field type, decided at runtime by the field catalog
func inOperand(p Predicate, field Field) (any, error) {
	raw, ok := p.Value.([]any)
	if !ok || len(raw) == 0 {
		return nil, &PredicateError{
			Field: p.Field, Code: CodeFilterValueInvalid,
			Message: "in takes a non-empty array of values",
		}
	}
	if len(raw) > PredicateMaxInValues {
		return nil, &PredicateError{
			Field: p.Field, Code: CodeFilterTooLarge,
			Message: fmt.Sprintf("in list exceeds the maximum of %d values", PredicateMaxInValues),
		}
	}
	switch field.Type {
	case FieldNumber:
		return inNumberOperand(raw, field, p.Field)
	case FieldCurrency:
		return inCurrencyOperand(raw, field, p.Field)
	default: // text, picklist, id — string-valued types (dates take no `in`).
		return inStringOperand(raw, field, p.Field)
	}
}

// inNumberOperand is inOperand's number branch: scalarOperand's contract
// for this type is always a float64, but the assertion is checked rather
// than asserted blind — a scalarOperand that ever changed that contract
// must fail loudly here, not hand pgx a mistyped bind slice.
func inNumberOperand(raw []any, field Field, name string) ([]float64, error) {
	values := make([]float64, len(raw))
	for i, v := range raw {
		checked, err := scalarOperand(v, field, name, OpIn)
		if err != nil {
			return nil, err
		}
		n, ok := checked.(float64)
		if !ok {
			return nil, fmt.Errorf("storekit: scalarOperand returned %T for a %s field, want float64", checked, field.Type)
		}
		values[i] = n
	}
	return values, nil
}

// inCurrencyOperand is inOperand's currency branch, checked the same way
// inNumberOperand is checked — but against int64, currency's actual bind
// shape (see scalarCurrencyOperand): a bigint column bound from a float64
// truncates through pgx's Int8Codec rather than refusing a fractional or
// out-of-range member.
func inCurrencyOperand(raw []any, field Field, name string) ([]int64, error) {
	values := make([]int64, len(raw))
	for i, v := range raw {
		checked, err := scalarOperand(v, field, name, OpIn)
		if err != nil {
			return nil, err
		}
		n, ok := checked.(int64)
		if !ok {
			return nil, fmt.Errorf("storekit: scalarOperand returned %T for a %s field, want int64", checked, field.Type)
		}
		values[i] = n
	}
	return values, nil
}

// inStringOperand is inOperand's text/picklist/id branch, checked the same
// way inNumberOperand is.
func inStringOperand(raw []any, field Field, name string) ([]string, error) {
	values := make([]string, len(raw))
	for i, v := range raw {
		checked, err := scalarOperand(v, field, name, OpIn)
		if err != nil {
			return nil, err
		}
		s, ok := checked.(string)
		if !ok {
			return nil, fmt.Errorf("storekit: scalarOperand returned %T for a %s field, want string", checked, field.Type)
		}
		values[i] = s
	}
	return values, nil
}

// scalarOperand validates one scalar against the field type and returns
// the value to bind. JSON numbers arrive as float64; integers are
// accepted too so hand-built Go trees read naturally.
//
//craft:ignore naked-any value is a decoded JSON filter operand and the return a bind parameter — both inherently span the SQL scalar types
func scalarOperand(value any, field Field, name, op string) (any, error) {
	invalid := func(want string) error {
		return &PredicateError{
			Field: name, Code: CodeFilterValueInvalid,
			Message: fmt.Sprintf("operator %q on %s field %q takes %s", op, field.Type, name, want),
		}
	}
	switch field.Type {
	case FieldText, FieldPicklist:
		return scalarStringOperand(value, invalid, "a string")
	case FieldID:
		return scalarUUIDOperand(value, invalid)
	case FieldNumber:
		return scalarNumberOperand(value, invalid)
	case FieldCurrency:
		return scalarCurrencyOperand(value, invalid)
	case FieldDate:
		return scalarDateOperand(value, invalid)
	case FieldBoolean:
		return scalarBoolOperand(value, invalid)
	default:
		// A vocabulary entry with an unknown type is a programming error
		// in the caller's field map, surfaced as a validation failure
		// rather than reaching the SQL text.
		return nil, invalid("a value of a known field type")
	}
}

// scalarStringOperand is scalarOperand's text/picklist branch: any plain
// string is a valid bind value, so there is nothing to validate beyond
// the type itself.
//
//craft:ignore naked-any value is a decoded JSON filter operand and the return a bind parameter — both inherit scalarOperand's own span across the SQL scalar types
func scalarStringOperand(value any, invalid func(string) error, want string) (any, error) {
	s, ok := value.(string)
	if !ok {
		return nil, invalid(want)
	}
	return s, nil
}

// scalarUUIDOperand is scalarOperand's id branch: the string must also
// parse as a UUID, so a malformed reference fails validation (422) rather
// than reaching the query as a value that can never match.
//
//craft:ignore naked-any value is a decoded JSON filter operand and the return a bind parameter — both inherit scalarOperand's own span across the SQL scalar types
func scalarUUIDOperand(value any, invalid func(string) error) (any, error) {
	s, ok := value.(string)
	if !ok {
		return nil, invalid("a UUID string")
	}
	if _, err := ids.Parse(s); err != nil {
		return nil, invalid("a UUID string")
	}
	return s, nil
}

// scalarNumberOperand is scalarOperand's number/currency branch: JSON
// numbers arrive as float64, and a hand-built Go tree's int/int64 are
// widened to match — a NaN or infinite float is refused, since neither
// can ever equal, exceed, or fall short of anything in SQL.
//
//craft:ignore naked-any value is a decoded JSON filter operand and the return a bind parameter — both inherit scalarOperand's own span across the SQL scalar types
func scalarNumberOperand(value any, invalid func(string) error) (any, error) {
	switch n := value.(type) {
	case float64:
		if math.IsNaN(n) || math.IsInf(n, 0) {
			return nil, invalid("a finite number")
		}
		return n, nil
	case int:
		return float64(n), nil
	case int64:
		return float64(n), nil
	default:
		return nil, invalid("a number")
	}
}

// currencyFloatFloor and currencyFloatCeil bound the float64 values
// scalarCurrencyOperand accepts before converting to int64: the widest
// half-open range a float64 can compare against the int64 range without
// undefined-behaviour overflow on the final conversion.
const (
	currencyFloatFloor = -9223372036854775808 // math.MinInt64, exact in float64
	currencyFloatCeil  = 9223372036854775808  // math.MaxInt64 + 1, exact in float64
)

// scalarCurrencyOperand is scalarOperand's currency branch: a currency
// column is bigint minor units (custom-fields.md), never a decimal, so the
// operand must be a whole number that fits int64 — binding a float64
// straight through would reach pgx's Int8Codec, which truncates a
// fractional value silently via its Int64Valuer path rather than refusing
// it, and an out-of-range value would fail as a database error instead of
// a caller-facing one. Converting to int64 here, once both checks pass,
// makes the eventual bind exact rather than routed through that lossy path.
//
//craft:ignore naked-any value is a decoded JSON filter operand and the return a bind parameter — both inherit scalarOperand's own span across the SQL scalar types
func scalarCurrencyOperand(value any, invalid func(string) error) (any, error) {
	switch n := value.(type) {
	case float64:
		if math.IsNaN(n) || math.IsInf(n, 0) {
			return nil, invalid("a whole number of minor units")
		}
		if math.Trunc(n) != n {
			return nil, invalid("a whole number of minor units, not a fractional amount")
		}
		if n < currencyFloatFloor || n >= currencyFloatCeil {
			return nil, invalid("a whole number of minor units within the supported range")
		}
		return int64(n), nil
	case int:
		return int64(n), nil
	case int64:
		return n, nil
	default:
		return nil, invalid("a whole number of minor units")
	}
}

// scalarDateOperand is scalarOperand's date branch: an ISO calendar date,
// parsed to prove it is a real one rather than passed through as an
// arbitrary string.
//
//craft:ignore naked-any value is a decoded JSON filter operand and the return a bind parameter — both inherit scalarOperand's own span across the SQL scalar types
func scalarDateOperand(value any, invalid func(string) error) (any, error) {
	s, ok := value.(string)
	if !ok {
		return nil, invalid("an ISO date (YYYY-MM-DD)")
	}
	if _, err := time.Parse("2006-01-02", s); err != nil {
		return nil, invalid("an ISO date (YYYY-MM-DD)")
	}
	return s, nil
}

// scalarBoolOperand is scalarOperand's boolean branch.
//
//craft:ignore naked-any value is a decoded JSON filter operand and the return a bind parameter — both inherit scalarOperand's own span across the SQL scalar types
func scalarBoolOperand(value any, invalid func(string) error) (any, error) {
	b, ok := value.(bool)
	if !ok {
		return nil, invalid("true or false")
	}
	return b, nil
}

// A LIKE/ILIKE operand is escaped through EscapeLike (suppression.go),
// which this package already exports for the erasure purge: the metacharacters
// % and _ and the escape character itself must match themselves, and Postgres'
// default LIKE escape is backslash, so the compiled predicate needs no ESCAPE
// clause of its own.
