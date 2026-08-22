// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The derivation's EXECUTION half: the drill-through rows, the aggregate
// recompute, and the shared WHERE side that keeps them reading the identical
// row set. The compile half (plan validation, definition rendering, the
// handle round-trip) stays in derivation.go.

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
)

// fetchDerivation executes a compiled plan: the drill-through rows and
// the aggregate recompute run over the identical WHERE side (validated
// predicates + the caller's row-scope clause) in one transaction.
func (e *reportEngine) fetchDerivation(ctx context.Context, report string, spec reportSpec, plan derivationPlan, out *derivationOutcome) error {
	return database.WithWorkspaceTx(ctx, e.pool, func(tx pgx.Tx) error {
		if err := requireFilterScopes(ctx, tx, spec, predicatesAsFilters(plan.predicates)); err != nil {
			return err
		}
		var args []any
		arg := func(v any) int { args = append(args, v); return len(args) }

		where, err := derivationWhere(ctx, spec, plan, arg)
		if err != nil {
			return err
		}
		// The identical mask exclusion the report applied (reportmask.go):
		// the explanation must not out-see the number it explains, and the
		// withheld count rides the envelope the same way.
		maskClauses, masked, err := maskExclusionClauses(ctx, spec, arg)
		if err != nil {
			return err
		}
		if masked {
			n, err := countMaskExcluded(ctx, tx, spec, where, maskClauses, args)
			if err != nil {
				return err
			}
			out.ExcludedByPermission = &n
			where = append(where, maskClauses...)
		}
		whereSQL := strings.Join(where, " AND ")

		rowsSQL, rowsArgs, err := bindReportTokens(ctx, tx, fmt.Sprintf(
			"SELECT %s FROM %s WHERE %s ORDER BY t.id LIMIT %d",
			strings.Join(plan.selects, ", "), spec.fromClause(), whereSQL, reportRowLimit), args)
		if err != nil {
			return err
		}
		pgRows, err := tx.Query(ctx, rowsSQL, rowsArgs...)
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
		aggSQL, aggArgs, err := bindReportTokens(ctx, tx, fmt.Sprintf(
			"SELECT count(*), %s FROM %s WHERE %s",
			strings.Join(plan.aggSelects, ", "), spec.fromClause(), whereSQL), args)
		if err != nil {
			return err
		}
		values := make([]any, len(plan.aggColumns)+1)
		ptrs := make([]any, len(values))
		for i := range values {
			ptrs[i] = &values[i]
		}
		if err := tx.QueryRow(ctx, aggSQL, aggArgs...).Scan(ptrs...); err != nil {
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
		switch {
		case p.threshold != nil:
			n, err := thresholdValue(p.field, p.value)
			if err != nil {
				return nil, err
			}
			where = append(where, p.threshold.clause(arg(n)))
		case p.isNull:
			where = append(where, p.expr+" IS NULL")
		default:
			where = append(where, fmt.Sprintf("%s = $%d", p.expr, arg(p.value)))
		}
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
	// The drill-through puts every dimension on its rows, so every reference
	// the spec carries takes its row scope here too — the explanation must
	// not out-see the number it explains.
	refs, err := referenceScopeClauses(ctx, spec, arg)
	if err != nil {
		return nil, err
	}
	return append(where, refs...), nil
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
