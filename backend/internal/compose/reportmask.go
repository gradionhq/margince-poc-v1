// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// Field masks meeting the report engine. A mask withholds a column of a row
// (deals' amount for a rep outside the owning team, ADR-level rule shipped
// with the field-mask work); an aggregate over that column would disclose it
// through the total, and a drill-through would print it. The report engine
// therefore EXCLUDES masked rows — from the aggregate and from the
// drill-through alike, so "Explain This Number" keeps reconciling exactly —
// and reports how many visible rows were withheld as excluded_by_permission,
// so a smaller number reads as governed, never as missing data.

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// maskExclusionClauses renders, per masked column of the spec's entity, the
// predicate for the rows the caller may still aggregate — "" clauses (no mask
// for this caller) yield (nil, false). The clauses AND into the query's WHERE;
// their negation is the excluded_by_permission count's filter.
func maskExclusionClauses(ctx context.Context, spec reportSpec, arg func(any) int) ([]string, bool, error) {
	p, ok := principal.Actor(ctx)
	if !ok {
		return nil, false, fmt.Errorf("compose: report mask check without an actor")
	}
	seen := map[string]bool{}
	var clauses []string
	for _, m := range p.Permissions.FieldMasks {
		if m.Object != string(spec.entity) || seen[m.Field] {
			continue
		}
		seen[m.Field] = true
		clause, applies, err := auth.MaskExcludedClause(ctx, string(spec.entity), m.Field, "t", arg)
		if err != nil {
			return nil, false, err
		}
		if !applies || clause == "" {
			continue
		}
		clauses = append(clauses, clause)
	}
	return clauses, len(clauses) > 0, nil
}

// countMaskExcluded answers how many rows of the report's visible base set the
// mask withheld: the same FROM and WHERE minus the mask clauses, counting the
// rows that fail them. Every bind of the main statement is referenced here
// too, so one args slice serves both.
func countMaskExcluded(ctx context.Context, tx pgx.Tx, spec reportSpec, baseWhere []string, maskClauses []string, args []any) (int, error) {
	where := strings.Join(baseWhere, " AND ")
	sql := fmt.Sprintf("SELECT count(*) FILTER (WHERE NOT (%s)) FROM %s WHERE %s",
		strings.Join(maskClauses, " AND "), spec.fromClause(), where)
	sql, args, err := bindReportTokens(ctx, tx, sql, args)
	if err != nil {
		return 0, err
	}
	var excluded int64
	if err := tx.QueryRow(ctx, sql, args...).Scan(&excluded); err != nil {
		return 0, fmt.Errorf("report mask exclusion count: %w", err)
	}
	return int(excluded), nil
}
