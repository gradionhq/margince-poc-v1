// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package search

// Narrowing a context walk to ONE body of work.

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// projectScope narrows a walk to one body of work. The zero value scopes
// nothing, which is the ordinary read.
//
// It is a CO-FILTER on an anchor that stays a person, a company or a deal —
// not an anchor of its own. "Catch me up on Acme" and "catch me up on Acme,
// but only the ERP rollout" walk the same neighborhood; the second one just
// refuses to be told about the other engagement.
type projectScope struct {
	projectID string
}

// require is the authority check the scope owes before it filters on a
// project. Filtering by a record is a read of it: the scoped walk answers
// "these touches are filed under this project", which a caller with no
// project grant may not learn, and a caller outside its row scope may not
// learn it exists. Object denial is a 403; an invisible, archived or missing
// project is the same existence-hiding 404 a direct read gives.
//
// The LIVE probe, because EnsureVisible lets an unbounded caller through
// without touching the table — a scope naming a project nobody ever created
// would then answer a full picture as though the filter had matched.
//
// activities.RequireProjectScope is the same check for the timeline list and
// the record pages; a module never imports a sibling (ADR-0054), so this is
// its deliberate copy. Change one, change both.
func (s projectScope) require(ctx context.Context, tx pgx.Tx) error {
	if s.projectID == "" {
		return nil
	}
	projectID, err := ids.ParseAs[ids.ProjectKind](s.projectID)
	if err != nil {
		return err
	}
	if err := auth.Require(ctx, string(datasource.RecordProject), principal.ActionRead); err != nil {
		return err
	}
	return auth.EnsureVisibleLive(ctx, tx, string(datasource.RecordProject), projectID.UUID)
}

// clause renders the exclusion the scope stands for, or "" when there is no
// scope. `activityAlias` is the ACTIVITY alias it applies to.
//
// It has to be a subquery over the activity's other links, not a test on the
// link row already joined: `activity_link_shape` admits exactly ONE target per
// row, so a person-link row carries `project_id IS NULL` by construction. A
// predicate on that row would be true for every row it saw and would filter
// nothing at all — a scope that silently does nothing, which reads in a brief
// exactly like a scope that works.
//
// KEEPING THE UNATTRIBUTED ROWS IS THE POINT. Attribution is optional here, so
// most correspondence on an account carries no project at all, and the rule is
// "filed HERE, or filed nowhere" — not "not filed elsewhere".
//
// Those two readings differ on one row: an activity linked to this project AND
// to another. `uq_activity_link_project` makes that unreachable today (a unique
// index on activity_id over the project rows), but the rule is what this
// predicate owes the reader, so it is spelled as the rule rather than as an
// inference from an index that could be relaxed by a migration this file would
// never hear about.
//
// activities.ActivityWithinProject is the same predicate for the timeline list.
// The two are deliberate copies, not an oversight: a module never imports a
// sibling (ADR-0054), and this rule is about SUBJECT MATTER rather than
// authority, so platform/auth — where the activity scope clauses both modules
// do share already live — is the wrong home for it. Change one, change both.
func (s projectScope) clause(activityAlias string, arg func(any) int) string {
	if s.projectID == "" {
		return ""
	}
	pos := arg(s.projectID)
	return fmt.Sprintf(`(
			EXISTS (
				SELECT 1 FROM activity_link scoped
				WHERE scoped.activity_id = %[1]s.id AND scoped.project_id = $%[2]d)
			OR NOT EXISTS (
				SELECT 1 FROM activity_link filed
				WHERE filed.activity_id = %[1]s.id AND filed.project_id IS NOT NULL))`,
		activityAlias, pos)
}
