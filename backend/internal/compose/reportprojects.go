// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The project keys of the prebuilt catalog: what a delivery manager asks of
// the bodies of work in flight. Kept apart from reportspecs.go because they
// share a vocabulary of their own — the project row's columns, the money and
// the commitments its deals and tasks fold to — and that file sits near its
// length cap.

import (
	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

const (
	// The project measures, as a caller names them.
	measureOpenDealValue   = "open_deal_value_minor"
	measureWonDealValue    = "won_deal_value_minor"
	measureOpenCommitments = "open_commitments"
	measureOverdue         = "overdue_commitments"

	fieldPhase          = "phase"
	fieldName           = "name"
	fieldKey            = "key"
	fieldLastActivityAt = "last_activity_at"
	fieldQuietSince     = "quiet_since"
	fieldDays           = "days"

	colProjectRowID = "t.id"
	colName         = "t.name"
	colKey          = "t.key"
	colPhase        = "t.phase"
	colLastActivity = "t.last_activity_at"

	// The money a project's WON deals fold to, in the installation's base
	// currency (the frozen base amount), read through the caller's deal row
	// scope: a per-project total that counted a deal the caller's deal list
	// would withhold discloses that deal through arithmetic (the rule
	// ProjectDealTotalsTx keeps).
	wonDealValueBaseExpr = "(SELECT coalesce(sum(d.amount_minor_base), 0)::bigint FROM deal d" +
		" WHERE d.project_id = t.id AND d.status = 'won' AND d.archived_at IS NULL AND " + reportDealScopeToken + ")"

	// A project's commitments are the open tasks filed under it. Overdue is
	// a due date already past; a task with no due date is open but never
	// overdue. Both read through the caller's activity content clause.
	openCommitmentsExpr = "(SELECT count(*) FROM activity a JOIN activity_link al ON al.activity_id = a.id" +
		" AND al.entity_type = 'project' AND al.project_id = t.id" +
		" WHERE a.kind = 'task' AND NOT a.is_done AND a.archived_at IS NULL AND " + reportActivityScopeToken + ")"
	overdueCommitmentsExpr = "(SELECT count(*) FROM activity a JOIN activity_link al ON al.activity_id = a.id" +
		" AND al.entity_type = 'project' AND al.project_id = t.id" +
		" WHERE a.kind = 'task' AND NOT a.is_done AND a.archived_at IS NULL AND a.due_at < now() AND " +
		reportActivityScopeToken + ")"
)

// openDealValueBaseExpr is wonDealValueBaseExpr's open-side twin: each open
// deal folded by deals.OpenDealBaseValueSQL over the token the engine binds
// the installation's base currency to. A variable because the fold is built
// by a function the deals module owns.
var openDealValueBaseExpr = "(SELECT coalesce(sum(" + deals.OpenDealBaseValueSQL("d", reportBaseCurrencyToken) +
	"), 0)::bigint FROM deal d WHERE d.project_id = t.id AND d.status = 'open' AND d.archived_at IS NULL AND " +
	reportDealScopeToken + ")"

// projectRowDimensions is the vocabulary the two listing-shaped project keys
// share: grouping by the row's own id makes each row one project, with the
// columns a reader needs to act on it.
func projectRowDimensions() map[string]string {
	return map[string]string{
		fieldProjectID:      colProjectRowID,
		fieldName:           colName,
		fieldKey:            colKey,
		fieldPhase:          colPhase,
		fieldOwnerID:        colOwnerID,
		fieldOrganizationID: colOrganizationID,
	}
}

// projectsByPhaseSpec counts the projects in each phase and folds the money
// their deals are worth. The project's company is row-scoped and masked on a
// normal read, so grouping by it carries the same obligation the deal reports'
// company columns do.
func projectsByPhaseSpec() reportSpec {
	return reportSpec{
		entity:    datasource.EntityProject,
		table:     tableProject,
		baseWhere: whereArchivedNull,
		basePlain: "live (unarchived) projects, with each project's open and won deal value in the installation's base currency",
		dimensions: map[string]string{
			fieldPhase:          colPhase,
			fieldOrganizationID: colOrganizationID,
			fieldOwnerID:        colOwnerID,
		},
		measures: map[string]string{
			measureOpenDealValue: openDealValueBaseExpr,
			measureWonDealValue:  wonDealValueBaseExpr,
		},
		filters: map[string]string{
			fieldOrganizationID: colOrganizationID,
			fieldOwnerID:        colOwnerID,
			fieldPhase:          colPhase,
		},
		referenceScopes: map[string]string{colOrganizationID: tableOrganization},
		// The money measures fold DEALS, which the project grant says nothing
		// about: the deal grant is owed before either is served.
		grants:    map[string]string{measureOpenDealValue: tableDeal, measureWonDealValue: tableDeal},
		defaultBy: []string{fieldPhase},
		defaultAggs: []reportAggregate{
			{Fn: aggFnCount, As: "projects"},
			{Fn: aggFnSum, Field: measureOpenDealValue, As: measureOpenDealValue},
			{Fn: aggFnSum, Field: measureWonDealValue, As: measureWonDealValue},
		},
		notes: "deal values are in the installation's base currency; an open deal in another currency counts " +
			"nothing until it closes",
	}
}

// projectCommitmentsSpec lists each live project with its open and overdue
// commitments, the most overdue first — the morning read of what was promised
// on which body of work.
func projectCommitmentsSpec() reportSpec {
	return reportSpec{
		entity:     datasource.EntityProject,
		table:      tableProject,
		baseWhere:  whereArchivedNull,
		basePlain:  "live (unarchived) projects, each with the open tasks filed under it (overdue: due date already past)",
		dimensions: projectRowDimensions(),
		measures: map[string]string{
			measureOpenCommitments: openCommitmentsExpr,
			measureOverdue:         overdueCommitmentsExpr,
		},
		filters: map[string]string{
			fieldOrganizationID: colOrganizationID,
			fieldOwnerID:        colOwnerID,
			fieldPhase:          colPhase,
		},
		referenceScopes: map[string]string{colOrganizationID: tableOrganization},
		// The commitment counts read TASKS, which take the activity grant.
		grants:    map[string]string{measureOpenCommitments: tableActivity, measureOverdue: tableActivity},
		defaultBy: []string{fieldProjectID, fieldName, fieldKey, fieldPhase, fieldOwnerID},
		defaultAggs: []reportAggregate{
			{Fn: aggFnSum, Field: measureOverdue, As: measureOverdue},
			{Fn: aggFnSum, Field: measureOpenCommitments, As: measureOpenCommitments},
		},
		orderBy: "sum(" + overdueCommitmentsExpr + ") DESC, sum(" + openCommitmentsExpr + ") DESC",
		notes:   "rows are ordered most overdue first",
	}
}

// projectsGoneQuietSpec lists the projects in flight that nothing has been
// filed against for `days` days (default deals.DefaultProjectQuietDays). The
// project_gone_quiet signal is raised off the SAME predicate
// (deals.ProjectQuietSQL), so the report and the signal never disagree about
// which projects are quiet.
func projectsGoneQuietSpec() reportSpec {
	dimensions := projectRowDimensions()
	dimensions[fieldLastActivityAt] = colLastActivity
	dimensions[fieldQuietSince] = deals.ProjectQuietAnchorSQL("t")
	return reportSpec{
		entity:     datasource.EntityProject,
		table:      tableProject,
		baseWhere:  whereArchivedNull + " AND " + deals.ProjectInFlightSQL("t"),
		basePlain:  "live projects being pursued or delivered that nothing has been filed against for at least `days` days (a project with no activity at all is measured from its creation)",
		dimensions: dimensions,
		measures:   map[string]string{},
		filters: map[string]string{
			fieldOrganizationID: colOrganizationID,
			fieldOwnerID:        colOwnerID,
			fieldPhase:          colPhase,
		},
		thresholds: map[string]reportThreshold{
			fieldDays: {
				clause:       func(pos int) string { return deals.ProjectQuietSQL("t", "now()", pos) },
				defaultValue: deals.DefaultProjectQuietDays,
			},
		},
		referenceScopes: map[string]string{colOrganizationID: tableOrganization},
		defaultBy:       []string{fieldProjectID, fieldName, fieldKey, fieldPhase, fieldOwnerID, fieldLastActivityAt, fieldQuietSince},
		defaultAggs: []reportAggregate{
			{Fn: aggFnCount, As: "projects"},
		},
		orderBy: "min(" + deals.ProjectQuietAnchorSQL("t") + ")",
		notes:   "`days` is a whole number of days of silence, default 30; quiet_since is when the silence began",
	}
}
