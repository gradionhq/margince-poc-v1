// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

// How well a project's correspondence is filed. The project page asks two
// questions of the timeline that the timeline's own list cannot answer: how
// much is ON the project, and how much is circling it — linked to one of its
// deals or one of its stakeholders — while carrying no project link at all.
// The second number is the filing debt a rep can work down; the first is the
// whole-lifecycle count the header shows.

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// ProjectActivityFacts is the timeline's answer about one project, counted
// over the caller's activity row scope — so a count never admits an activity
// the caller's timeline would not show.
type ProjectActivityFacts struct {
	// Attributed is every live activity linked to the project — its whole
	// lifecycle, not a page of it.
	Attributed int
	// UnattributedNearby is every live activity linked to one of the
	// project's deals or to one of its stakeholder people that carries NO
	// project link — not this project's, not another's. An activity filed
	// under a sibling engagement is somebody's, and is not debt here.
	UnattributedNearby int
	// LastActivityAt is the newest attributed activity's instant; nil when
	// nothing is filed under the project yet.
	LastActivityAt *time.Time
	// OpenCommitments is every open task filed under the project — the
	// whole set, where the commitments section carries a page of it.
	OpenCommitments int
}

// ProjectActivityFactsTx counts inside a caller-opened transaction. The
// project is the record the count is narrowed TO, so it is gated before it
// is counted against — the same existence-hiding rule the timeline list
// keeps for its narrowing target.
func (s *Store) ProjectActivityFactsTx(ctx context.Context, tx pgx.Tx, id ids.ProjectID) (ProjectActivityFacts, error) {
	if err := auth.Require(ctx, "activity", principal.ActionRead); err != nil {
		return ProjectActivityFacts{}, err
	}
	if err := auth.EnsureVisible(ctx, tx, "project", id.UUID); err != nil {
		return ProjectActivityFacts{}, err
	}
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	projectPos := arg(id)
	where := []string{"a.archived_at IS NULL"}
	scope, err := auth.ActivityContentClause(ctx, "a", arg)
	if err != nil {
		return ProjectActivityFacts{}, err
	}
	if scope != "" {
		where = append(where, scope)
	}
	// The neighbourhood is bounded the way the caller's own lists are: a deal
	// they may not list and a seat whose endpoints they may not read do not
	// pull their activities into the count, or the number would reflect
	// records the caller cannot open.
	dealScope, err := auth.ScopeClauseFor(ctx, "deal", "d", arg)
	if err != nil {
		return ProjectActivityFacts{}, err
	}
	if dealScope == "" {
		dealScope = scopeUnbounded
	}
	edgeScope, err := auth.RelationshipEndpointScope(ctx, "r", arg)
	if err != nil {
		return ProjectActivityFacts{}, err
	}
	if edgeScope == "" {
		edgeScope = scopeUnbounded
	}
	var facts ProjectActivityFacts
	// The candidate set is every activity with ANY link into the project's
	// neighbourhood; each is then classified once, so an activity linked to
	// both the project and its deal is one row and counted on one side.
	err = tx.QueryRow(ctx, sprintf(`
		WITH nearby AS (
			SELECT DISTINCT l.activity_id
			FROM activity_link l
			LEFT JOIN deal d ON d.id = l.deal_id AND d.archived_at IS NULL AND %[3]s
			LEFT JOIN relationship r ON r.person_id = l.person_id
			     AND r.kind = 'project_stakeholder' AND r.archived_at IS NULL AND %[4]s
			WHERE l.project_id = $%[1]d OR d.project_id = $%[1]d OR r.project_id = $%[1]d
		)
		SELECT count(*) FILTER (WHERE f.filed_here),
		       max(a.occurred_at) FILTER (WHERE f.filed_here),
		       count(*) FILTER (WHERE NOT f.filed_anywhere),
		       count(*) FILTER (WHERE f.filed_here AND a.kind = 'task' AND NOT a.is_done)
		FROM activity a
		JOIN nearby n ON n.activity_id = a.id
		CROSS JOIN LATERAL (
			SELECT EXISTS (SELECT 1 FROM activity_link l
			               WHERE l.activity_id = a.id AND l.project_id = $%[1]d) AS filed_here,
			       EXISTS (SELECT 1 FROM activity_link l
			               WHERE l.activity_id = a.id AND l.project_id IS NOT NULL) AS filed_anywhere
		) f
		WHERE %[2]s`, projectPos, strings.Join(where, " AND "), dealScope, edgeScope), args...).
		Scan(&facts.Attributed, &facts.LastActivityAt, &facts.UnattributedNearby, &facts.OpenCommitments)
	if err != nil {
		return ProjectActivityFacts{}, err
	}
	return facts, nil
}
