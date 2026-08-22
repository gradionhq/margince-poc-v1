// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The report query text: rendering a validated plan into SQL and shaping the
// rows that come back. Every identifier here is a fixed expression the
// report's spec supplied and every caller value travels as a bind parameter,
// which is why this stays the ONE place report SQL is assembled — a second
// builder is how a typed plan quietly becomes free SQL again.

import (
	"context"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/identity"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// sqlUnnarrowed is the clause an unbounded reader's scope renders to where a
// predicate is syntactically required: every row passes.
const sqlUnnarrowed = "TRUE"

// reportRowLimit bounds any report; aggregates past this are a data
// export, not a report.
const reportRowLimit = 1000

// fetchRows assembles the WHERE side (validated filters + the caller's
// row-scope clause), runs the plan inside the workspace-bound
// transaction, and shapes each row for the wire.
func (e *reportEngine) fetchRows(ctx context.Context, report string, spec reportSpec, req reportRequest, groupBy, selects, columns []string) ([]map[string]any, *int, error) {
	var rows []map[string]any
	var excluded *int
	err := database.WithWorkspaceTx(ctx, e.pool, func(tx pgx.Tx) error {
		if err := requireFilterScopes(ctx, tx, spec, req.Filters); err != nil {
			return err
		}
		var args []any
		arg := func(v any) int { args = append(args, v); return len(args) }
		where, err := buildReportWhere(ctx, spec, req, arg)
		if err != nil {
			return err
		}
		// Field masks exclude their rows from the whole statement — the
		// aggregate and the drill-through must keep reading the identical
		// row set (reportmask.go) — and the withheld count rides the
		// envelope as excluded_by_permission.
		maskClauses, masked, err := maskExclusionClauses(ctx, spec, arg)
		if err != nil {
			return err
		}
		if masked {
			n, err := countMaskExcluded(ctx, tx, spec, where, maskClauses, args)
			if err != nil {
				return err
			}
			excluded = &n
			where = append(where, maskClauses...)
		}
		sql, args, err := bindReportTokens(ctx, tx, reportSQL(spec, selects, where, groupBy), args)
		if err != nil {
			return err
		}
		pgRows, err := tx.Query(ctx, sql, args...)
		if err != nil {
			return fmt.Errorf("report %s: %w", report, err)
		}
		defer pgRows.Close()
		rows, err = scanReportRows(pgRows, columns)
		return err
	})
	if err != nil {
		return nil, nil, err
	}
	return rows, excluded, nil
}

// buildReportWhere assembles the WHERE side — the spec's base predicate,
// the validated caller filters (sorted for a deterministic plan echo), and
// the caller's row-scope clause — binding every value through arg.
func buildReportWhere(ctx context.Context, spec reportSpec, req reportRequest, arg func(any) int) ([]string, error) {
	where := []string{spec.baseWhere}
	// Deterministic filter order — the plan echo and the SQL must not
	// depend on map iteration.
	filterKeys := make([]string, 0, len(req.Filters))
	for key := range req.Filters {
		filterKeys = append(filterKeys, key)
	}
	sort.Strings(filterKeys)
	for _, key := range filterKeys {
		if threshold, ok := spec.thresholds[key]; ok {
			n, err := thresholdValue(key, req.Filters[key])
			if err != nil {
				return nil, err
			}
			where = append(where, threshold.clause(arg(n)))
			continue
		}
		expr, ok := spec.filters[key]
		if !ok {
			return nil, &FieldNotAllowedError{Field: key, Slot: slotFilters, Allowed: allowedReportNames(spec.filters)}
		}
		// A null filter means "not set", the SAME meaning the drill-through
		// gives an empty group key (derivationWhere). Binding it as `= NULL`
		// instead — which is never true — let one response answer "no rows"
		// while the derivation handle it minted in the same breath answered
		// with every unset row. Two doors onto one question have to agree, or
		// the explanation disagrees with the number it explains.
		if req.Filters[key] == nil {
			where = append(where, expr+" IS NULL")
			continue
		}
		value, err := reportFilterValue(key, req.Filters[key])
		if err != nil {
			return nil, err
		}
		where = append(where, fmt.Sprintf("%s = $%d", expr, arg(value)))
	}
	var scope string
	var err error
	if spec.activityWalk {
		scope, err = auth.ActivityContentClause(ctx, "t", arg)
	} else {
		scope, err = auth.ScopeClauseFor(ctx, string(spec.entity), "t", arg)
	}
	if err != nil {
		return nil, err
	}
	if scope != "" {
		where = append(where, scope)
	}
	refs, err := referenceScopeClauses(ctx, spec, arg)
	if err != nil {
		return nil, err
	}
	return append(where, refs...), nil
}

// referenceScopeClauses narrows a report to the rows whose REFERENCED records
// the caller could open, for every reference the spec declares.
//
// The engine's own gate covers the report's entity and nothing it points at, so
// a dimension over a reference column would otherwise hand back ids that the
// same caller's ordinary read of the same row masks. Excluding the row is the
// only honest aggregate answer: there is no per-row place to write "withheld",
// and folding those deals under a null key would still say that SOME partner
// brought them.
//
// This is the ROW-SCOPE obligation only. A reference that also takes the
// referenced table's OBJECT grant before it may be named at all (the project
// an activity is filed under) declares that in spec.grants, and the
// vocabulary gate (reportgrants.go) refuses the plan by name; a row scope
// clause that renders empty means an unbounded reader of the table and
// nothing about the grant, which is why the two are asked separately.
// TestGroupingByPartnerDoesNotNameAPartnerTheCallerCannotOpen pins the
// partner dimension to the row scope alone.
func referenceScopeClauses(ctx context.Context, spec reportSpec, arg func(any) int) ([]string, error) {
	if len(spec.referenceScopes) == 0 {
		return nil, nil
	}
	clauses := make([]string, 0, len(spec.referenceScopes))
	for _, column := range slices.Sorted(maps.Keys(spec.referenceScopes)) {
		table := spec.referenceScopes[column]
		scope, err := auth.ScopeClauseFor(ctx, table, "ref", arg)
		if err != nil {
			return nil, err
		}
		if scope == "" {
			// An unbounded reader of that table: every row it could name is one
			// they may open, so there is nothing to narrow.
			continue
		}
		clauses = append(clauses, fmt.Sprintf(
			"(%s IS NULL OR EXISTS (SELECT 1 FROM %s ref WHERE ref.id = %s AND %s))",
			column, table, column, scope))
	}
	return clauses, nil
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
		order := strings.Join(positions, ", ")
		if spec.orderBy != "" {
			order = spec.orderBy + ", " + order
		}
		sql += " GROUP BY " + strings.Join(positions, ", ") + " ORDER BY " + order
	}
	sql += fmt.Sprintf(" LIMIT %d", reportRowLimit)
	return sql
}

// scanReportRows shapes each result row into a column→value map, rendering
// values wire-friendly.
func scanReportRows(pgRows pgx.Rows, columns []string) ([]map[string]any, error) {
	// Empty, never nil. "No deals in that stage" is a real answer and arrives
	// shaped like the array it is: nil marshals to `null`, which a model reads as
	// "unknown". Normalized here so no transport can put null on the wire —
	// reportOutcome.Rows is marshalled straight through on the tool surface.
	rows := []map[string]any{}
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

// Tokens a spec expression carries in place of a bind position it cannot
// name: the catalog is a static map of expressions, so a value resolved per
// request — the installation's zone or base currency, the caller's scope over
// a table a subquery reads — is written as a token and bound once the
// statement is assembled. None is valid SQL, so a statement that reaches
// Postgres with one unsubstituted fails loudly rather than quietly reporting
// in the wrong zone, the wrong currency, or past the caller's scope.
const (
	// reportZoneToken stands in for the installation timezone's bind position.
	reportZoneToken = "<<installation-timezone>>"
	// reportBaseCurrencyToken stands in for the installation base currency's
	// bind position.
	reportBaseCurrencyToken = "<<installation-base-currency>>"
	// reportDealScopeToken stands in for the caller's deal row-scope clause
	// over a deal subquery aliased d — what keeps a per-project money total
	// from disclosing a deal the same caller's deal list would withhold.
	reportDealScopeToken = "<<deal-scope:d>>" //nolint:gosec // an SQL placeholder the engine substitutes, not a credential
	// reportActivityScopeToken stands in for the caller's activity content
	// clause over an activity subquery aliased a, for the same reason.
	reportActivityScopeToken = "<<activity-scope:a>>"
)

// bindReportTokens substitutes every token the assembled statement carries
// for a real bind position, appending the resolved values to THIS statement's
// arguments.
//
// It takes and returns the argument slice rather than sharing one, because a
// caller may assemble several statements from one plan and only some of them
// mention a token: Postgres rejects a parameter the statement never
// references, so a shared slice would break the queries that do not use it.
//
// Each value is resolved only when the statement actually asks for it, so a
// report that never buckets by date neither reads the zone setting nor takes
// its gate.
func bindReportTokens(
	ctx context.Context, tx pgx.Tx, sql string, args []any,
) (string, []any, error) {
	args = slices.Clone(args)
	arg := func(v any) int { args = append(args, v); return len(args) }
	bindValue := func(token string, resolve func() (string, error)) error {
		if !strings.Contains(sql, token) {
			return nil
		}
		value, err := resolve()
		if err != nil {
			return err
		}
		sql = strings.ReplaceAll(sql, token, fmt.Sprintf("$%d", arg(value)))
		return nil
	}
	if err := bindValue(reportZoneToken, func() (string, error) { return identity.TimezoneOf(ctx, tx) }); err != nil {
		return "", nil, err
	}
	if err := bindValue(reportBaseCurrencyToken, func() (string, error) { return identity.BaseCurrencyOf(ctx, tx) }); err != nil {
		return "", nil, err
	}
	bindScope := func(token string, resolve func() (string, error)) error {
		if !strings.Contains(sql, token) {
			return nil
		}
		clause, err := resolve()
		if err != nil {
			return err
		}
		if clause == "" {
			// An unbounded reader of that table: nothing to narrow.
			clause = sqlUnnarrowed
		}
		sql = strings.ReplaceAll(sql, token, clause)
		return nil
	}
	if err := bindScope(reportDealScopeToken, func() (string, error) { return auth.ScopeClauseFor(ctx, tableDeal, "d", arg) }); err != nil {
		return "", nil, err
	}
	if err := bindScope(reportActivityScopeToken, func() (string, error) { return auth.ActivityContentClause(ctx, "a", arg) }); err != nil {
		return "", nil, err
	}
	return sql, args, nil
}
