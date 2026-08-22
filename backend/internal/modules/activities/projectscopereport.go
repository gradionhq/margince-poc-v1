// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

// What a read narrowed to one project reports about the narrowing.
//
// A scoped brief, answer or draft is fluent whether the scope dropped nothing
// or dropped most of the account, and the two read identically. The report
// is what lets a surface print "Scoped to KEY · 4 of 11 activities" from the
// server's own count, so the reader can see how much of the account the
// words are standing on before reading them.

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// ProjectScopeReport names the project a read was narrowed to and counts what
// the narrowing kept.
type ProjectScopeReport struct {
	ProjectID ids.ProjectID
	Name      string
	Key       string
	// InScope is how many of the anchor's activities the scoped read could
	// see — filed under this project or under none; Total is the same anchor
	// unscoped. Both are nil when the caller holds no activity grant: the
	// project is still named, the count is not invented.
	InScope *int
	Total   *int
}

// Wire renders the report in the contract's shape. A project without a key
// sends null rather than "", so a renderer falls back to the name instead of
// printing "Scoped to ".
func (r ProjectScopeReport) Wire() crmcontracts.ProjectScope {
	out := crmcontracts.ProjectScope{
		ProjectId: openapi_types.UUID(r.ProjectID.UUID),
		Name:      r.Name,
		InScope:   r.InScope,
		Total:     r.Total,
	}
	if r.Key != "" {
		out.Key = &r.Key
	}
	return out
}

// ReadProjectScope reads the report for one project over one anchor.
//
// anchor renders the predicate that ties an activity (alias `a`) to the read's
// anchor — the organization walk or the person link — registering its binds
// through arg. The caller has already run RequireProjectScope, which is why
// the project row is read here without a gate of its own: a project that is
// not live or not visible never reaches this.
//
// The two counts come from ONE statement. A scoped count and an unscoped
// count read in two statements can straddle a capture, and "12 of 11" is the
// kind of number a reader stops trusting the whole line over.
func ReadProjectScope(
	ctx context.Context, tx pgx.Tx, projectID ids.ProjectID, anchor func(arg func(any) int) string,
) (ProjectScopeReport, error) {
	out := ProjectScopeReport{ProjectID: projectID}
	var key *string
	err := tx.QueryRow(ctx, `SELECT name, key FROM project WHERE id = $1 AND archived_at IS NULL`, projectID).
		Scan(&out.Name, &key)
	if errors.Is(err, pgx.ErrNoRows) {
		return ProjectScopeReport{}, apperrors.ErrNotFound
	}
	if err != nil {
		return ProjectScopeReport{}, fmt.Errorf("read the scoping project: %w", err)
	}
	if key != nil {
		out.Key = *key
	}
	if err := auth.Require(ctx, "activity", principal.ActionRead); err != nil {
		if errors.Is(err, apperrors.ErrPermissionDenied) {
			return out, nil
		}
		return ProjectScopeReport{}, err
	}
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	anchored := anchor(arg)
	// The DISCOVER gate, not the content gate: a conversation this caller may
	// know exists but not open is still one the scope kept or dropped, and the
	// timeline counts it the same way.
	scope, err := auth.ActivityDiscoverClause(ctx, "a", arg)
	if err != nil {
		return ProjectScopeReport{}, err
	}
	if scope == "" {
		scope = scopeUnbounded
	}
	within := ActivityWithinProject(arg(projectID))
	var inScope, total int
	if err := tx.QueryRow(ctx, sprintf(`
		SELECT count(*) FILTER (WHERE %s), count(*)
		FROM activity a
		WHERE a.archived_at IS NULL AND %s AND (%s)`, within, anchored, scope), args...).
		Scan(&inScope, &total); err != nil {
		return ProjectScopeReport{}, fmt.Errorf("count the activities the project scope kept: %w", err)
	}
	out.InScope, out.Total = &inScope, &total
	return out, nil
}
