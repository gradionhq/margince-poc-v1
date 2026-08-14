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
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// Column references reused across the prebuilt report specs. One spelling
// each so a dimension, measure, and filter that mean the same column cannot
// drift apart.
const (
	colOwnerID        = "t.owner_id"
	colAmountMinor    = "t.amount_minor"
	colPipelineID     = "t.pipeline_id"
	colStageID        = "t.stage_id"
	colOrganizationID = "t.organization_id"
	colCurrency       = "t.currency"
	colStatus         = "t.status"
	whereArchivedNull = "t.archived_at IS NULL"

	// joinStageForWinProbability is the one join a spec adds when it needs the
	// deal's current stage for win_probability. It is safe from BOTH directions
	// a join can go wrong: it cannot MULTIPLY rows (a to-one lookup — deal.stage_id
	// is NOT NULL, stage.id is its PK) and it cannot DROP one under the workspace
	// GUC either — every deal's stage_id is validated against the SAME workspace
	// at write time (modules/deals writes inside the RLS tx), so a deal's stage row
	// is never invisible to the deal's own scope.
	joinStageForWinProbability = "JOIN stage s ON s.id = t.stage_id"
	colWinProbability          = "s.win_probability"

	// weightedAmountMinorExpr is the one spelling of "weighted value" (formulas
	// §6, AC-F1): round PER DEAL, half away from zero, so a roll-up over it
	// equals the sum of its own rows exactly. Shared by every spec that joins
	// stage for win_probability, so the forecast report and any other
	// per-stage weighted figure cannot drift apart. The multiply casts to
	// numeric first — amount_minor is an unbounded bigint, and bigint × smallint
	// overflows before the /100.0 below would otherwise widen it.
	weightedAmountMinorExpr = "round((t.amount_minor::numeric * s.win_probability) / 100.0)::bigint"
	// fieldWeightedAmountMinor is the API-facing measure name every spec that
	// defines weightedAmountMinorExpr registers it under.
	fieldWeightedAmountMinor = "weighted_amount_minor"

	// fieldStageID, fieldStatus and fieldWinProbability are report-vocabulary
	// field NAMES (map keys) — distinct from the col* constants above, which
	// are the SQL expressions those names resolve to. Declared here, not
	// borrowed from an unrelated surface's vocabulary (overlay's query-param
	// names happen to share these spellings, but renaming one must never
	// rename the other).
	fieldStageID        = "stage_id"
	fieldStatus         = "status"
	fieldWinProbability = "win_probability"
	fieldOrganizationID = "organization_id"
	fieldPartnerSourced = "partner_sourced"
	fieldStalled        = "stalled"
	fieldCurrency       = "currency"
	fieldPipelineID     = "pipeline_id"
	fieldOwnerID        = "owner_id"
	fieldAmountMinor    = "amount_minor"

	// The aggregate-function vocabulary aggregateSelect switches on. Named for
	// the same reason as the field names above: it is a CLOSED set that several
	// specs spell, and a set discoverable only by reading a switch is one a
	// second spelling can drift away from unnoticed.
	aggFnCount = "count"
	aggFnSum   = "sum"
	aggFnAvg   = "avg"
	aggFnMin   = "min"
	aggFnMax   = "max"

	// aliasDeals is the output column the three deal-side specs count into by
	// DEFAULT. An alias is otherwise the caller's own free-form name; this one
	// is shared because those three default plans answer the same question, and
	// a reader comparing two reports should not have to notice a spelling
	// difference that means nothing.
	aliasDeals = "deals"
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
// "Today" buckets in the installation's reporting zone (data-semantics §2 r4).
//
// The zone arrives as a BIND parameter, written here as reportZoneToken and
// substituted for a real $n once the statement is assembled — the catalog is a
// static map of expressions, so it has no bind position to name at the point
// it is written. Postgres still does the date arithmetic, which is what keeps
// the DST rules and the day boundary where they were when the zone was a
// column on a joined row.
const forecastCategoryExpr = `(CASE WHEN t.forecast_category IN ('commit','best_case')
		AND (t.expected_close_date IS NULL
			OR t.expected_close_date < (timezone(` + reportZoneToken + `, now()))::date
			OR t.close_date_provisional)
	THEN 'slipped' ELSE t.forecast_category END)`

// reportZoneToken stands in for the installation timezone's bind position
// until fetchRows knows it. It is deliberately not valid SQL, so a statement
// that reaches Postgres with the token unsubstituted fails loudly rather than
// quietly reporting in the wrong zone.
const reportZoneToken = "<<installation-timezone>>"

// prebuiltReports is the report catalog (data-model §13 shape): keys
// are never UUIDs, so saved-report ids cannot collide.
var prebuiltReports = map[string]reportSpec{
	"open-deals-per-company": {
		entity:     datasource.EntityDeal,
		table:      tableDeal,
		baseWhere:  "t.archived_at IS NULL AND t.status = 'open'",
		basePlain:  "live (unarchived) open deals",
		dimensions: map[string]string{fieldOrganizationID: colOrganizationID, fieldOwnerID: colOwnerID},
		measures:   map[string]string{fieldAmountMinor: colAmountMinor},
		filters:    map[string]string{fieldOwnerID: colOwnerID, fieldPipelineID: colPipelineID},
		defaultBy:  []string{fieldOrganizationID},
		defaultAggs: []reportAggregate{
			{Fn: aggFnCount, As: "open_deals"},
		},
	},
	"deals-by-stage": {
		entity:    datasource.EntityDeal,
		table:     tableDeal,
		joins:     []string{joinStageForWinProbability},
		baseWhere: whereArchivedNull,
		basePlain: "live (unarchived) deals",
		dimensions: map[string]string{
			fieldStageID:        colStageID,
			fieldStatus:         colStatus,
			fieldPipelineID:     colPipelineID,
			fieldWinProbability: colWinProbability,
			fieldCurrency:       colCurrency,
		},
		measures: map[string]string{
			fieldAmountMinor:         colAmountMinor,
			fieldWeightedAmountMinor: weightedAmountMinorExpr,
		},
		// No stage_id filter: nothing serves it (the screen groups BY stage_id
		// instead), and a filter key this report has no caller for is public
		// agent surface (the run_report catalog, mcp-info.{json,md}) with no
		// concrete use behind it. The rest match the board's own filter dials:
		// partner_sourced and stalled are boolean-valued expressions, which
		// the engine's generic `expr = $n` rendering already handles with no
		// special-casing.
		filters: map[string]string{
			fieldPipelineID:     colPipelineID,
			fieldStatus:         colStatus,
			fieldOwnerID:        colOwnerID,
			fieldOrganizationID: colOrganizationID,
			fieldPartnerSourced: deals.PartnerSourcedSQL("t"),
			fieldStalled:        deals.StalledSQL("t"),
			fieldCurrency:       colCurrency,
		},
		defaultBy: []string{fieldStageID},
		defaultAggs: []reportAggregate{
			{Fn: aggFnCount, As: aliasDeals},
			{Fn: aggFnSum, Field: fieldAmountMinor, As: "amount_minor_sum"},
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
			{Fn: aggFnCount, As: "activities"},
		},
	},
	// win-loss (REPORT-KEY-8) is assembled by winLossSpec in reportperiod.go
	// rather than spelled inline: it carries the period-bucket vocabulary with
	// it, and this file is at the package's file-length cap.
	"win-loss": winLossSpec(),
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
		table:     tableDeal,
		joins:     []string{joinStageForWinProbability},
		baseWhere: "t.archived_at IS NULL AND t.status = 'open'",
		basePlain: "open, unarchived deals (win probability read live from the deal's current stage; a commit/best_case deal whose close date is past, missing, or provisional reports as 'slipped' instead, per formulas §11)",
		dimensions: map[string]string{
			fieldOwnerID:        colOwnerID,
			fieldStageID:        colStageID,
			fieldPipelineID:     colPipelineID,
			"forecast_category": forecastCategoryExpr,
			fieldCurrency:       colCurrency,
			fieldWinProbability: colWinProbability,
		},
		measures: map[string]string{
			fieldAmountMinor:         colAmountMinor,
			fieldWeightedAmountMinor: weightedAmountMinorExpr,
		},
		filters: map[string]string{
			fieldOwnerID:        colOwnerID,
			fieldStageID:        colStageID,
			fieldPipelineID:     colPipelineID,
			"forecast_category": forecastCategoryExpr,
			fieldCurrency:       colCurrency,
		},
		defaultBy: []string{"forecast_category"},
		defaultAggs: []reportAggregate{
			{Fn: aggFnCount, As: aliasDeals},
			{Fn: aggFnSum, Field: fieldAmountMinor, As: "unweighted_minor"},
			{Fn: aggFnSum, Field: fieldWeightedAmountMinor, As: "weighted_minor"},
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
			return nil, nil, &FieldNotAllowedError{Field: dim, Slot: slotGroupBy, Allowed: allowedReportNames(spec.dimensions)}
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
		// Its own refusal: nothing here is out of vocabulary, so the vocabulary
		// error would name a field the caller never wrote.
		return nil, nil, &EmptyReportPlanError{}
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
		//
		// No vocabulary rides here, and that is the point: an alias is the
		// caller's own free-form name — every other one is accepted — so
		// listing the report's MEASURES would answer a question nobody asked
		// and tell them the one thing that is not true, that aliases come from
		// a fixed set. The refusal is about this ONE reserved name.
		return "", "", &ReservedAliasError{Alias: name}
	}
	switch agg.Fn {
	case aggFnCount:
		return name, fmt.Sprintf("count(*) AS %s", quoteIdent(name)), nil
	case aggFnSum, aggFnAvg, aggFnMin, aggFnMax:
		expr, ok := spec.measures[agg.Field]
		if !ok {
			return "", "", &FieldNotAllowedError{Field: agg.Field, Slot: slotAggregates, Allowed: allowedReportNames(spec.measures)}
		}
		return name, fmt.Sprintf("%s(%s) AS %s", agg.Fn, expr, quoteIdent(name)), nil
	default:
		return "", "", &FieldNotAllowedError{Field: "fn=" + agg.Fn}
	}
}

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
		defaultAggs:  []reportAggregate{{Fn: aggFnCount, As: aggFnCount}},
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
