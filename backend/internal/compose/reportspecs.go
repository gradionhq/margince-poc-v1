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
	fieldProject   = "project"
	colKind        = "t.kind"
	colDirection   = "t.direction"

	// activityProjectIDExpr is the project an activity is filed under, read
	// off its activity_link row. It is a scalar because the schema admits at
	// most one project link per activity (uq_activity_link_project), which is
	// what lets ONE expression serve as the dimension, the filter and the
	// drill-through column: `expr = $n` is exactly "EXISTS an activity_link
	// naming that project", and NULL is exactly "filed under none".
	activityProjectIDExpr = "(SELECT al.project_id FROM activity_link al" +
		" WHERE al.activity_id = t.id AND al.entity_type = 'project')"
	// activityProjectLabelExpr is that project as a reader sees it — its key
	// when it has one, else its name — so a breakdown by project needs no
	// second lookup to be readable.
	activityProjectLabelExpr = "(SELECT coalesce(p.key, p.name) FROM activity_link al" +
		" JOIN project p ON p.id = al.project_id" +
		" WHERE al.activity_id = t.id AND al.entity_type = 'project')"
)

// projectFilterScope is the read gate every report filtering by project_id
// runs before the filter binds (reportthreshold.go requireFilterScopes): the
// project grant, then the live row probe.
var projectFilterScope = map[string]string{fieldProjectID: tableProject}

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
			fieldProjectID:  colProjectID,
		},
		filterScopes: projectFilterScope,
		// The company a deal points at is row-scoped and masked on a normal
		// deal read, so grouping by it carries the same obligation the partner
		// dimension does.
		referenceScopes: map[string]string{colOrganizationID: tableOrganization},
		defaultBy:       []string{fieldOrganizationID},
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
			fieldProjectID:      colProjectID,
		},
		filterScopes: projectFilterScope,
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
		dimensions: map[string]string{
			fieldKind:      colKind,
			fieldDirection: colDirection,
			// Grouping by the project an activity is filed under answers
			// which bodies of work consumed the meeting and call effort; an
			// unfiled activity lands in the NULL group, which the wire reads
			// as "no project".
			fieldProjectID: activityProjectIDExpr,
			fieldProject:   activityProjectLabelExpr,
		},
		measures: map[string]string{},
		filters: map[string]string{
			fieldKind:      colKind,
			fieldDirection: colDirection,
			fieldProjectID: activityProjectIDExpr,
		},
		filterScopes: projectFilterScope,
		// The project a filed activity names is row-scoped; grouping by it
		// carries the same obligation the deal reports' company columns do,
		// and naming it at all — id or label — takes the project grant.
		referenceScopes: map[string]string{activityProjectIDExpr: tableProject},
		grants:          map[string]string{fieldProjectID: tableProject, fieldProject: tableProject},
		notes: "project_id admits exactly the activities filed under that project (an activity_link row " +
			"naming it); an activity filed nowhere, or under another project, is excluded",
		defaultBy: []string{fieldKind},
		defaultAggs: []reportAggregate{
			{Fn: aggFnCount, As: "activities"},
		},
	},
	// win-loss (REPORT-KEY-8) is assembled by winLossSpec in reportperiod.go
	// rather than spelled inline: it carries the period-bucket vocabulary with
	// it, and that vocabulary belongs beside the buckets it names.
	"win-loss": winLossSpec(),
	// The project keys (reportprojects.go): what a delivery manager asks of
	// the bodies of work in flight.
	"projects-by-phase":   projectsByPhaseSpec(),
	"project-commitments": projectCommitmentsSpec(),
	"projects-gone-quiet": projectsGoneQuietSpec(),
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
			fieldProjectID:      colProjectID,
		},
		filterScopes: projectFilterScope,
		defaultBy:    moneyDefaultBy("forecast_category"),
		defaultAggs: []reportAggregate{
			{Fn: aggFnCount, As: aliasDeals},
			{Fn: aggFnSum, Field: fieldAmountMinor, As: "unweighted_minor"},
			{Fn: aggFnSum, Field: fieldWeightedAmountMinor, As: "weighted_minor"},
		},
	},
}
