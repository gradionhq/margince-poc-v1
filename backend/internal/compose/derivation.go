// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// "Explain This Number" (B-E09.9, features/03 §1.3): every aggregate a
// report returns carries a derivation handle; resolving it yields the
// plain-language definition of the exact filter+group+aggregate plus
// the underlying source rows. The drill-through runs the SAME vocabulary,
// FROM clause, and row-scope clause as the report, so the explanation
// can never out-see — or disagree with — the number it explains.

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// reservedDerivationColumn is the key the transport injects into every
// aggregate row; the plan validator refuses aliases that squat on it.
const reservedDerivationColumn = "derivation_url"

// derivationQuery is one parsed derivation handle: the equality
// predicates that pin the explained cell (plan filters + the row's
// group-key values), which of those keys were grouping dimensions, and
// the aggregates being explained.
type derivationQuery struct {
	// Predicates bind field → value; the empty string means SQL NULL
	// (the "no owner" group has no other wire spelling in a URL).
	Predicates map[string]string
	GroupBy    []string
	Aggregates []reportAggregate
}

// derivationOutcome is a resolved handle: definition, drill-through
// rows, and the aggregates recomputed over exactly those rows.
type derivationOutcome struct {
	Report      string
	Definition  string
	Plan        map[string]any
	Columns     []string
	Rows        []map[string]any
	Aggregates  map[string]any
	TotalRows   int
	GeneratedAt time.Time
}

// derivationURL mints the handle for one aggregate row (or, with a nil
// row, for the whole filtered result). parseDerivationQuery is its exact
// inverse; the round trip is unit-tested so a handle we mint always
// resolves.
func derivationURL(report string, filters map[string]any, groupBy []string, aggregates []reportAggregate, row map[string]any) string {
	values := url.Values{}
	for _, agg := range aggregates {
		values.Add("agg", agg.Fn+":"+agg.Field+":"+agg.As)
	}
	filterKeys := make([]string, 0, len(filters))
	for key := range filters {
		filterKeys = append(filterKeys, key)
	}
	sort.Strings(filterKeys)
	for _, key := range filterKeys {
		values.Set(key, predicateString(filters[key]))
	}
	if row != nil {
		for _, dim := range groupBy {
			values.Add("by", dim)
			values.Set(dim, predicateString(row[dim]))
		}
	}
	return "/v1/reports/" + url.PathEscape(report) + "/derivation?" + values.Encode()
}

// predicateString renders a bound value for the handle's query string;
// nil becomes the empty string, the URL spelling of SQL NULL.
//
//craft:ignore naked-any handle values arrive from JSON plan echoes and wire-shaped report rows — schemaless by design
func predicateString(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

// parseDerivationQuery reads a handle's query string back into the
// typed form: `by` and `agg` are the reserved plan keys, every other
// parameter is an equality predicate to be validated against the
// report's closed vocabulary.
func parseDerivationQuery(values url.Values) (derivationQuery, error) {
	q := derivationQuery{Predicates: map[string]string{}}
	for key, vals := range values {
		switch key {
		case "by":
			q.GroupBy = append(q.GroupBy, vals...)
		case "agg":
			for _, v := range vals {
				parts := strings.SplitN(v, ":", 3)
				if len(parts) != 3 || parts[0] == "" {
					return derivationQuery{}, &FieldNotAllowedError{Field: "agg=" + v}
				}
				q.Aggregates = append(q.Aggregates, reportAggregate{Fn: parts[0], Field: parts[1], As: parts[2]})
			}
		default:
			if len(vals) != 1 {
				// One cell binds one value per field; a repeated key is
				// not a plan this engine ever minted.
				return derivationQuery{}, &FieldNotAllowedError{Field: key}
			}
			q.Predicates[key] = vals[0]
		}
	}
	sort.Strings(q.GroupBy)
	return q, nil
}

// boundExpr is one validated predicate: the vocabulary field, its fixed
// SQL expression, and the bound value ("" = SQL NULL).
type boundExpr struct {
	field, expr, value string
}

// derivationPlan is a compiled handle: validated predicates, the
// drill-through SELECT list, the aggregate recompute list, and the
// plain-language definition — everything but the execution.
type derivationPlan struct {
	preds      []boundExpr
	definition string
	aggregates []reportAggregate
	columns    []string // drill-through output names, aligned with selects
	selects    []string
	aggColumns []string
	aggSelects []string
}

// Derive resolves one handle against a prebuilt report's vocabulary.
func (e *reportEngine) Derive(ctx context.Context, report string, q derivationQuery) (derivationOutcome, error) {
	if uuidShape.MatchString(report) {
		// Saved reports are a later slice; an unknown id is absent, not
		// half-supported (same rule as Run).
		return derivationOutcome{}, fmt.Errorf("saved report %s: %w", report, apperrors.ErrNotFound)
	}
	spec, ok := prebuiltReports[report]
	if !ok {
		return derivationOutcome{}, fmt.Errorf("report %q: %w", report, apperrors.ErrNotFound)
	}
	if err := auth.Require(ctx, string(spec.entity), principal.ActionRead); err != nil {
		return derivationOutcome{}, err
	}
	plan, err := compileDerivation(spec, q)
	if err != nil {
		return derivationOutcome{}, err
	}
	out := derivationOutcome{
		Report:     report,
		Definition: plan.definition,
		Plan: map[string]any{
			"object":     string(spec.entity),
			"predicates": q.Predicates,
			"group_by":   q.GroupBy,
			"aggregates": plan.aggregates,
		},
		Columns:     plan.columns,
		Aggregates:  map[string]any{},
		GeneratedAt: time.Now().UTC(),
	}
	if err := e.fetchDerivation(ctx, report, spec, plan, &out); err != nil {
		return derivationOutcome{}, err
	}
	return out, nil
}

// compileDerivation validates a parsed handle against the report's
// closed vocabulary and renders every SQL fragment and the definition.
func compileDerivation(spec reportSpec, q derivationQuery) (derivationPlan, error) {
	plan := derivationPlan{aggregates: q.Aggregates}
	if len(plan.aggregates) == 0 {
		plan.aggregates = spec.defaultAggs
	}

	grouped := map[string]bool{}
	for _, dim := range q.GroupBy {
		if _, ok := spec.dimensions[dim]; !ok {
			return derivationPlan{}, &FieldNotAllowedError{Field: dim}
		}
		grouped[dim] = true
	}
	// Predicates admit the union of the report's dimensions and filters:
	// a group-key value pins the cell, a filter value replays the plan.
	for key, value := range q.Predicates {
		expr, ok := spec.dimensions[key]
		if !ok {
			expr, ok = spec.filters[key]
		}
		if !ok {
			return derivationPlan{}, &FieldNotAllowedError{Field: key}
		}
		plan.preds = append(plan.preds, boundExpr{field: key, expr: expr, value: value})
	}
	sort.Slice(plan.preds, func(i, j int) bool { return plan.preds[i].field < plan.preds[j].field })

	var filterPreds, groupPreds []boundPredicate
	for _, p := range plan.preds {
		bp := boundPredicate{Field: p.field, Value: p.value}
		if grouped[p.field] {
			groupPreds = append(groupPreds, bp)
		} else {
			filterPreds = append(filterPreds, bp)
		}
	}
	definition, err := renderDefinition(spec, filterPreds, groupPreds, plan.aggregates)
	if err != nil {
		return derivationPlan{}, err
	}
	plan.definition = definition

	// Drill-through columns: the row identity plus every dimension and
	// measure the vocabulary declares — a derived measure (e.g. the
	// weighted value) sits NEXT TO its inputs, so the lineage bottoms
	// out at base values with no opaque intermediate step.
	plan.columns = []string{"id"}
	plan.selects = []string{"t.id AS id"}
	for _, name := range sortedKeys(spec.dimensions) {
		plan.columns = append(plan.columns, name)
		plan.selects = append(plan.selects, spec.dimensions[name]+" AS "+name)
	}
	for _, name := range sortedKeys(spec.measures) {
		plan.columns = append(plan.columns, name)
		plan.selects = append(plan.selects, spec.measures[name]+" AS "+name)
	}
	for _, agg := range plan.aggregates {
		name, sel, err := aggregateSelect(spec, agg)
		if err != nil {
			return derivationPlan{}, err
		}
		plan.aggColumns = append(plan.aggColumns, name)
		plan.aggSelects = append(plan.aggSelects, sel)
	}
	return plan, nil
}

// fetchDerivation executes a compiled plan: the drill-through rows and
// the aggregate recompute run over the identical WHERE side (validated
// predicates + the caller's row-scope clause) in one transaction.
func (e *reportEngine) fetchDerivation(ctx context.Context, report string, spec reportSpec, plan derivationPlan, out *derivationOutcome) error {
	return database.WithWorkspaceTx(ctx, e.pool, func(tx pgx.Tx) error {
		var args []any
		arg := func(v any) int { args = append(args, v); return len(args) }

		where, err := derivationWhere(ctx, spec, plan, arg)
		if err != nil {
			return err
		}
		whereSQL := strings.Join(where, " AND ")

		rowsSQL := fmt.Sprintf("SELECT %s FROM %s WHERE %s ORDER BY t.id LIMIT %d",
			strings.Join(plan.selects, ", "), spec.fromClause(), whereSQL, reportRowLimit)
		pgRows, err := tx.Query(ctx, rowsSQL, args...)
		if err != nil {
			return fmt.Errorf("derivation %s rows: %w", report, err)
		}
		defer pgRows.Close()
		out.Rows, err = scanDerivationRows(pgRows, plan.columns)
		if err != nil {
			return err
		}
		pgRows.Close()

		// Recompute the explained aggregates over the identical row set
		// (count(*) rides along as the honest total behind the capped
		// rows slice). Values are read positionally, so a caller alias
		// cannot shadow the total.
		aggSQL := fmt.Sprintf("SELECT count(*), %s FROM %s WHERE %s",
			strings.Join(plan.aggSelects, ", "), spec.fromClause(), whereSQL)
		values := make([]any, len(plan.aggColumns)+1)
		ptrs := make([]any, len(values))
		for i := range values {
			ptrs[i] = &values[i]
		}
		if err := tx.QueryRow(ctx, aggSQL, args...).Scan(ptrs...); err != nil {
			return fmt.Errorf("derivation %s aggregates: %w", report, err)
		}
		total, ok := values[0].(int64)
		if !ok {
			return fmt.Errorf("derivation %s: count(*) returned %T, not int64", report, values[0])
		}
		out.TotalRows = int(total)
		for i, name := range plan.aggColumns {
			out.Aggregates[name] = wireValue(values[i+1])
		}
		return nil
	})
}

// derivationWhere renders the drill-through's WHERE side: the report's
// base predicate, the validated equality predicates ("" = SQL NULL), and
// the caller's row-scope clause (the activity link-walk when the report
// rides on activities). The identical clause backs both the rows query
// and the aggregate recompute, so the explanation can never out-see the
// number it explains.
func derivationWhere(ctx context.Context, spec reportSpec, plan derivationPlan, arg func(any) int) ([]string, error) {
	where := []string{spec.baseWhere}
	for _, p := range plan.preds {
		if p.value == "" {
			where = append(where, p.expr+" IS NULL")
		} else {
			where = append(where, fmt.Sprintf("%s = $%d", p.expr, arg(p.value)))
		}
	}
	var scope string
	var err error
	if spec.activityWalk {
		scope, err = auth.ActivityScopeClause(ctx, "t", arg)
	} else {
		scope, err = auth.ScopeClauseFor(ctx, string(spec.entity), "t", arg)
	}
	if err != nil {
		return nil, err
	}
	if scope != "" {
		where = append(where, scope)
	}
	return where, nil
}

// scanDerivationRows materializes the drill-through rows, mapping each
// column to its wire value positionally.
func scanDerivationRows(pgRows pgx.Rows, columns []string) ([]map[string]any, error) {
	var out []map[string]any
	for pgRows.Next() {
		values, err := pgRows.Values()
		if err != nil {
			return nil, err
		}
		row := make(map[string]any, len(columns))
		for i, col := range columns {
			row[col] = wireValue(values[i])
		}
		out = append(out, row)
	}
	if err := pgRows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// boundPredicate is one field = value binding rendered into the
// plain-language definition.
type boundPredicate struct {
	Field string
	Value string
}

// renderDefinition writes the exact filter+group+aggregate as one plain
// English sentence — the (a) half of AC-R6. Pure; unit-tested against
// golden strings.
func renderDefinition(spec reportSpec, filters, groups []boundPredicate, aggregates []reportAggregate) (string, error) {
	var b strings.Builder
	b.WriteString("Over ")
	if spec.basePlain != "" {
		b.WriteString(spec.basePlain)
	} else {
		b.WriteString(string(spec.entity) + " records")
	}
	if len(filters) > 0 {
		b.WriteString(", filtered to " + renderPredicates(filters))
	}
	if len(groups) > 0 {
		b.WriteString(", within the group where " + renderPredicates(groups))
	}
	b.WriteString(": ")
	phrases := make([]string, 0, len(aggregates))
	for _, agg := range aggregates {
		phrase, err := aggregatePhrase(agg)
		if err != nil {
			return "", err
		}
		phrases = append(phrases, phrase)
	}
	b.WriteString(strings.Join(phrases, "; "))
	b.WriteString(".")
	return b.String(), nil
}

func renderPredicates(preds []boundPredicate) string {
	parts := make([]string, len(preds))
	for i, p := range preds {
		if p.Value == "" {
			parts[i] = p.Field + " is not set"
		} else {
			parts[i] = fmt.Sprintf("%s = %q", p.Field, p.Value)
		}
	}
	return strings.Join(parts, " and ")
}

func aggregatePhrase(agg reportAggregate) (string, error) {
	verbs := map[string]string{
		"count": "the number of matching records",
		"sum":   "the sum of",
		"avg":   "the average of",
		"min":   "the minimum of",
		"max":   "the maximum of",
	}
	verb, ok := verbs[agg.Fn]
	if !ok {
		return "", &FieldNotAllowedError{Field: "fn=" + agg.Fn}
	}
	phrase := verb
	if agg.Fn != "count" {
		phrase += " " + agg.Field
	}
	if agg.As != "" {
		phrase += " as " + agg.As
	}
	return phrase, nil
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
