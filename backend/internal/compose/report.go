// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The compiled report engine (interfaces.md §3 RunReport, crm.yaml
// runReport): a validated, typed plan — never free SQL. Field
// vocabulary is closed per report; every identifier that reaches the
// query text comes from these tables, and every value travels as a
// bind parameter. Lives in compose because reports read across the
// domain modules' tables, which is exactly the composition layer's
// charter.

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// reportRowLimit bounds any report; aggregates past this are a data
// export, not a report.
const reportRowLimit = 1000

// Column references reused across the prebuilt report specs. One spelling
// each so a dimension, measure, and filter that mean the same column cannot
// drift apart.
const (
	colOwnerID        = "t.owner_id"
	colAmountMinor    = "t.amount_minor"
	colPipelineID     = "t.pipeline_id"
	colStageID        = "t.stage_id"
	whereArchivedNull = "t.archived_at IS NULL"
)

type reportAggregate struct {
	Fn    string `json:"fn"`
	Field string `json:"field,omitempty"`
	As    string `json:"as,omitempty"`
}

type reportRequest struct {
	Filters    map[string]any    `json:"filters,omitempty"`
	GroupBy    []string          `json:"group_by,omitempty"`
	Aggregates []reportAggregate `json:"aggregates,omitempty"`
}

// reportSpec is one report's closed vocabulary: which entity it reads,
// which dimensions may group, which measures may aggregate, which keys
// may filter — each mapping an API name to a fixed SQL expression.
type reportSpec struct {
	entity datasource.EntityType
	table  string
	// joins widen the FROM side with fixed lookup tables (e.g. the
	// deal's stage for win_probability); the row grain stays the base
	// table's — a spec must never join a to-many side, or aggregates
	// would double-count.
	joins        []string
	baseWhere    string
	basePlain    string // plain-language reading of baseWhere for "Explain This Number"
	activityWalk bool
	dimensions   map[string]string
	measures     map[string]string
	filters      map[string]string
	defaultBy    []string
	defaultAggs  []reportAggregate
}

// forecastCategoryExpr is the forecast's effective-category dimension
// (formulas §11, AC-F9): a claimed commit/best_case deal whose close
// date is past, missing, or still a provisional machine guess is NOT
// counted in those totals — it groups under 'slipped' until a human
// confirms a real date. The exclusion lives in the dimension itself, so
// the aggregate, its filter, and the drill-through all read the same
// row set and keep reconciling exactly (no post-hoc subtraction).
// "Today" buckets in the workspace reporting zone (data-semantics §2 r4)
// via the spec's fixed workspace join.
const forecastCategoryExpr = `(CASE WHEN t.forecast_category IN ('commit','best_case')
		AND (t.expected_close_date IS NULL
			OR t.expected_close_date < (timezone(w.timezone, now()))::date
			OR t.close_date_provisional)
	THEN 'slipped' ELSE t.forecast_category END)`

// prebuiltReports is the report catalog (data-model §13 shape): keys
// are never UUIDs, so saved-report ids cannot collide.
var prebuiltReports = map[string]reportSpec{
	"open-deals-per-company": {
		entity:     datasource.EntityDeal,
		table:      "deal",
		baseWhere:  "t.archived_at IS NULL AND t.status = 'open'",
		basePlain:  "live (unarchived) open deals",
		dimensions: map[string]string{"organization_id": "t.organization_id", "owner_id": colOwnerID},
		measures:   map[string]string{"amount_minor": colAmountMinor},
		filters:    map[string]string{"owner_id": colOwnerID, "pipeline_id": colPipelineID},
		defaultBy:  []string{"organization_id"},
		defaultAggs: []reportAggregate{
			{Fn: "count", As: "open_deals"},
		},
	},
	"deals-by-stage": {
		entity:     datasource.EntityDeal,
		table:      "deal",
		baseWhere:  whereArchivedNull,
		basePlain:  "live (unarchived) deals",
		dimensions: map[string]string{"stage_id": colStageID, "status": "t.status", "pipeline_id": colPipelineID},
		measures:   map[string]string{"amount_minor": colAmountMinor},
		filters:    map[string]string{"pipeline_id": colPipelineID, "status": "t.status", "owner_id": colOwnerID},
		defaultBy:  []string{"stage_id"},
		defaultAggs: []reportAggregate{
			{Fn: "count", As: "deals"},
			{Fn: "sum", Field: "amount_minor", As: "amount_minor_sum"},
		},
	},
	"activities-by-kind": {
		entity:       datasource.EntityActivity,
		table:        "activity",
		baseWhere:    whereArchivedNull,
		basePlain:    "live (unarchived) activities",
		activityWalk: true,
		dimensions:   map[string]string{"kind": "t.kind", "direction": "t.direction"},
		measures:     map[string]string{},
		filters:      map[string]string{"kind": "t.kind", "direction": "t.direction"},
		defaultBy:    []string{"kind"},
		defaultAggs: []reportAggregate{
			{Fn: "count", As: "activities"},
		},
	},
	// The forecast (B-E09.10) is a parameterized report over this same
	// engine, not a separate subsystem. Weighted value follows
	// formulas-and-rules §6: round(amount_minor × stage.win_probability
	// / 100) PER DEAL (half away from zero), so the roll-up total equals
	// the sum of the per-deal weighted values exactly (AC-F1) — the same
	// expression the drill-through rows expose. Stakeholders never join
	// in: the grain is one row per deal, so a multi-stakeholder deal
	// counts once (AC-F2).
	"forecast": {
		entity:    datasource.EntityDeal,
		table:     "deal",
		joins:     []string{"JOIN stage s ON s.id = t.stage_id", "JOIN workspace w ON w.id = t.workspace_id"},
		baseWhere: "t.archived_at IS NULL AND t.status = 'open'",
		basePlain: "open, unarchived deals (win probability read live from the deal's current stage; a commit/best_case deal whose close date is past, missing, or provisional reports as 'slipped' instead, per formulas §11)",
		dimensions: map[string]string{
			"owner_id":          colOwnerID,
			"stage_id":          colStageID,
			"pipeline_id":       colPipelineID,
			"forecast_category": forecastCategoryExpr,
			"currency":          "t.currency",
			"win_probability":   "s.win_probability",
		},
		measures: map[string]string{
			"amount_minor":          colAmountMinor,
			"weighted_amount_minor": "round((t.amount_minor * s.win_probability) / 100.0)::bigint",
		},
		filters: map[string]string{
			"owner_id":          colOwnerID,
			"stage_id":          colStageID,
			"pipeline_id":       colPipelineID,
			"forecast_category": forecastCategoryExpr,
			"currency":          "t.currency",
		},
		defaultBy: []string{"forecast_category"},
		defaultAggs: []reportAggregate{
			{Fn: "count", As: "deals"},
			{Fn: "sum", Field: "amount_minor", As: "unweighted_minor"},
			{Fn: "sum", Field: "weighted_amount_minor", As: "weighted_minor"},
		},
	},
}

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

// FieldNotAllowedError maps to 422 report_field_not_allowed.
type FieldNotAllowedError struct{ Field string }

func (e *FieldNotAllowedError) Error() string {
	return fmt.Sprintf("report: field %q is outside this report's vocabulary", e.Field)
}

// reportOutcome is the executed result plus the validated plan echo.
// Filters/GroupBy/Aggregates carry the EFFECTIVE plan (defaults applied)
// so the transport can mint derivation handles for exactly what ran.
type reportOutcome struct {
	Report      string
	Plan        map[string]any
	Filters     map[string]any
	GroupBy     []string
	Aggregates  []reportAggregate
	Columns     []string
	Rows        []map[string]any
	GeneratedAt time.Time
}

type reportEngine struct {
	pool *pgxpool.Pool
}

func newReportEngine(pool *pgxpool.Pool) *reportEngine {
	return &reportEngine{pool: pool}
}

// uuidShape distinguishes a saved-report id from a prebuilt key (the
// contract's collision rule).
var uuidShape = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func (e *reportEngine) Run(ctx context.Context, report string, req reportRequest) (reportOutcome, error) {
	if uuidShape.MatchString(report) {
		// Saved reports are a later slice; an unknown id is absent, not
		// half-supported.
		return reportOutcome{}, fmt.Errorf("saved report %s: %w", report, apperrors.ErrNotFound)
	}
	spec, ok := prebuiltReports[report]
	if !ok {
		return reportOutcome{}, fmt.Errorf("report %q: %w", report, apperrors.ErrNotFound)
	}
	return e.runSpec(ctx, report, spec, req)
}

// runSpec executes one validated vocabulary; Run (prebuilt catalog) and
// runAdHocPlan (schema-descriptor vocabulary) both land here.
func (e *reportEngine) runSpec(ctx context.Context, report string, spec reportSpec, req reportRequest) (reportOutcome, error) {
	if err := auth.Require(ctx, string(spec.entity), principal.ActionRead); err != nil {
		return reportOutcome{}, err
	}

	groupBy := req.GroupBy
	if len(groupBy) == 0 {
		groupBy = spec.defaultBy
	}
	aggregates := req.Aggregates
	if len(aggregates) == 0 {
		aggregates = spec.defaultAggs
	}

	columns, selects, err := buildSelectList(spec, groupBy, aggregates)
	if err != nil {
		return reportOutcome{}, err
	}

	rows, err := e.fetchRows(ctx, report, spec, req, groupBy, selects, columns)
	if err != nil {
		return reportOutcome{}, err
	}

	return reportOutcome{
		Report: report,
		Plan: map[string]any{
			"object":     string(spec.entity),
			"filters":    req.Filters,
			"group_by":   groupBy,
			"aggregates": aggregates,
		},
		Filters:     req.Filters,
		GroupBy:     groupBy,
		Aggregates:  aggregates,
		Columns:     columns,
		Rows:        rows,
		GeneratedAt: time.Now().UTC(),
	}, nil
}

// buildSelectList validates the requested dimensions and aggregates
// against the spec's closed vocabulary and renders the SELECT list — the
// only path by which a caller-chosen name reaches the query text.
func buildSelectList(spec reportSpec, groupBy []string, aggregates []reportAggregate) (columns, selects []string, err error) {
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
func aggregateSelect(spec reportSpec, agg reportAggregate) (name, sel string, err error) {
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
func (e *reportEngine) fetchRows(ctx context.Context, report string, spec reportSpec, req reportRequest, groupBy, selects, columns []string) ([]map[string]any, error) {
	var rows []map[string]any
	err := database.WithWorkspaceTx(ctx, e.pool, func(tx pgx.Tx) error {
		var args []any
		arg := func(v any) int { args = append(args, v); return len(args) }

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
				return &FieldNotAllowedError{Field: key}
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
			return err
		}
		if scope != "" {
			where = append(where, scope)
		}

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

		pgRows, err := tx.Query(ctx, sql, args...)
		if err != nil {
			return fmt.Errorf("report %s: %w", report, err)
		}
		defer pgRows.Close()
		for pgRows.Next() {
			values, err := pgRows.Values()
			if err != nil {
				return err
			}
			row := make(map[string]any, len(columns))
			for i, col := range columns {
				row[col] = wireValue(values[i])
			}
			rows = append(rows, row)
		}
		return pgRows.Err()
	})
	if err != nil {
		return nil, err
	}
	return rows, nil
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

var errUnknownEntity = errors.New("compose: entity outside the schema descriptors")

// runAdHocPlan serves the datasource seam's RunReport: the plan's
// vocabulary is the schema descriptors (every declared field may group
// or filter; count is the aggregate). Used by overlay tooling and the
// seam conformance tests rather than the HTTP surface.
func (e *reportEngine) runAdHocPlan(ctx context.Context, plan datasource.ReportPlan) (datasource.ReportResult, error) {
	fields, ok := schemaFields(plan.Entity)
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
		defaultAggs:  []reportAggregate{{Fn: "count", As: "count"}},
	}
	for _, f := range fields {
		expr := "t." + f.Name
		spec.dimensions[f.Name] = expr
		spec.filters[f.Name] = expr
		if f.Type == "bigint" || f.Type == "integer" {
			spec.measures[f.Name] = expr
		}
	}
	req := reportRequest{GroupBy: plan.GroupBy, Filters: map[string]any{}}
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
