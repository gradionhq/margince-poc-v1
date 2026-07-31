// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package auth

// The activity_link visibility rules. An activity has no owner of its own —
// its free-text inherits the sensitivity of the records it attaches to — so
// two different questions are answered from the same disjunction, and both
// live here so they cannot drift apart (ADR-0054 §8: scope policy has
// exactly one spelling).
//
//   - MAY I READ THIS ACTIVITY: yes if ANY of its links points at a record
//     I can see (ActivityScopeClause, rbac.go).
//   - MAY I BE TOLD WHAT IT IS ABOUT: per link, because the any-link answer
//     above does not license disclosing the other records it touches.

import (
	"context"
	"fmt"
	"strings"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// LinkTargetVisibleClause answers, for ONE activity_link row, whether the
// record it points at is visible under the caller's row scope. An empty
// string means a caller for whom every target is visible — which, since
// person and organization carry capture privacy, is the system principal
// alone.
//
// It exists because "may I read this activity" and "may I be told what this
// activity is about" are different questions. The activity gate above is an
// ANY-link rule: an activity reachable through one visible person is
// readable in full. Projecting its link rows back to the client would then
// hand over the ids of the OTHER records it touches — a colleague's deal,
// say — which the caller may not read. So the projection carries its own
// per-row predicate, built from the same disjunction the gate uses, because
// scope policy has exactly one spelling (ADR-0054 §8).
//
// alias names the activity_link table in the caller's query.
func LinkTargetVisibleClause(ctx context.Context, alias string, arg func(any) int) (string, error) {
	p, err := rbacActor(ctx)
	if err != nil {
		return "", err
	}
	if UnboundedFor(p, linkTargetTables...) {
		return "", nil
	}
	return linkTargetVisible(p, alias, arg), nil
}

// The row-scoped record types, named once. Several of these appear in
// half a dozen table-name positions across the package, and a typo in any
// of them silently renders a predicate that matches nothing.
const (
	tablePerson       = "person"
	tableOrganization = "organization"
	tableDeal         = "deal"
	tableLead         = "lead"
	tableProject      = "project"
)

// linkTargetTables names every record type an activity_link points at, in
// the order the disjunction below walks them. Both this projection and the
// activity gate in rbac.go decide whether they may skip their clause by
// asking UnboundedFor over this set, so a record type that gains capture
// privacy tightens both at once.
var linkTargetTables = []string{tablePerson, tableOrganization, tableDeal, tableLead, tableProject}

// linkTargetVisible renders the per-arm "this link's target is visible"
// disjunction over activity_link's polymorphic columns.
func linkTargetVisible(p principal.Principal, alias string, arg func(any) int) string {
	arms := make([]string, 0, len(linkTargetTables))
	for _, t := range []struct{ column, table, probe string }{
		{"person_id", tablePerson, "sp"},
		{"organization_id", tableOrganization, "so"},
		{"deal_id", tableDeal, "sd"},
		{"lead_id", tableLead, "sl"},
		{"project_id", tableProject, "spr"},
	} {
		arms = append(arms, linkTargetArm(alias, t.column, t.table, t.probe,
			VisiblePredicate(p, t.table, arg)(t.probe)))
	}
	return "(\n\t      " + strings.Join(arms, "\n\t   OR ") + ")"
}

// linkTargetArm renders one arm of that disjunction. A predicate of TRUE —
// the caller reads every row of that table — collapses the arm to the
// column test alone: the composite FK already guarantees the target row
// exists, so the EXISTS could only confirm what TRUE already said.
//
// Dropping it is not a micro-optimization. The walk runs per candidate
// row, and the search branch runs it across a whole FTS result set: an
// all-scope reader was paying five correlated joins to be told yes five
// times, which measured as a 6x regression on the search_fts perf budget
// the moment capture privacy stopped exempting them from the walk.
func linkTargetArm(alias, column, table, probe, predicate string) string {
	if predicate == "TRUE" {
		return fmt.Sprintf(`%s.%s IS NOT NULL`, alias, column)
	}
	return fmt.Sprintf(`(%[1]s.%[2]s IS NOT NULL AND EXISTS (SELECT 1 FROM %[3]s %[4]s WHERE %[4]s.id = %[1]s.%[2]s AND %[5]s))`,
		alias, column, table, probe, predicate)
}
