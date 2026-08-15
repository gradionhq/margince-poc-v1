// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package contracts

// Contract row visibility, which is INHERITED rather than owned.
//
// Every other row-scoped record in this system answers `owner_id`, and the
// shared row-scope helpers interpolate exactly those tables — they reject a
// table name they do not know rather than guessing, so `contract` cannot join
// them: there is no column for their clauses to test.
//
// A contract belongs to a company, not a person (ADR-0109 §8). It is visible
// when the deal it came from is visible, and — for the contracts that never ran
// through a pipeline — when its organization is visible. Deriving it rather
// than copying an owner at creation is what makes a deal reassignment carry its
// contracts in the same query, instead of leaving the agreement behind with the
// representative who no longer works the account.

import (
	"context"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
)

// visibleClause renders the SQL predicate that admits the contracts this
// caller may see, for a contract row under the given alias. It returns the
// empty string for an unbounded caller, exactly as the shared row-scope
// helpers do, so a caller composes it the same way.
//
// Both arms require a LIVE anchor. An archived deal keeps its foreign key and
// its grants, so without the filter a contract would stay readable — and
// mutable — through a record whose own read already answers 404.
//
// The two arms are a disjunction because a contract has one anchor or the
// other: a contract WITH a deal is judged by that deal, and a contract with no
// deal is judged by its organization. A contract with a deal is deliberately
// NOT also admitted by its organization — the deal is the narrower claim, and
// widening to the company would hand a caller agreements attached to deals
// they cannot see.
func visibleClause(ctx context.Context, alias string, arg func(any) int) (string, error) {
	dealScope, err := auth.ScopeClauseFor(ctx, "deal", "d", arg)
	if err != nil {
		return "", err
	}
	orgScope, err := auth.ScopeClauseFor(ctx, "organization", "o", arg)
	if err != nil {
		return "", err
	}
	// Both empty means the caller reads every row of both anchors, so there is
	// nothing to narrow and the caller composes no clause at all.
	if dealScope == "" && orgScope == "" {
		return "", nil
	}

	qualified := alias
	if qualified != "" {
		qualified += "."
	}

	return storekit.SQLf(`(
		(%[1]sdeal_id IS NOT NULL AND EXISTS (
			SELECT 1 FROM deal d WHERE d.id = %[1]sdeal_id AND d.archived_at IS NULL AND %[2]s))
		OR (%[1]sdeal_id IS NULL AND EXISTS (
			SELECT 1 FROM organization o WHERE o.id = %[1]sorganization_id AND o.archived_at IS NULL AND %[3]s))
	)`, qualified, trueWhenEmpty(dealScope), trueWhenEmpty(orgScope)), nil
}

// trueWhenEmpty renders an absent scope clause as a literal truth. One anchor
// can be unbounded while the other is not — a role may read every organization
// and only its own team's deals — and the arm for the unbounded one still has
// to say something.
func trueWhenEmpty(clause string) string {
	if clause == "" {
		return "TRUE"
	}
	return clause
}
