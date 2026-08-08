// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package search

// A validated plan, as SQL.
//
// Nothing a caller wrote reaches this statement as text. A field name is
// resolved through the vocabulary and then through the storage binding, so the
// identifier interpolated is one Postgres itself named; operands and document
// leaves bind as parameters. The identifier is sanitized on the way in anyway,
// because a defence that rests on an argument about provenance is one edit away
// from being wrong.
//
// Every read this builds carries what every other read on this surface carries:
// archived_at IS NULL, the branch's discovery narrowing, object RBAC, and the
// caller's row-scope clause — for the HOP as much as for the target, since a
// hop is a read of the record it lands on.

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// hopBinding is the resolved traversal: which record type the hop lands on,
// what it can answer, and which side of the edge holds the reference.
type hopBinding struct {
	relation Relation
	branch   searchBranch
	columns  *storage
	// column is the reference column, and forward says whose it is: the
	// target's (`deal.organization_id` → organization) or the hop record's
	// (organization → the deals that point back at it).
	column  string
	forward bool
}

// newHopBinding reads the edge off Relation.Via, which records the contract
// reference the relation was DERIVED from in two spellings: a bare
// `organization_id` is the target's own column, and a qualified
// `deal.organization_id` is the referring record's.
func newHopBinding(relation Relation, branch searchBranch, columns *storage) hopBinding {
	column, forward := relation.Via, true
	if _, qualified, ok := strings.Cut(relation.Via, "."); ok {
		column, forward = qualified, false
	}
	return hopBinding{relation: relation, branch: branch, columns: columns, column: column, forward: forward}
}

// planBinding is everything the compiler needs that the plan itself does not
// carry: where each record type is stored and what it can answer.
type planBinding struct {
	branch  searchBranch
	columns *storage
	hop     *hopBinding
	// candidates narrows the statement to the ids the similarity lane ranked.
	// Empty means the exact lane, which is bounded by the limit instead.
	candidates []ids.UUID
	// fetch is the row ceiling for the exact lane: the limit plus one, so a
	// truncated answer is detectable rather than merely suspected.
	fetch int
}

// planCompiler accumulates the statement's bound arguments.
type planCompiler struct {
	args []any
}

//craft:ignore naked-any a bound parameter is whatever Go type the column's kind encodes as (string, int64, float64, bool, time.Time, ids.UUID) — the kind switch IS the conversion contract, so there is no narrower signature
func (c *planCompiler) arg(v any) int {
	c.args = append(c.args, v)
	return len(c.args)
}

// compileStatement renders the statement, or admitted=false when object RBAC has
// stopped admitting a record type this plan needs. That is a mid-flight
// permission change rather than a caller error, and it answers an empty result
// for the same reason every other read on this surface does: existence-hiding
// costs nothing here and a 403 would confirm the record type is populated.
func (c *planCompiler) compileStatement(ctx context.Context, plan ValidatedPlan, binding planBinding) (string, bool, error) {
	scope, admitted, err := branchScope(ctx, binding.branch, "t", c.arg)
	if err != nil || !admitted {
		return "", false, err
	}
	where := []string{"t.archived_at IS NULL"}
	if binding.branch.extraWhere != "" {
		where = append(where, binding.branch.extraWhere)
	}
	if scope != "" {
		where = append(where, scope)
	}
	predicates, refusals := c.predicates("t", binding.columns, plan.Target, "where", plan.Plan.Where)
	where = append(where, predicates...)

	join, hopSelect, hopRefusals, hopAdmitted, err := c.lateralHop(ctx, plan, binding)
	if err != nil || !hopAdmitted {
		return "", false, err
	}
	refusals = append(refusals, hopRefusals...)
	if len(refusals) > 0 {
		return "", false, &PlanRefusal{Refusals: refusals}
	}
	if len(binding.candidates) > 0 {
		where = append(where, c.idsIn("t.id", binding.candidates))
	}

	sql := fmt.Sprintf("SELECT t.id, %s AS title%s FROM %s t%s WHERE %s",
		binding.branch.title, hopSelect, binding.branch.table, join, strings.Join(where, " AND "))
	if len(binding.candidates) > 0 {
		// The similarity lane is already bounded by the ranked candidate set,
		// and its order is the retriever's rather than the table's, so it is
		// applied after the rows come back rather than by the statement.
		return sql, true, nil
	}
	// Ids are uuidv7, so the primary key already orders by creation time: one
	// always-present, unique column, deterministic under concurrent writes
	// without a second sort key or a nullable column to reason about.
	return sql + fmt.Sprintf(" ORDER BY t.id DESC LIMIT $%d", c.arg(binding.fetch)), true, nil
}

// lateralHop renders the traversal as a LATERAL join that returns ONE matching
// hop row, which is what makes the hop legible as evidence rather than as an
// invisible filter. An EXISTS would answer the same rows and explain none of
// them.
//
// The hop carries its own admission and its own row scope: a caller who cannot
// see the Stuttgart organization cannot use it to select deals either.
func (c *planCompiler) lateralHop(ctx context.Context, plan ValidatedPlan, binding planBinding) (
	join, selection string, refusals []apperrors.FieldRefusal, admitted bool, err error,
) {
	if binding.hop == nil {
		return "", "", nil, true, nil
	}
	hop := binding.hop
	scope, admitted, err := branchScope(ctx, hop.branch, "h", c.arg)
	if err != nil || !admitted {
		return "", "", nil, false, err
	}
	where := []string{"h.archived_at IS NULL", c.edgeCondition(*hop)}
	if scope != "" {
		where = append(where, scope)
	}
	predicates, refusals := c.predicates("h", hop.columns, plan.HopVocabulary, "traverse.where", plan.Plan.Traverse.Where)
	where = append(where, predicates...)

	// ORDER BY keeps the evidence deterministic when several hop rows match;
	// the row's membership never depends on which one is returned.
	join = fmt.Sprintf(
		" JOIN LATERAL (SELECT h.id AS hop_id, %s AS hop_title FROM %s h WHERE %s ORDER BY h.id LIMIT 1) hop ON true",
		hop.branch.title, hop.branch.table, strings.Join(where, " AND "),
	)
	return join, ", hop.hop_id, hop.hop_title", refusals, true, nil
}

// edgeCondition joins the two records on the reference the relation was
// derived from, in whichever direction declares it.
func (c *planCompiler) edgeCondition(hop hopBinding) string {
	column := sanitize(hop.column)
	if hop.forward {
		return "h.id = t." + column
	}
	return "h." + column + " = t.id"
}

// predicates renders one where-list, reporting a refusal per clause it cannot
// bind rather than the first — a caller told about one of three operand faults
// makes three round trips to learn what one answer could have carried.
func (c *planCompiler) predicates(alias string, columns *storage, vocab TargetVocabulary, path string, clauses []Predicate) ([]string, []apperrors.FieldRefusal) {
	var (
		fragments []string
		refusals  []apperrors.FieldRefusal
	)
	for i, clause := range clauses {
		at := path + "[" + strconv.Itoa(i) + "]"
		fragment, refusal := c.clause(alias, columns, vocab, at, clause)
		if refusal != nil {
			refusals = append(refusals, *refusal)
			continue
		}
		fragments = append(fragments, fragment)
	}
	return fragments, refusals
}

// clause renders one `field op value`.
func (c *planCompiler) clause(alias string, columns *storage, vocab TargetVocabulary, at string, clause Predicate) (string, *apperrors.FieldRefusal) {
	field, ok := vocab.Field(clause.Field)
	if !ok {
		// Unreachable through Execute: the validator settled membership first,
		// against this same vocabulary. Reaching it means the executor was
		// handed a plan that never passed validation, which is a wiring fault
		// to fail loudly on rather than a caller to explain it to.
		return "", &apperrors.FieldRefusal{
			Field: at + ".field", Code: CodeUnknownField,
			Message: "the query plan cannot name " + quote(clause.Field) + " on " + quote(vocab.Target),
		}
	}
	expr, ok := columns.expr(alias, field)
	if !ok {
		// Same shape, same reason: a published field compiles, and the fitness
		// function is what keeps that true.
		return "", &apperrors.FieldRefusal{
			Field: at + ".field", Code: CodeUnknownField,
			Message: quote(clause.Field) + " cannot be answered by this workspace's records",
		}
	}
	if clause.Op == OpIn {
		return c.inClause(expr, at, field, clause)
	}
	value, cast, refusal := c.bind(at+"."+memberValue, field, clause.Value)
	if refusal != nil {
		return "", refusal
	}
	operand := fmt.Sprintf("$%d%s", c.arg(value), cast)
	if clause.Op == OpNeq {
		// IS DISTINCT FROM rather than <>: a field that is UNSET is distinct
		// from every value, and three-valued logic would otherwise drop those
		// rows from an answer the caller reads as "everything that is not X".
		return expr + " IS DISTINCT FROM " + operand, nil
	}
	comparator, ok := sqlComparators[clause.Op]
	if !ok {
		return "", &apperrors.FieldRefusal{
			Field: at + ".op", Code: CodeUnknownOperator,
			Message: quote(clause.Op) + " is not an operator this workspace can answer",
		}
	}
	return expr + " " + comparator + " " + operand, nil
}

// inClause renders a membership test as an explicit parameter list. Each
// element binds under the field's own kind, so a list carrying one operand of
// the wrong shape is refused naming that element rather than the clause.
func (c *planCompiler) inClause(expr, at string, field Field, clause Predicate) (string, *apperrors.FieldRefusal) {
	var values []json.RawMessage
	if err := json.Unmarshal(clause.Values, &values); err != nil {
		return "", &apperrors.FieldRefusal{
			Field: at + "." + memberValues, Code: CodeValueMissing,
			Message: quote(OpIn) + " needs a non-empty " + quote(memberValues) + " list",
		}
	}
	operands := make([]string, len(values))
	for i, raw := range values {
		value, cast, refusal := c.bind(at+"."+memberValues+"["+strconv.Itoa(i)+"]", field, raw)
		if refusal != nil {
			return "", refusal
		}
		operands[i] = fmt.Sprintf("$%d%s", c.arg(value), cast)
	}
	return expr + " IN (" + strings.Join(operands, ", ") + ")", nil
}

// sqlComparators maps the ordered and equality operators onto their SQL
// spelling. `neq` and `in` are absent deliberately — both need more than a
// comparator, and putting a wrong one here would be silent.
var sqlComparators = map[string]string{
	OpEq:  "=",
	OpLt:  "<",
	OpLte: "<=",
	OpGt:  ">",
	OpGte: ">=",
}

// dateLayout is the contract's date encoding, and timestampLayouts the two
// spellings a caller may send an instant in.
const dateLayout = "2006-01-02"

// bind turns one JSON operand into a bound parameter under the field's kind,
// with the cast the comparison needs.
//
// This is where a FORMAT is checked. The validator deliberately left it here
// ("their format is the executor's business"): it had a shape to compare
// against and no calendar, and refusing `"next tuesday"` at the moment it
// would become a parameter is what keeps a malformed date a refusal rather
// than a query that quietly matches nothing.
//
//craft:ignore naked-any a bound parameter is whatever Go type the column's kind encodes as (string, int64, float64, bool, time.Time, ids.UUID) — the kind switch IS the conversion contract, so there is no narrower signature
func (c *planCompiler) bind(at string, field Field, raw json.RawMessage) (any, string, *apperrors.FieldRefusal) {
	switch field.Kind {
	case KindNumber:
		return bindNumber(at, field, raw)
	case KindBoolean:
		var value bool
		return value, "", decodeOperand(at, field, raw, &value, "true or false")
	case KindID:
		return bindID(at, field, raw)
	case KindDate:
		return bindTemporal(at, field, raw, dateLayout, "::date", "a date, as YYYY-MM-DD")
	case KindTimestamp:
		return bindTemporal(at, field, raw, time.RFC3339, "", "an instant, as RFC 3339 (2026-08-08T09:00:00Z)")
	case KindText:
		var value string
		return value, "", decodeOperand(at, field, raw, &value, "text")
	case KindGeo:
		// A place is never compared: within_radius answers
		// distance_ranking_unavailable and the executor stops before here.
		return nil, "", operandFault(at, field, "a place, which this deployment cannot rank by")
	default:
		return nil, "", operandFault(at, field, "a value of a kind this workspace can compare")
	}
}

//craft:ignore naked-any a bound parameter is whatever Go type the column's kind encodes as (string, int64, float64, bool, time.Time, ids.UUID) — the kind switch IS the conversion contract, so there is no narrower signature
func bindNumber(at string, field Field, raw json.RawMessage) (any, string, *apperrors.FieldRefusal) {
	var value json.Number
	if refusal := decodeOperand(at, field, raw, &value, "a number"); refusal != nil {
		return nil, "", refusal
	}
	// A whole number binds as one, so a bigint column compares against a
	// bigint rather than against a float that rounded on the way in.
	if whole, err := value.Int64(); err == nil {
		return whole, "", nil
	}
	fractional, err := value.Float64()
	if err != nil {
		return nil, "", operandFault(at, field, "a number")
	}
	return fractional, "", nil
}

//craft:ignore naked-any a bound parameter is whatever Go type the column's kind encodes as (string, int64, float64, bool, time.Time, ids.UUID) — the kind switch IS the conversion contract, so there is no narrower signature
func bindID(at string, field Field, raw json.RawMessage) (any, string, *apperrors.FieldRefusal) {
	var text string
	if refusal := decodeOperand(at, field, raw, &text, "an identifier"); refusal != nil {
		return nil, "", refusal
	}
	id, err := ids.Parse(text)
	if err != nil {
		return nil, "", operandFault(at, field, "an identifier, as a UUID")
	}
	return id, "", nil
}

// bindTemporal parses a date or an instant in the contract's own encoding and
// binds it as TEXT with an explicit cast where one is needed. A date compared
// through a timestamp would be resolved at the session's time zone, which
// makes the same plan answer differently on two servers.
//
//craft:ignore naked-any a bound parameter is whatever Go type the column's kind encodes as (string, int64, float64, bool, time.Time, ids.UUID) — the kind switch IS the conversion contract, so there is no narrower signature
func bindTemporal(at string, field Field, raw json.RawMessage, layout, cast, shape string) (any, string, *apperrors.FieldRefusal) {
	var text string
	if refusal := decodeOperand(at, field, raw, &text, shape); refusal != nil {
		return nil, "", refusal
	}
	parsed, err := time.Parse(layout, text)
	if err != nil {
		return nil, "", operandFault(at, field, shape)
	}
	if cast == "" {
		return parsed, "", nil
	}
	return parsed.Format(layout), cast, nil
}

// decodeOperand decodes one operand into its Go type, which IS the check: a
// number offered where text belongs fails at the decode rather than after it.
//
//craft:ignore naked-any `into` is the caller's own destination for one operand; decoding INTO its Go type is the check, and a narrower signature would have to name every kind
func decodeOperand(at string, field Field, raw json.RawMessage, into any, shape string) *apperrors.FieldRefusal {
	if len(raw) == 0 || isJSONNull(raw) || json.Unmarshal(raw, into) != nil {
		return operandFault(at, field, shape)
	}
	return nil
}

func operandFault(at string, field Field, shape string) *apperrors.FieldRefusal {
	return &apperrors.FieldRefusal{
		Field: at, Code: CodeValueTypeMismatch,
		Message: quote(field.Name) + " is a " + string(field.Kind) + " field; its operand must be " + shape,
	}
}

// idsIn renders a membership test over already-resolved ids. They are this
// server's own, so the list is bound rather than rendered, and it exists only
// to narrow the statement to what the similarity lane ranked.
func (c *planCompiler) idsIn(expr string, values []ids.UUID) string {
	operands := make([]string, len(values))
	for i, id := range values {
		operands[i] = "$" + strconv.Itoa(c.arg(id))
	}
	return expr + " IN (" + strings.Join(operands, ", ") + ")"
}
