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

	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// LinkTargetVisibleClause answers, for ONE activity_link row, whether the
// record it points at is visible under the caller's row scope. An empty
// string means an unbounded caller, for whom every target is visible.
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
	if Unbounded(p) {
		return "", nil
	}
	return linkTargetVisible(p, alias, arg), nil
}

// linkTargetVisible renders the per-arm "this link's target is visible"
// disjunction over activity_link's polymorphic columns.
func linkTargetVisible(p principal.Principal, alias string, arg func(any) int) string {
	person := VisiblePredicate(p, "person", arg)
	organization := VisiblePredicate(p, "organization", arg)
	deal := VisiblePredicate(p, "deal", arg)
	lead := VisiblePredicate(p, "lead", arg)
	project := VisiblePredicate(p, "project", arg)
	return fmt.Sprintf(`(
	      (%[1]s.person_id IS NOT NULL AND EXISTS (SELECT 1 FROM person sp WHERE sp.id = %[1]s.person_id AND %[2]s))
	   OR (%[1]s.organization_id IS NOT NULL AND EXISTS (SELECT 1 FROM organization so WHERE so.id = %[1]s.organization_id AND %[3]s))
	   OR (%[1]s.deal_id IS NOT NULL AND EXISTS (SELECT 1 FROM deal sd WHERE sd.id = %[1]s.deal_id AND %[4]s))
	   OR (%[1]s.lead_id IS NOT NULL AND EXISTS (SELECT 1 FROM lead sl WHERE sl.id = %[1]s.lead_id AND %[5]s))
	   OR (%[1]s.project_id IS NOT NULL AND EXISTS (SELECT 1 FROM project spr WHERE spr.id = %[1]s.project_id AND %[6]s)))`,
		alias, person("sp"), organization("so"), deal("sd"), lead("sl"), project("spr"))
}
