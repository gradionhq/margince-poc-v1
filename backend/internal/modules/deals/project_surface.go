// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// The project reads the relationship surfaces compose: which live bodies of
// work a company carries, and which ones a person is part of. Both are
// summaries for a record page, not a paging list — the full list with its
// cursor vocabulary stays ListProjects.

package deals

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// ProjectCard is one project as a record page shows it: enough to name it,
// say where it stands and who holds it, never the whole row. The contract
// shape is returned directly because the company page and the person page
// render the same rows, and one mapping is what keeps them reading alike.
type ProjectCard = crmcontracts.Organization360Project

// projectSurfaceCap bounds a record page's project list. A company with more
// live projects than this is a portfolio, which the projects list answers.
const projectSurfaceCap = 25

// projectCardColumns is the SELECT both surface reads share, for a query that
// aliases project as p and the owner as u.
const projectCardColumns = `p.id, p.name, p.key, p.phase, p.last_activity_at, p.target_end_date, p.owner_id,
	coalesce(u.display_name, '')`

// projectCardOrder puts the work in motion first: delivering, then pursuing,
// then the initiatives, and closed projects last — a page reader wants what is
// live, and within one phase the most recently touched project.
const projectCardOrder = `ORDER BY CASE p.phase
		WHEN 'delivering' THEN 0 WHEN 'pursuing' THEN 1 WHEN 'initiative' THEN 2 ELSE 3 END,
	p.last_activity_at DESC NULLS LAST, p.created_at DESC, p.id`

// ListProjectsForOrganizationTx lists the company's unarchived projects under
// the caller's project row scope, work in motion first.
func (s *Store) ListProjectsForOrganizationTx(ctx context.Context, tx pgx.Tx, orgID ids.OrganizationID) ([]ProjectCard, error) {
	if err := auth.Require(ctx, projectObject, principal.ActionRead); err != nil {
		return nil, err
	}
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	orgPos := arg(orgID)
	scope, err := projectScopeBound(ctx, arg)
	if err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, fmt.Sprintf(`
		SELECT `+projectCardColumns+`
		  FROM project p
		  LEFT JOIN app_user u ON u.id = p.owner_id AND u.status = 'active' AND u.archived_at IS NULL
		 WHERE p.organization_id = $%d AND p.archived_at IS NULL AND (%s)
		 `+projectCardOrder+`
		 LIMIT %d`, orgPos, scope, projectSurfaceCap), args...)
	if err != nil {
		return nil, err
	}
	return collectProjectCards(rows)
}

// ListProjectsForPersonTx lists the unarchived projects a person is part of:
// the ones they hold a live stakeholder seat on, plus every project of the
// company they currently work for. One row per project, work in motion first.
//
// The seat edges are bounded by the edge read scope and the projects by the
// project row scope, because they answer different questions: the edge bound
// asks which seats this caller may learn of, the row scope which projects.
func (s *Store) ListProjectsForPersonTx(ctx context.Context, tx pgx.Tx, personID ids.PersonID) ([]ProjectCard, error) {
	if err := auth.Require(ctx, projectObject, principal.ActionRead); err != nil {
		return nil, err
	}
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	personPos := arg(personID)
	scope, err := projectScopeBound(ctx, arg)
	if err != nil {
		return nil, err
	}
	seatBound, err := edgeBound(ctx, "r", arg)
	if err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, fmt.Sprintf(`
		SELECT `+projectCardColumns+`
		  FROM project p
		  LEFT JOIN app_user u ON u.id = p.owner_id AND u.status = 'active' AND u.archived_at IS NULL
		 WHERE p.archived_at IS NULL AND (%[2]s)
		   AND (EXISTS (
		            SELECT 1 FROM relationship r
		             WHERE r.kind = 'project_stakeholder' AND r.project_id = p.id
		               AND r.person_id = $%[1]d AND r.archived_at IS NULL AND r.ended_at IS NULL
		               AND (%[3]s))
		        OR EXISTS (
		            SELECT 1 FROM relationship e
		             WHERE e.kind = 'employment' AND e.person_id = $%[1]d
		               AND e.organization_id = p.organization_id AND e.is_current_primary
		               AND e.archived_at IS NULL AND e.ended_at IS NULL))
		 `+projectCardOrder+`
		 LIMIT %[4]d`, personPos, scope, seatBound, projectSurfaceCap), args...)
	if err != nil {
		return nil, err
	}
	return collectProjectCards(rows)
}

// collectProjectCards drains one surface read. An empty result is an empty
// list on the wire, never null: the section is present and says "none",
// which is a different fact from the section being withheld.
func collectProjectCards(rows pgx.Rows) ([]ProjectCard, error) {
	cards, err := pgx.CollectRows(rows, scanProjectCard)
	if err != nil {
		return nil, err
	}
	if cards == nil {
		cards = []ProjectCard{}
	}
	return cards, nil
}

// projectScopeBound is the caller's project row scope as a predicate on alias
// p, admitting every row for a caller bounded by nothing.
func projectScopeBound(ctx context.Context, arg func(any) int) (string, error) {
	scope, err := auth.ScopeClauseFor(ctx, projectObject, "p", arg)
	if err != nil {
		return "", err
	}
	if scope == "" {
		return predicateAlways, nil
	}
	return scope, nil
}

func scanProjectCard(row pgx.CollectableRow) (ProjectCard, error) {
	var card ProjectCard
	var id ids.UUID
	var ownerID *ids.UUID
	var phase, ownerName string
	var targetEnd *time.Time
	if err := row.Scan(&id, &card.Name, &card.Key, &phase, &card.LastActivityAt,
		&targetEnd, &ownerID, &ownerName); err != nil {
		return ProjectCard{}, err
	}
	card.ProjectId = openapi_types.UUID(id)
	card.Phase = crmcontracts.Organization360ProjectPhase(phase)
	if targetEnd != nil {
		card.TargetEndDate = &openapi_types.Date{Time: *targetEnd}
	}
	card.OwnerId = uuidPtr(ownerID)
	// An empty name is "no active owner", which the wire says as null rather
	// than as a member with no name.
	if ownerName != "" {
		card.OwnerName = &ownerName
	}
	return card, nil
}
