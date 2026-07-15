// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package reporting

// The compiled report engine (interfaces.md §3 RunReport, crm.yaml
// runReport): a validated, typed plan — never free SQL. Field
// vocabulary is closed per report; every identifier that reaches the
// query text comes from these tables, and every value travels as a
// bind parameter. Reports read across the domain modules' tables, so
// the composition layer injects the schema-descriptor lookup and binds
// this engine into the one datasource seam and the HTTP surface.

import (
	"context"
	"fmt"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
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

type Aggregate struct {
	Fn    string `json:"fn"`
	Field string `json:"field,omitempty"`
	As    string `json:"as,omitempty"`
}

type Request struct {
	Filters    map[string]any `json:"filters,omitempty"`
	GroupBy    []string       `json:"group_by,omitempty"`
	Aggregates []Aggregate    `json:"aggregates,omitempty"`
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
	defaultAggs  []Aggregate
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
		defaultAggs: []Aggregate{
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
		defaultAggs: []Aggregate{
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
		defaultAggs: []Aggregate{
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
		defaultAggs: []Aggregate{
			{Fn: "count", As: "deals"},
			{Fn: "sum", Field: "amount_minor", As: "unweighted_minor"},
			{Fn: "sum", Field: "weighted_amount_minor", As: "weighted_minor"},
		},
	},
}

// FieldNotAllowedError maps to 422 report_field_not_allowed.
type FieldNotAllowedError struct{ Field string }

func (e *FieldNotAllowedError) Error() string {
	return fmt.Sprintf("report: field %q is outside this report's vocabulary", e.Field)
}

// Outcome is the executed result plus the validated plan echo.
// Filters/GroupBy/Aggregates carry the EFFECTIVE plan (defaults applied)
// so the transport can mint derivation handles for exactly what ran.
type Outcome struct {
	Report      string
	Plan        map[string]any
	Filters     map[string]any
	GroupBy     []string
	Aggregates  []Aggregate
	Columns     []string
	Rows        []map[string]any
	GeneratedAt time.Time
}

type Engine struct {
	pool         *pgxpool.Pool
	schemaFields func(datasource.EntityType) ([]datasource.FieldDef, bool)
}

// New builds the engine over the connection pool, taking the
// schema-descriptor lookup by injection so the composition layer keeps
// ownership of the descriptor set (no import cycle back into compose).
func New(pool *pgxpool.Pool, schemaFields func(datasource.EntityType) ([]datasource.FieldDef, bool)) *Engine {
	return &Engine{pool: pool, schemaFields: schemaFields}
}

// uuidShape distinguishes a saved-report id from a prebuilt key (the
// contract's collision rule).
var uuidShape = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func (e *Engine) Run(ctx context.Context, report string, req Request) (Outcome, error) {
	if uuidShape.MatchString(report) {
		// Saved reports are a later slice; an unknown id is absent, not
		// half-supported.
		return Outcome{}, fmt.Errorf("saved report %s: %w", report, apperrors.ErrNotFound)
	}
	spec, ok := prebuiltReports[report]
	if !ok {
		return Outcome{}, fmt.Errorf("report %q: %w", report, apperrors.ErrNotFound)
	}
	return e.runSpec(ctx, report, spec, req)
}

// runSpec executes one validated vocabulary; Run (prebuilt catalog) and
// RunAdHocPlan (schema-descriptor vocabulary) both land here.
func (e *Engine) runSpec(ctx context.Context, report string, spec reportSpec, req Request) (Outcome, error) {
	if err := auth.Require(ctx, string(spec.entity), principal.ActionRead); err != nil {
		return Outcome{}, err
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
		return Outcome{}, err
	}

	rows, err := e.fetchRows(ctx, report, spec, req, groupBy, selects, columns)
	if err != nil {
		return Outcome{}, err
	}

	return Outcome{
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
