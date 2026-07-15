// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package reporting

// The SQL-building half of the engine: the only path by which a
// caller-chosen name reaches the query text. Every identifier is looked
// up in a spec's closed vocabulary and every value travels as a bind
// parameter.

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// fromClause renders the base table (aliased t) plus the spec's fixed
// lookup joins — the one spelling shared by the aggregate plan and the
// drill-through, so both read the identical row set.
func (s reportSpec) fromClause() string {
	from := s.table + " t"
	for _, join := range s.joins {
		from += " " + join
	}
	return from
}

// buildSelectList validates the requested dimensions and aggregates
// against the spec's closed vocabulary and renders the SELECT list — the
// only path by which a caller-chosen name reaches the query text.
func buildSelectList(spec reportSpec, groupBy []string, aggregates []Aggregate) (columns, selects []string, err error) {
	for _, dim := range groupBy {
		expr, ok := spec.dimensions[dim]
		if !ok {
			return nil, nil, &FieldNotAllowedError{Field: dim}
		}
		selects = append(selects, fmt.Sprintf("%s AS %s", expr, dim))
		columns = append(columns, dim)
	}
	for _, agg := range aggregates {
		name, sel, err := aggregateSelect(spec, agg)
		if err != nil {
			return nil, nil, err
		}
		selects = append(selects, sel)
		columns = append(columns, name)
	}
	if len(selects) == 0 {
		return nil, nil, &FieldNotAllowedError{Field: "(empty plan)"}
	}
	return columns, selects, nil
}

// aggregateSelect renders one aggregate's SELECT term against the spec's
// measure vocabulary. The report plan and the derivation recompute both
// come through here, so the explained number and the explaining number
// are spelled by the same expression — reconciliation by construction.
func aggregateSelect(spec reportSpec, agg Aggregate) (name, sel string, err error) {
	name = agg.As
	if name == "" {
		name = agg.Fn
	}
	if name == reservedDerivationColumn {
		// The transport injects this key into every aggregate row; an
		// alias squatting on it would make the handle ambiguous.
		return "", "", &FieldNotAllowedError{Field: name}
	}
	switch agg.Fn {
	case "count":
		return name, fmt.Sprintf("count(*) AS %s", quoteIdent(name)), nil
	case "sum", "avg", "min", "max":
		expr, ok := spec.measures[agg.Field]
		if !ok {
			return "", "", &FieldNotAllowedError{Field: agg.Field}
		}
		return name, fmt.Sprintf("%s(%s) AS %s", agg.Fn, expr, quoteIdent(name)), nil
	default:
		return "", "", &FieldNotAllowedError{Field: "fn=" + agg.Fn}
	}
}

// fetchRows assembles the WHERE side (validated filters + the caller's
// row-scope clause), runs the plan inside the workspace-bound
// transaction, and shapes each row for the wire.
func (e *Engine) fetchRows(ctx context.Context, report string, spec reportSpec, req Request, groupBy, selects, columns []string) ([]map[string]any, error) {
	var rows []map[string]any
	err := database.WithWorkspaceTx(ctx, e.pool, func(tx pgx.Tx) error {
		var args []any
		arg := func(v any) int { args = append(args, v); return len(args) }
		where, err := buildReportWhere(ctx, spec, req, arg)
		if err != nil {
			return err
		}
		pgRows, err := tx.Query(ctx, reportSQL(spec, selects, where, groupBy), args...)
		if err != nil {
			return fmt.Errorf("report %s: %w", report, err)
		}
		defer pgRows.Close()
		rows, err = scanReportRows(pgRows, columns)
		return err
	})
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// buildReportWhere assembles the WHERE side — the spec's base predicate,
// the validated caller filters (sorted for a deterministic plan echo), and
// the caller's row-scope clause — binding every value through arg.
func buildReportWhere(ctx context.Context, spec reportSpec, req Request, arg func(any) int) ([]string, error) {
	where := []string{spec.baseWhere}
	// Deterministic filter order — the plan echo and the SQL must not
	// depend on map iteration.
	filterKeys := make([]string, 0, len(req.Filters))
	for key := range req.Filters {
		filterKeys = append(filterKeys, key)
	}
	sort.Strings(filterKeys)
	for _, key := range filterKeys {
		expr, ok := spec.filters[key]
		if !ok {
			return nil, &FieldNotAllowedError{Field: key}
		}
		where = append(where, fmt.Sprintf("%s = $%d", expr, arg(req.Filters[key])))
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

// reportSQL renders the aggregate query: the validated SELECT list over the
// spec's FROM and WHERE, grouped and ordered by the dimension positions,
// bounded by the report row limit.
func reportSQL(spec reportSpec, selects, where, groupBy []string) string {
	sql := fmt.Sprintf("SELECT %s FROM %s WHERE %s",
		strings.Join(selects, ", "), spec.fromClause(), strings.Join(where, " AND "))
	if len(groupBy) > 0 {
		positions := make([]string, len(groupBy))
		for i := range groupBy {
			positions[i] = fmt.Sprint(i + 1)
		}
		sql += " GROUP BY " + strings.Join(positions, ", ") + " ORDER BY " + strings.Join(positions, ", ")
	}
	sql += fmt.Sprintf(" LIMIT %d", reportRowLimit)
	return sql
}

// scanReportRows shapes each result row into a column→value map, rendering
// values wire-friendly.
func scanReportRows(pgRows pgx.Rows, columns []string) ([]map[string]any, error) {
	var rows []map[string]any
	for pgRows.Next() {
		values, err := pgRows.Values()
		if err != nil {
			return nil, err
		}
		row := make(map[string]any, len(columns))
		for i, col := range columns {
			row[col] = wireValue(values[i])
		}
		rows = append(rows, row)
	}
	return rows, pgRows.Err()
}

// wireValue renders driver-native values JSON-friendly: uuids as their
// canonical string, not a 16-byte array.
//
//craft:ignore naked-any report rows are schemaless by design — values arrive driver-native and leave JSON-wire-shaped
func wireValue(v any) any {
	if raw, ok := v.([16]byte); ok {
		return ids.UUID(raw).String()
	}
	return v
}

// quoteIdent admits caller-chosen aggregate aliases into the SQL text
// safely: strict identifier shape or the plan is rejected.
func quoteIdent(name string) string {
	if !identShape.MatchString(name) {
		return "result"
	}
	return name
}

var identShape = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

var errUnknownEntity = errors.New("reporting: entity outside the schema descriptors")

// RunAdHocPlan serves the datasource seam's RunReport: the plan's
// vocabulary is the schema descriptors (every declared field may group
// or filter; count is the aggregate). Used by overlay tooling and the
// seam conformance tests rather than the HTTP surface.
func (e *Engine) RunAdHocPlan(ctx context.Context, plan datasource.ReportPlan) (datasource.ReportResult, error) {
	fields, ok := e.schemaFields(plan.Entity)
	if !ok {
		return datasource.ReportResult{}, errUnknownEntity
	}
	spec := reportSpec{
		entity:       plan.Entity,
		table:        string(plan.Entity),
		baseWhere:    whereArchivedNull,
		activityWalk: plan.Entity == datasource.EntityActivity,
		dimensions:   map[string]string{},
		measures:     map[string]string{},
		filters:      map[string]string{},
		defaultAggs:  []Aggregate{{Fn: "count", As: "count"}},
	}
	for _, f := range fields {
		expr := "t." + f.Name
		spec.dimensions[f.Name] = expr
		spec.filters[f.Name] = expr
		if f.Type == "bigint" || f.Type == "integer" {
			spec.measures[f.Name] = expr
		}
	}
	req := Request{GroupBy: plan.GroupBy, Filters: map[string]any{}}
	for k, v := range plan.Filter {
		req.Filters[k] = v
	}
	outcome, err := e.runSpec(ctx, "adhoc:"+string(plan.Entity), spec, req)
	if err != nil {
		return datasource.ReportResult{}, err
	}
	result := datasource.ReportResult{Columns: outcome.Columns}
	for _, row := range outcome.Rows {
		values := make([]any, len(outcome.Columns))
		for i, col := range outcome.Columns {
			values[i] = row[col]
		}
		result.Rows = append(result.Rows, values)
	}
	return result, nil
}
