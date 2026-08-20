// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// The activity report's own vocabulary. Spelled here rather than borrowed from
// an unrelated surface that happens to use the same words: renaming a report
// dimension must never rename a capture target type.
const (
	fieldKind      = "kind"
	fieldDirection = "direction"
	colKind        = "t.kind"
	colDirection   = "t.direction"
)

// The prebuilt report catalog: WHAT each report asks, as data.
//
// Split from report.go, which holds the machinery that RUNS a spec — the
// validation, the SQL assembly, the row mapping. The two change for unrelated
// reasons: a new report is a new entry here and nothing else, while a change to
// how a report is executed touches none of these entries. Keeping them in one
// file put the catalog over the 500-line cap the moment a report grew a
// dimension.

// prebuiltReports is the report catalog (data-model §13 shape): keys
// are never UUIDs, so saved-report ids cannot collide.
var prebuiltReports = map[string]reportSpec{
	"open-deals-per-company": {
		entity:    datasource.EntityDeal,
		table:     tableDeal,
		baseWhere: "t.archived_at IS NULL AND t.status = 'open'",
		basePlain: "live (unarchived) open deals",
		// Currency is a dimension AND a filter because amount_minor is a
		// measure here: a caller summing money on this key has to be able to
		// split it by currency. It stays OUT of defaultBy, unlike the two
		// reports below — this key's default plan counts deals and sums
		// nothing, so a currency split would multiply its rows while reporting
		// no more than it does now.
		dimensions: map[string]string{
			fieldOrganizationID: colOrganizationID,
			fieldOwnerID:        colOwnerID,
			fieldCurrency:       colCurrency,
		},
		measures: map[string]string{fieldAmountMinor: colAmountMinor},
		filters: map[string]string{
			fieldOwnerID:    colOwnerID,
			fieldPipelineID: colPipelineID,
			fieldCurrency:   colCurrency,
		},
		defaultBy: []string{fieldOrganizationID},
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
			// Grouping BY the partner is what turns "is this deal
			// partner-sourced" into "what did this partner bring us" — the
			// question the partner program is run on, and the one this report
			// could not answer while partner_sourced was a filter alone.
			fieldPartnerOrgID: colPartnerOrgID,
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
		// partner_org_id points at an organization, which a normal deal read
		// masks per row when the caller cannot open it.
		referenceScopes: map[string]string{colPartnerOrgID: tableOrganization},
		defaultBy:       moneyDefaultBy(fieldStageID),
		defaultAggs: []reportAggregate{
			{Fn: aggFnCount, As: aliasDeals},
			{Fn: aggFnSum, Field: fieldAmountMinor, As: "amount_minor_sum"},
		},
	},
	"activities-by-kind": {
		entity:       datasource.EntityActivity,
		table:        tableActivity,
		baseWhere:    whereArchivedNull,
		basePlain:    "live (unarchived) activities",
		activityWalk: true,
		dimensions:   map[string]string{fieldKind: colKind, fieldDirection: colDirection},
		measures:     map[string]string{},
		filters:      map[string]string{fieldKind: colKind, fieldDirection: colDirection},
		defaultBy:    []string{fieldKind},
		defaultAggs: []reportAggregate{
			{Fn: aggFnCount, As: "activities"},
		},
	},
	// win-loss (REPORT-KEY-8) is assembled by winLossSpec in reportperiod.go
	// rather than spelled inline: it carries the period-bucket vocabulary with
	// it, and that vocabulary belongs beside the buckets it names.
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
		defaultBy: moneyDefaultBy("forecast_category"),
		defaultAggs: []reportAggregate{
			{Fn: aggFnCount, As: aliasDeals},
			{Fn: aggFnSum, Field: fieldAmountMinor, As: "unweighted_minor"},
			{Fn: aggFnSum, Field: fieldWeightedAmountMinor, As: "weighted_minor"},
		},
	},
}
