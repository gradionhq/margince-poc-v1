// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package org360

// The vocabulary every section read shares — the per-section row cap, the
// truncation flag, and the two row-scope predicates — plus the next-steps
// section itself. The larger sections live one concept per file
// (contacts.go, deals.go, collections.go).
//
// Every section prunes to the caller's row scope with the same
// platform/auth predicates the module lists use, so a section can never
// out-see the dedicated endpoint it summarizes.

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// sectionLimit is how many rows of a nested collection one 360 carries.
// The section is a summary with a "there is more" flag, not a paging
// surface: follow-up pages come from the dedicated endpoint for that
// collection, which owns the cursor vocabulary.
const sectionLimit = 25

// truncate cuts a section to sectionLimit and reports whether it had to.
func truncate[T any](rows []T) ([]T, crmcontracts.PageInfo) {
	if len(rows) > sectionLimit {
		return rows[:sectionLimit], crmcontracts.PageInfo{HasMore: true}
	}
	return rows, crmcontracts.PageInfo{HasMore: false}
}

// scopeAll is the predicate an unbounded (admin) caller gets: the SQL
// that embeds a row-scope clause then needs only one spelling.
const scopeAll = "TRUE"

// scopeClause resolves one object's row-scope predicate for the caller,
// answering scopeAll for an unbounded caller.
func scopeClause(ctx context.Context, object, alias string, arg func(any) int) (string, error) {
	clause, err := auth.ScopeClauseFor(ctx, object, alias, arg)
	if err != nil {
		return "", err
	}
	if clause == "" {
		return scopeAll, nil
	}
	return clause, nil
}

// linkScope resolves "this activity_link row points at a record the caller
// can read" for one table alias, answering scopeAll for an unbounded caller.
func linkScope(ctx context.Context, alias string, arg func(any) int) (string, error) {
	clause, err := auth.LinkTargetVisibleClause(ctx, alias, arg)
	if err != nil {
		return "", err
	}
	if clause == "" {
		return scopeAll, nil
	}
	return clause, nil
}

// nextStepsSection reads the account's open tasks in the order a rep works
// them: overdue first, then dated, then undated. A task reaches the
// account through any of its links — the task itself, its deal, or the
// contact it is about — which is why the reachability test is an EXISTS
// over all three arms rather than one join.
//
// The two linked ids it reports carry their OWN row scope. Task visibility
// is an any-link rule, so a task reachable through a visible contact would
// otherwise hand back the id of a deal the caller may not read — the task
// is theirs to see, the colleague's deal is not.
func nextStepsSection(ctx context.Context, tx pgx.Tx, orgID ids.OrganizationID, now time.Time) ([]crmcontracts.Organization360NextStep, crmcontracts.PageInfo, error) {
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	orgPos := arg(orgID)
	activityScope, err := auth.ActivityScopeClause(ctx, "a", arg)
	if err != nil {
		return nil, crmcontracts.PageInfo{}, err
	}
	if activityScope == "" {
		activityScope = scopeAll
	}
	// Two renderings of the same predicate, one per sub-select alias. Each
	// registers its own bind positions through arg(), which is why they are
	// built rather than string-substituted from one another.
	linkVisible, err := linkScope(ctx, "dl", arg)
	if err != nil {
		return nil, crmcontracts.PageInfo{}, err
	}
	personVisible, err := linkScope(ctx, "pl", arg)
	if err != nil {
		return nil, crmcontracts.PageInfo{}, err
	}
	rows, err := tx.Query(ctx, fmt.Sprintf(`
		SELECT a.id, coalesce(a.subject, ''), a.due_at, a.assignee_id,
		       (SELECT dl.deal_id FROM activity_link dl
		         WHERE dl.activity_id = a.id AND dl.entity_type = 'deal' AND %[5]s
		         ORDER BY dl.id LIMIT 1),
		       (SELECT pl.person_id FROM activity_link pl
		         WHERE pl.activity_id = a.id AND pl.entity_type = 'person' AND %[6]s
		         ORDER BY pl.id LIMIT 1)
		FROM activity a
		WHERE a.kind = 'task' AND NOT a.is_done AND a.archived_at IS NULL AND %[1]s
		  AND EXISTS (
		    SELECT 1 FROM activity_link l
		    LEFT JOIN deal d ON d.id = l.deal_id
		    LEFT JOIN relationship r ON r.person_id = l.person_id AND r.kind = 'employment'
		      AND r.ended_at IS NULL AND r.archived_at IS NULL
		    WHERE l.activity_id = a.id
		      AND (l.organization_id = $%[2]d OR d.organization_id = $%[3]d OR r.organization_id = $%[4]d))
		ORDER BY (a.due_at IS NULL), a.due_at, a.id
		LIMIT %[7]d`,
		activityScope, orgPos, orgPos, orgPos, linkVisible, personVisible, sectionLimit+1), args...)
	if err != nil {
		return nil, crmcontracts.PageInfo{}, err
	}
	steps, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (crmcontracts.Organization360NextStep, error) {
		var step crmcontracts.Organization360NextStep
		var id ids.UUID
		var assignee, dealID, personID *ids.UUID
		if err := row.Scan(&id, &step.Subject, &step.DueAt, &assignee, &dealID, &personID); err != nil {
			return step, err
		}
		step.ActivityId = openapi_types.UUID(id)
		step.AssigneeId = uuidPtr(assignee)
		step.LinkedDealId = uuidPtr(dealID)
		step.LinkedPersonId = uuidPtr(personID)
		step.Overdue = step.DueAt != nil && step.DueAt.Before(now)
		return step, nil
	})
	if err != nil {
		return nil, crmcontracts.PageInfo{}, err
	}
	// Overdue leads by construction: the SQL orders dated before undated and
	// earliest first, and overdue is exactly "dated before now".
	steps, page := truncate(steps)
	if steps == nil {
		steps = []crmcontracts.Organization360NextStep{}
	}
	return steps, page, nil
}

func uuidPtr(id *ids.UUID) *openapi_types.UUID {
	if id == nil {
		return nil
	}
	v := openapi_types.UUID(*id)
	return &v
}
