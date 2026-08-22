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
	"maps"
	"net/url"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// reservedDerivationColumn is the key the transport injects into every
// aggregate row; the plan validator refuses aliases that squat on it.
const reservedDerivationColumn = "derivation_url"

// nullPredicateKey names the fields a handle pins as SQL NULL.
//
// It exists because a query string cannot otherwise tell "this column is unset"
// from "this column holds the empty string": both used to render as `field=`,
// and the resolver read every empty value as NULL. A dimension whose column is
// text with no CHECK against "" — deal.source is the first in this catalog —
// therefore reported a bucket of N rows and minted a handle that resolved to
// none, from the same response. The value slot now always means a VALUE, and
// absence is stated separately.
const nullPredicateKey = "isnull"

// reservedDerivationKeys are the query-string names a handle owns. Report
// vocabularies may not squat on them, or a minted URL would be ambiguous.
// Derived from here rather than restated, so adding a key updates the gate.
var reservedDerivationKeys = []string{"by", "agg", nullPredicateKey, reservedDerivationColumn}

// derivationQuery is one parsed derivation handle: the equality
// predicates that pin the explained cell (plan filters + the row's
// group-key values), which of those keys were grouping dimensions, and
// the aggregates being explained.
type derivationQuery struct {
	// Predicates bind field → value. The empty string means the empty string;
	// an unset column is named in Unset instead.
	Predicates map[string]string
	// Unset is the fields pinned as SQL NULL.
	Unset      map[string]bool
	GroupBy    []string
	Aggregates []reportAggregate
}

// derivationOutcome is a resolved handle: definition, drill-through
// rows, and the aggregates recomputed over exactly those rows.
type derivationOutcome struct {
	Report     string
	Definition string
	Plan       map[string]any
	Columns    []string
	Rows       []map[string]any
	Aggregates map[string]any
	TotalRows  int
	// ExcludedByPermission counts the visible rows a field mask withheld —
	// nil when no mask applied, exactly like the report envelope it explains.
	ExcludedByPermission *int
	GeneratedAt          time.Time
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
		setPredicate(values, key, filters[key])
	}
	if row != nil {
		for _, dim := range groupBy {
			values.Add("by", dim)
			setPredicate(values, dim, row[dim])
		}
	}
	return "/v1/reports/" + url.PathEscape(report) + "/derivation?" + values.Encode()
}

// setPredicate writes one bound field into the handle: an unset column is named
// under nullPredicateKey, anything else carries its rendered value. Keeping the
// two apart is what stops the empty string from reading as NULL.
//
//craft:ignore naked-any handle values arrive from JSON plan echoes and wire-shaped report rows — schemaless by design
func setPredicate(values url.Values, key string, v any) {
	if v == nil {
		values.Add(nullPredicateKey, key)
		return
	}
	values.Set(key, predicateString(v))
}

// predicateString renders a bound value for the handle's query string. Never
// called with nil — setPredicate spells that case separately.
//
//craft:ignore naked-any handle values arrive from JSON plan echoes and wire-shaped report rows — schemaless by design
func predicateString(v any) string {
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
	q := derivationQuery{Predicates: map[string]string{}, Unset: map[string]bool{}}
	for key, vals := range values {
		switch key {
		case "by":
			q.GroupBy = append(q.GroupBy, vals...)
		case nullPredicateKey:
			for _, field := range vals {
				q.Unset[field] = true
			}
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
	for field := range q.Unset {
		// One field cannot be both unset and equal to something; a handle
		// saying both is not one this engine ever minted.
		if _, ok := q.Predicates[field]; ok {
			return derivationQuery{}, &FieldNotAllowedError{Field: field}
		}
	}
	return q, nil
}

// boundExpr is one validated predicate: the vocabulary field, its fixed
// SQL expression, and the bound value ("" = SQL NULL).
type boundExpr struct {
	field, expr, value string
	// isNull pins the column as unset rather than equal to value. Separate from
	// an empty value, which now means the empty string and nothing else.
	isNull bool
	// threshold, when set, renders the predicate itself over the bound number
	// (reportthreshold.go); expr is empty and value is the decimal the handle
	// carried.
	threshold *reportThreshold
}

// derivationPlan is a compiled handle: validated predicates, the
// drill-through SELECT list, the aggregate recompute list, and the
// plain-language definition — everything but the execution.
type derivationPlan struct {
	preds []boundExpr
	// predicates is the handle's raw field → value map, kept for the scoped
	// filter gate the execution half runs before the WHERE side binds.
	predicates map[string]string
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
	// A handle naming a dimension or measure the caller may not read is
	// refused, like the plan that would have minted it; the columns the handle
	// did not name are narrowed to what the caller may see.
	if err := requireVocabularyGrants(ctx, spec, slices.Concat(q.GroupBy, aggregateFields(q.Aggregates), slices.Sorted(maps.Keys(q.Predicates)))); err != nil {
		return derivationOutcome{}, err
	}
	spec = grantedSpec(ctx, spec)
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
	plan := derivationPlan{aggregates: q.Aggregates, predicates: q.Predicates}
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
		if threshold, ok := spec.thresholds[key]; ok {
			plan.preds = append(plan.preds, boundExpr{field: key, value: value, threshold: &threshold})
			continue
		}
		expr, ok := spec.dimensions[key]
		if !ok {
			expr, ok = spec.filters[key]
		}
		if !ok {
			return derivationPlan{}, &FieldNotAllowedError{Field: key}
		}
		plan.preds = append(plan.preds, boundExpr{field: key, expr: expr, value: value})
	}
	for field := range q.Unset {
		expr, ok := spec.dimensions[field]
		if !ok {
			expr, ok = spec.filters[field]
		}
		if !ok {
			return derivationPlan{}, &FieldNotAllowedError{Field: field}
		}
		plan.preds = append(plan.preds, boundExpr{field: field, expr: expr, isNull: true})
	}
	sort.Slice(plan.preds, func(i, j int) bool { return plan.preds[i].field < plan.preds[j].field })

	var filterPreds, groupPreds []boundPredicate
	for _, p := range plan.preds {
		bp := boundPredicate{Field: p.field, Value: p.value, IsNull: p.isNull}
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

// grantedSpec is the spec with the dimensions and measures this caller may
// not read removed, so a drill-through selects only what the caller could
// have grouped or aggregated by themselves.
func grantedSpec(ctx context.Context, spec reportSpec) reportSpec {
	if len(spec.grants) == 0 {
		return spec
	}
	narrowed := spec
	narrowed.dimensions = map[string]string{}
	for _, name := range grantedNames(ctx, spec, spec.dimensions) {
		narrowed.dimensions[name] = spec.dimensions[name]
	}
	narrowed.measures = map[string]string{}
	for _, name := range grantedNames(ctx, spec, spec.measures) {
		narrowed.measures[name] = spec.measures[name]
	}
	narrowed.defaultAggs = grantedDefaultAggregates(ctx, spec)
	if len(narrowed.measures) < len(spec.measures) {
		// A reading order spelled over a withheld measure would still rank
		// the rows by it, which tells the caller what the numbers were.
		narrowed.orderBy = ""
	}
	return narrowed
}

// boundPredicate is one field = value binding rendered into the
// plain-language definition.
type boundPredicate struct {
	Field  string
	Value  string
	IsNull bool
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
		if p.IsNull {
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
