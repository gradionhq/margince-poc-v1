// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package capture

// The permission a project-attribution rung reads under.
//
// The two SQL rungs of the ladder both return a PROJECT ID that is then copied
// onto an activity the caller can read, so both are reads OF A PROJECT and both
// carry the project's own gate: the object grant first, then the row-scope
// predicate. Without the object grant, a connector principal holding only
// activity create/read would have a project id planted on its timeline out of a
// table it may not open — EnsureLinkTarget at the write checks row visibility,
// which is a different question and does not answer this one.

import (
	"context"
	"errors"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// projectObject is the RBAC object name of the project record, spelled here
// because capture may not import the module that owns the table.
const projectObject = "project"

// projectScope is the caller's project read grant, resolved once and shared by
// every rung. It carries no rendered SQL on purpose: the two rungs bind
// different numbers of arguments ahead of the predicate, so a pre-rendered `$N`
// would be wrong in one of them. projectScopeClause renders it per query
// instead, against that query's own argument allocator.
type projectScope struct {
	// deniedOutright reports that the caller holds no project read grant at
	// all, so no rung may run. It is separate from an empty predicate, which is
	// the OPPOSITE case — an unbounded reader, who may see every project.
	deniedOutright bool
}

// readableProjectScope resolves the caller's project read grant once, for every
// rung to share.
//
// A caller with no grant is reported as denied rather than as an error: a role
// that never sees projects is a legitimate role, and its mail simply attributes
// to nothing. Failing the attribution here would turn an ordinary permission
// setting into a fault an operator has to read.
func readableProjectScope(ctx context.Context) (projectScope, error) {
	if err := auth.Require(ctx, projectObject, principal.ActionRead); err != nil {
		if errors.Is(err, apperrors.ErrPermissionDenied) {
			return projectScope{deniedOutright: true}, nil
		}
		return projectScope{}, err
	}
	return projectScope{}, nil
}

// projectScopeClause renders the caller's row-scope predicate as a trailing
// ` AND …` over the project alias, allocating its binds through the query's own
// arg function so they land after whatever placeholders that query already
// used. An unbounded reader renders the empty string.
func projectScopeClause(ctx context.Context, alias string, arg func(any) int) (string, error) {
	clause, err := auth.ScopeClauseFor(ctx, projectObject, alias, arg)
	if err != nil {
		return "", err
	}
	if clause == "" {
		return "", nil
	}
	return " AND " + clause, nil
}
