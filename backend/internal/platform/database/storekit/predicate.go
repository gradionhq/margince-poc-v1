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
// This file COMPILES a filter and nothing else, which is why it reaches
// for neither the database nor auth. Compilation is scope-neutral by
// design: it renders the caller's filter and NOTHING more, so a caller
// MUST AND the result with their row-scope clause
// (auth.ScopeClauseFor). Nobody does that by hand — the Query executor
// in query.go composes it, along with the object-RBAC admission and the
// row bound, so surface code cannot forget any of the three.

import (
	"fmt"
	"strings"
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
	// Link makes this a correlated-subquery leaf rather than a base-table
	// one: Expr names the column INSIDE the subquery, and Link is the
	// EXISTS template — exactly one %s — the compiled comparison is
	// substituted into. It exists because some filterable facts are link
	// rows rather than columns (a tag lives in the polymorphic taggable
	// join), and a link row is present or absent where a column is null
	// or not. Empty for every base-table field.
	Link string
	// References names the record type this field's ids point at, for a
	// surface that has to offer the record rather than ask for its uuid.
	//
	// The compiler has no use for it — an id compares as an id whatever it
	// refers to — so it sits here for one reason: this is where a field is
	// declared, and a lookup table keyed by field name elsewhere would be a
	// second list of the engine's fields for a new leaf to fall out of.
	//
	// It is required of every id field in a vocabulary that is PUBLISHED to a
	// client — the collections segment engines, gated by
	// TestEveryIDFieldDeclaresWhatItReferences — and left empty everywhere
	// else. An engine built to answer a count nobody reads a field list from
	// (automation's preview vocabularies) owes no target, because nothing can
	// offer a picker for it.
	References Reference
	// Options is a picklist field's allowed values, for a surface that has to
	// OFFER them. Empty for every other type, and empty for a picklist whose
	// values this engine does not know.
	//
	// ADVERTISEMENT only today: compileLeaf does not refuse a value outside the
	// set. Making it refuse is a live-API behaviour change — a saved segment
	// holding a value since removed from the set would begin failing at read time
	// — so that is its own decision, tracked separately. What this field fixes
	// meanwhile is the surface: a builder that knows the values offers them
	// instead of asking a reader to type one, which is how a typo became a filter
	// matching nothing.
	Options []string
}

// Reference is a record type an id field's values point at. Named rather than a
// bare string so an unlisted target cannot be assigned, the same way FieldType
// closes the type column above it.
type Reference string

// The record types a FieldID field may reference. The values are the contract's
// own record-type words (`app_user`, not `user`), so a client keying a picker on
// them needs no translation table.
const (
	RefTag          Reference = "tag"
	RefAppUser      Reference = "app_user"
	RefTeam         Reference = "team"
	RefOrganization Reference = "organization"
	RefPipeline     Reference = "pipeline"
	RefStage        Reference = "stage"
	RefProject      Reference = "project"
)

// ReferenceTargets is every target the engine admits, and the ONE list of them.
//
// A function beside the constants rather than a slice restated in each test that
// needs it — the shape fieldcatalog.Types() already uses for the same reason. The
// point is which drift stays catchable: a constant absent here fails the sweep
// over the engines, and an entry here absent from the contract's enum fails the
// parity gate in compose. Restating this set in either test would make both gates
// pass on a stale copy of it, which is the one failure they exist to catch.
func ReferenceTargets() []Reference {
	return []Reference{
		RefTag, RefAppUser, RefTeam, RefOrganization,
		RefPipeline, RefStage, RefProject,
	}
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

// operatorOrder is the one reading order for an operator list: equality,
// then ordering, then membership, then presence. The matrix above is a map,
// so iterating it directly would answer a different order on every call and
// make a vocabulary response unstable between two identical requests.
var operatorOrder = []string{
	OpEq, OpNeq, OpGt, OpGte, OpLt, OpLte, OpIn, OpContains, OpExists,
}

// linkOperators are the operators a correlated-link leaf can express, and the
// authority compileLinkLeaf's own switch is held to.
//
// The comparison for a linked field builds INSIDE an EXISTS subquery, where the
// question is whether a matching row is there. Equality, membership and presence
// all say something about that; substring match and ordering do not have a
// spelling the wrapper can carry, so they are not offered rather than offered
// and refused.
//
// TestALinkedFieldAdvertisesExactlyWhatItCanCompile holds this set and that
// switch together, so neither can gain an operator the other lacks.
var linkOperators = map[string]bool{
	OpEq: true, OpNeq: true, OpIn: true, OpExists: true,
}

// OperatorsFor answers the operators this field admits, in operatorOrder.
//
// It exists so a surface that has to TELL a caller what a field accepts —
// the filter-vocabulary read — derives the answer from the same matrix
// compileLeaf refuses against, rather than carrying a second copy of it. The
// two cannot then disagree, which is the failure this arc has already paid for
// once: a vocabulary restated beside the engine offers a field the engine
// rejects, and the caller learns the difference as a 422 it could not predict.
//
// It takes the FIELD and not merely its type, because the type is not the whole
// answer: a correlated-link leaf compiles through a narrower set than its type
// admits, so a text field reached through a link accepts everything text does
// EXCEPT `contains`. Typing alone would advertise that operator and the engine
// would then refuse it — the same drift, one layer down.
//
// An unknown type answers an empty slice, not every operator: a field the
// matrix does not describe is one compileLeaf refuses outright, so promising
// operators for it would be the exact drift this function prevents.
func OperatorsFor(f Field) []string {
	admitted := operatorsByType[f.Type]
	ops := make([]string, 0, len(admitted))
	for _, op := range operatorOrder {
		if !admitted[op] {
			continue
		}
		if f.Link != "" && !linkOperators[op] {
			continue
		}
		ops = append(ops, op)
	}
	return ops
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

	if field.Link != "" {
		return compileLinkLeaf(p, field, arg)
	}

	switch p.Op {
	case OpExists:
		// exists carries a boolean operand: true → the value is present.
		present, err := existsOperand(p)
		if err != nil {
			return "", err
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
		return fmt.Sprintf("%s ILIKE $%d", field.Expr, arg("%"+EscapeLike(text)+"%")), nil

	default: // eq, neq, gt, gte, lt, lte — scalar comparisons.
		value, err := scalarOperand(p.Value, field, p.Field, p.Op)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%s %s $%d", field.Expr, comparisonSQL[p.Op], arg(value)), nil
	}
}

// compileLinkLeaf compiles a leaf whose fact lives on a linked row. The
// comparison is built against the column inside the subquery and then wrapped,
// and a negation applies to the WRAPPER: "does not carry this tag" is
// NOT EXISTS(… = …), where EXISTS(… <> …) would answer "carries some other tag"
// — a different question, and true for almost every record. The same reading
// governs a column reached through a join: `neq` answers "has no linked row
// matching this", which for a deal with no organization at all is true.
//
// `exists` binds nothing and asks about the COLUMN, not the row:
// EXISTS(… AND <expr> IS NOT NULL). The distinction only shows up once a link
// carries a nullable column. For a tag it makes no difference — taggable.tag_id
// is NOT NULL, so the added test cannot change which rows the wrapper finds —
// but for organization.industry the two readings differ, and the row reading is
// the wrong one: "which deals is the customer's industry unknown for" would
// answer "the ones with no customer", silently excluding every deal whose
// company simply has no industry recorded. Asking about the column gives one
// meaning to `exists` on every linked field.
func compileLinkLeaf(p Predicate, field Field, arg func(any) int) (string, error) {
	var inner string
	negate := false
	switch p.Op {
	case OpExists:
		present, err := existsOperand(p)
		if err != nil {
			return "", err
		}
		inner, negate = field.Expr+" IS NOT NULL", !present
	case OpIn:
		values, err := inOperand(p, field)
		if err != nil {
			return "", err
		}
		inner = fmt.Sprintf("%s = ANY($%d)", field.Expr, arg(values))
	case OpEq, OpNeq:
		value, err := scalarOperand(p.Value, field, p.Field, p.Op)
		if err != nil {
			return "", err
		}
		inner, negate = fmt.Sprintf("%s = $%d", field.Expr, arg(value)), p.Op == OpNeq
	default:
		// An operator that the link shape cannot express: the comparison builds
		// inside an EXISTS subquery where only certain operators make sense.
		// linkOperators names exactly the cases above, and no surface OFFERS an
		// operator this branch would refuse — OperatorsFor narrows a linked
		// field's advertised set to that same map. So this is the guard for a
		// filter that NAMES one anyway (a saved segment, a hand-written body),
		// not a state a picker can put a reader in.
		return "", &PredicateError{
			Field: p.Field, Code: CodeFilterOpNotAllowed,
			Message: fmt.Sprintf("operator %q does not apply to the linked field %q", p.Op, p.Field),
		}
	}
	sql := fmt.Sprintf(field.Link, inner)
	if negate {
		return "NOT " + sql, nil
	}
	return sql, nil
}
