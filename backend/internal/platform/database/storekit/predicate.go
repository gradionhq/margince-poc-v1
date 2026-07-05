// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package storekit

// The canonical typed AND/OR predicate engine (B-E15.10a/b, features/10
// §3): the ONE filter representation behind lists, saved views, dynamic
// lists, filtered export, and NL→filter. A predicate is a tree of
// AND/OR groups over typed leaves; each leaf names a field from the
// caller's closed vocabulary (the data-model §13.5 per-resource
// allow-list — the caller passes it as a map, exactly like the report
// engine's reportSpec), an operator from the fixed DSL set
// (eq,neq,gt,lt,gte,lte,in,contains,exists — B-E15.10a acceptance; no
// new grammar), and a value validated against the field's type. Every
// identifier that reaches the query text comes from the vocabulary map;
// every value travels as a bind parameter.
//
// It lives in storekit because modules (people, deals, …) and compose
// both consume it, and the DAG only lets platform sit under both.
//
// Compile is scope-neutral by design: it renders the caller's filter
// and NOTHING else. Callers MUST AND the result with their row-scope
// clause (auth.ScopeClauseFor) — the bundled Query executor does that
// composition itself, so surface code cannot forget it.

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// Grammar bounds (B-E15.10 "bounded" acceptance): a filter is a UI
// artifact, not a query language — a tree deeper or wider than a human
// would build in the visual builder is rejected, not executed.
const (
	// PredicateMaxDepth bounds group nesting; a leaf inside N groups is
	// at depth N. The root group is depth 1.
	PredicateMaxDepth = 4
	// PredicateMaxLeaves bounds the total condition count across the tree.
	PredicateMaxLeaves = 32
	// PredicateMaxInValues bounds one `in` list.
	PredicateMaxInValues = 100
	// PredicateRowLimit is the hard row bound of the executor; a larger
	// slice is an export, not a filtered list.
	PredicateRowLimit = 1000
)

// FieldType types a filterable field (features/10 §3: typed operators
// per field type). It decides which operators apply and how a leaf's
// value is validated before it may become a bind parameter.
type FieldType string

const (
	FieldText     FieldType = "text"
	FieldNumber   FieldType = "number"
	FieldDate     FieldType = "date"
	FieldCurrency FieldType = "currency"
	FieldPicklist FieldType = "picklist"
	FieldBoolean  FieldType = "boolean"
	// FieldID covers the allow-list's UUID reference columns (owner_id,
	// stage_id, …): equality/membership only, value must parse as a UUID
	// so a malformed id fails validation (422), never query execution.
	FieldID FieldType = "id"
)

// Field is one entry of a resource's closed filter vocabulary: the API
// name maps to a fixed SQL expression (table alias included, e.g.
// "t.owner_id") plus its type. Only expressions from this map ever
// reach the query text.
type Field struct {
	Expr string
	Type FieldType
}

// Predicate is the canonical filter tree (the representation
// saved_view.query and dynamic-list definitions carry). Exactly one of
// And, Or, or the leaf triple (Field+Op) is set per node; anything else
// is a shape error.
type Predicate struct {
	And []Predicate `json:"and,omitempty"`
	Or  []Predicate `json:"or,omitempty"`

	Field string `json:"field,omitempty"`
	Op    string `json:"op,omitempty"`
	// Value is the leaf operand — inherently schemaless at the wire (a
	// JSON scalar or array) and validated against the field's declared
	// type before it becomes a bind parameter.
	Value any `json:"value,omitempty"`
}

// The fixed operator vocabulary (B-E15.10a: the existing filter DSL set,
// nothing invented).
const (
	OpEq       = "eq"
	OpNeq      = "neq"
	OpGt       = "gt"
	OpLt       = "lt"
	OpGte      = "gte"
	OpLte      = "lte"
	OpIn       = "in"
	OpContains = "contains"
	OpExists   = "exists"
)

// operatorsByType is the typed-operator matrix: ordering only for
// ordered types, substring match only for text, membership only where
// equality is meaningful.
var operatorsByType = map[FieldType]map[string]bool{
	FieldText:     {OpEq: true, OpNeq: true, OpIn: true, OpContains: true, OpExists: true},
	FieldPicklist: {OpEq: true, OpNeq: true, OpIn: true, OpExists: true},
	FieldID:       {OpEq: true, OpNeq: true, OpIn: true, OpExists: true},
	FieldNumber:   {OpEq: true, OpNeq: true, OpGt: true, OpGte: true, OpLt: true, OpLte: true, OpIn: true, OpExists: true},
	FieldCurrency: {OpEq: true, OpNeq: true, OpGt: true, OpGte: true, OpLt: true, OpLte: true, OpIn: true, OpExists: true},
	FieldDate:     {OpEq: true, OpNeq: true, OpGt: true, OpGte: true, OpLt: true, OpLte: true, OpExists: true},
	FieldBoolean:  {OpEq: true, OpNeq: true, OpExists: true},
}

// PredicateError is the typed validation failure: the transport maps it
// onto the httperr.Validation 422 shape (data-model §13.5's
// "anything else → 422"). Field carries the offending filter field (or
// a positional path for shape errors), Code the machine-readable reason.
type PredicateError struct {
	Field   string
	Code    string
	Message string
}

func (e *PredicateError) Error() string {
	return fmt.Sprintf("predicate: %s: %s (%s)", e.Field, e.Message, e.Code)
}

// The PredicateError codes, mirroring the §13.5 naming
// (sort_field_not_allowed → filter_field_not_allowed).
const (
	CodeFilterFieldNotAllowed = "filter_field_not_allowed"
	CodeFilterOpNotAllowed    = "filter_operator_not_allowed"
	CodeFilterValueInvalid    = "filter_value_invalid"
	CodeFilterTooDeep         = "filter_too_deep"
	CodeFilterTooLarge        = "filter_too_large"
	CodeFilterShapeInvalid    = "filter_shape_invalid"
)

// CompilePredicate renders the tree as one parenthesized SQL boolean
// expression against the closed vocabulary. arg registers a bind value
// and returns its 1-based position (the report engine's convention), so
// the result composes with clauses the caller already accumulated. The
// output contains NO row scope — callers AND it with their scope clause.
// Compilation is deterministic: the same tree yields the same SQL and
// the same argument order.
func CompilePredicate(p Predicate, fields map[string]Field, arg func(any) int) (string, error) {
	leaves := 0
	return compileNode(p, fields, arg, 1, &leaves)
}

func compileNode(p Predicate, fields map[string]Field, arg func(any) int, depth int, leaves *int) (string, error) {
	group, children, isGroup, err := groupShape(p)
	if err != nil {
		return "", err
	}
	if !isGroup {
		return compileLeaf(p, fields, arg, leaves)
	}
	if depth > PredicateMaxDepth {
		return "", &PredicateError{
			Field: group, Code: CodeFilterTooDeep,
			Message: fmt.Sprintf("filter groups nest deeper than the maximum of %d", PredicateMaxDepth),
		}
	}
	if len(children) == 0 {
		return "", &PredicateError{
			Field: group, Code: CodeFilterShapeInvalid,
			Message: "a filter group must contain at least one condition",
		}
	}
	parts := make([]string, len(children))
	for i, child := range children {
		part, err := compileNode(child, fields, arg, depth+1, leaves)
		if err != nil {
			return "", err
		}
		parts[i] = part
	}
	joiner := " AND "
	if group == "or" {
		joiner = " OR "
	}
	return "(" + strings.Join(parts, joiner) + ")", nil
}

// groupShape classifies a node and rejects ambiguous ones: a node that
// sets both group kinds, or mixes a group with leaf parts, has no
// defined meaning and must not guess one.
func groupShape(p Predicate) (kind string, children []Predicate, isGroup bool, err error) {
	hasAnd, hasOr, hasLeaf := len(p.And) > 0, len(p.Or) > 0, p.Field != "" || p.Op != "" || p.Value != nil
	switch {
	case hasAnd && (hasOr || hasLeaf), hasOr && hasLeaf:
		return "", nil, false, &PredicateError{
			Field: "filter", Code: CodeFilterShapeInvalid,
			Message: "a filter node is exactly one of: an \"and\" group, an \"or\" group, or a single condition",
		}
	case hasAnd:
		return "and", p.And, true, nil
	case hasOr:
		return "or", p.Or, true, nil
	case hasLeaf:
		return "", nil, false, nil
	default:
		return "", nil, false, &PredicateError{
			Field: "filter", Code: CodeFilterShapeInvalid,
			Message: "empty filter node: supply a condition or an \"and\"/\"or\" group",
		}
	}
}

func compileLeaf(p Predicate, fields map[string]Field, arg func(any) int, leaves *int) (string, error) {
	*leaves++
	if *leaves > PredicateMaxLeaves {
		return "", &PredicateError{
			Field: p.Field, Code: CodeFilterTooLarge,
			Message: fmt.Sprintf("filter has more than the maximum of %d conditions", PredicateMaxLeaves),
		}
	}
	field, ok := fields[p.Field]
	if !ok {
		return "", &PredicateError{
			Field: p.Field, Code: CodeFilterFieldNotAllowed,
			Message: fmt.Sprintf("field %q is not filterable on this resource", p.Field),
		}
	}
	if !operatorsByType[field.Type][p.Op] {
		return "", &PredicateError{
			Field: p.Field, Code: CodeFilterOpNotAllowed,
			Message: fmt.Sprintf("operator %q does not apply to the %s field %q", p.Op, field.Type, p.Field),
		}
	}

	switch p.Op {
	case OpExists:
		// exists carries a boolean operand: true → the value is present.
		present, ok := p.Value.(bool)
		if !ok {
			return "", &PredicateError{
				Field: p.Field, Code: CodeFilterValueInvalid,
				Message: "exists takes true or false",
			}
		}
		if present {
			return field.Expr + " IS NOT NULL", nil
		}
		return field.Expr + " IS NULL", nil

	case OpIn:
		values, err := inOperand(p, field)
		if err != nil {
			return "", err
		}
		// One array bind (= ANY) keeps the SQL text independent of the
		// list length — same tree shape, same statement, plan-cache warm.
		return fmt.Sprintf("%s = ANY($%d)", field.Expr, arg(values)), nil

	case OpContains:
		text, ok := p.Value.(string)
		if !ok || text == "" {
			return "", &PredicateError{
				Field: p.Field, Code: CodeFilterValueInvalid,
				Message: "contains takes a non-empty string",
			}
		}
		// Substring match, case-insensitive (the visual builder's
		// "contains"); LIKE metacharacters in the operand match
		// literally — a value of "100%" finds "100%", not everything.
		return fmt.Sprintf("%s ILIKE $%d", field.Expr, arg("%"+escapeLike(text)+"%")), nil

	default: // eq, neq, gt, gte, lt, lte — scalar comparisons.
		value, err := scalarOperand(p.Value, field, p.Field, p.Op)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%s %s $%d", field.Expr, comparisonSQL[p.Op], arg(value)), nil
	}
}

// comparisonSQL is closed over the operator constants above; compileLeaf
// only reaches it after the operator passed the typed matrix.
var comparisonSQL = map[string]string{
	OpEq: "=", OpNeq: "<>", OpGt: ">", OpGte: ">=", OpLt: "<", OpLte: "<=",
}

// inOperand validates an `in` list: a non-empty, bounded array of
// scalars each valid for the field's type, returned as a uniformly
// typed slice pgx can bind as one array parameter.
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
	case FieldNumber, FieldCurrency:
		values := make([]float64, len(raw))
		for i, v := range raw {
			checked, err := scalarOperand(v, field, p.Field, OpIn)
			if err != nil {
				return nil, err
			}
			values[i] = checked.(float64)
		}
		return values, nil
	default: // text, picklist, id — string-valued types (dates take no `in`).
		values := make([]string, len(raw))
		for i, v := range raw {
			checked, err := scalarOperand(v, field, p.Field, OpIn)
			if err != nil {
				return nil, err
			}
			values[i] = checked.(string)
		}
		return values, nil
	}
}

// scalarOperand validates one scalar against the field type and returns
// the value to bind. JSON numbers arrive as float64; integers are
// accepted too so hand-built Go trees read naturally.
func scalarOperand(value any, field Field, name, op string) (any, error) {
	invalid := func(want string) error {
		return &PredicateError{
			Field: name, Code: CodeFilterValueInvalid,
			Message: fmt.Sprintf("operator %q on %s field %q takes %s", op, field.Type, name, want),
		}
	}
	switch field.Type {
	case FieldText, FieldPicklist:
		s, ok := value.(string)
		if !ok {
			return nil, invalid("a string")
		}
		return s, nil
	case FieldID:
		s, ok := value.(string)
		if !ok {
			return nil, invalid("a UUID string")
		}
		if _, err := ids.Parse(s); err != nil {
			return nil, invalid("a UUID string")
		}
		return s, nil
	case FieldNumber, FieldCurrency:
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
	case FieldDate:
		s, ok := value.(string)
		if !ok {
			return nil, invalid("an ISO date (YYYY-MM-DD)")
		}
		if _, err := time.Parse("2006-01-02", s); err != nil {
			return nil, invalid("an ISO date (YYYY-MM-DD)")
		}
		return s, nil
	case FieldBoolean:
		b, ok := value.(bool)
		if !ok {
			return nil, invalid("true or false")
		}
		return b, nil
	default:
		// A vocabulary entry with an unknown type is a programming error
		// in the caller's field map, surfaced as a validation failure
		// rather than reaching the SQL text.
		return nil, invalid("a value of a known field type")
	}
}

// escapeLike makes a user string safe as a LIKE/ILIKE operand: the
// metacharacters % _ and the escape character \ match themselves.
// Postgres' default LIKE escape is backslash, so no ESCAPE clause is
// needed.
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// Query is the one-engine executor (B-E15.10b): one resource's closed
// vocabulary plus its fixed base clause, executing any Predicate as
// bounded, indexable SQL over the real columns. Lists, saved views, and
// filtered export all run their filters through here — never through a
// per-surface variant.
type Query struct {
	// Table is the base table, aliased t in every Fields expression.
	Table string
	// Fields is the resource's §13.5 filter allow-list.
	Fields map[string]Field
	// BaseWhere is the resource's fixed visibility clause (e.g.
	// "t.archived_at IS NULL"); empty means none.
	BaseWhere string
	// ActivityWalk selects the activity link-walk scope clause instead
	// of the direct ownership clause (the timeline's visibility rule).
	ActivityWalk bool
}

// SelectIDs runs the predicate inside the caller's workspace-bound
// transaction and returns matching row ids, deterministically ordered
// by id (the keyset tie-breaker) and hard-capped at PredicateRowLimit.
// The row-scope clause is composed HERE, unconditionally — a predicate
// can only ever narrow what the caller is already allowed to see.
func (q Query) SelectIDs(ctx context.Context, tx pgx.Tx, p Predicate, limit int) ([]ids.UUID, error) {
	if err := auth.Require(ctx, q.Table, principal.ActionRead); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > PredicateRowLimit {
		limit = PredicateRowLimit
	}

	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }

	where := make([]string, 0, 3)
	if q.BaseWhere != "" {
		where = append(where, q.BaseWhere)
	}
	compiled, err := CompilePredicate(p, q.Fields, arg)
	if err != nil {
		return nil, err
	}
	where = append(where, compiled)

	var scope string
	if q.ActivityWalk {
		scope, err = auth.ActivityScopeClause(ctx, "t", arg)
	} else {
		scope, err = auth.ScopeClauseFor(ctx, q.Table, "t", arg)
	}
	if err != nil {
		return nil, err
	}
	if scope != "" {
		where = append(where, scope)
	}

	sql := fmt.Sprintf("SELECT t.id FROM %s t WHERE %s ORDER BY t.id LIMIT %d",
		q.Table, strings.Join(where, " AND "), limit)
	rows, err := tx.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("predicate query on %s: %w", q.Table, err)
	}
	defer rows.Close()

	var matched []ids.UUID
	for rows.Next() {
		var id ids.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("predicate query on %s: %w", q.Table, err)
		}
		matched = append(matched, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("predicate query on %s: %w", q.Table, err)
	}
	return matched, nil
}
