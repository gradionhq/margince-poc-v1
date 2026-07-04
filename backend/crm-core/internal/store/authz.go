package store

// Activity row-scoping (B-EP03.3a, decisions/0006). The entity-agnostic
// RBAC lives in platform/auth (ADR-0054 §8); what stays here is the one
// scope rule that knows this module's tables: an activity has no owner,
// so its visibility walks the activity_link rows to the person/
// organization/deal records it attaches to.

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// activityScopeClause is the activity analogue of auth.ScopeClause:
// activities have no owner, but their free-text inherits the
// sensitivity of the records they attach to. An activity is visible when
// ANY linked person/organization/deal is visible under the caller's row
// scope, or when it has no links at all (a workspace-shared note —
// decisions/0006). alias names the activity table in the outer query.
func activityScopeClause(ctx context.Context, alias string, arg func(any) int) (string, error) {
	p, err := storekit.Actor(ctx)
	if err != nil {
		return "", err
	}
	if auth.Unbounded(p) {
		return "", nil
	}
	visible := auth.OwnerPredicate(p, arg)
	return sprintf(`(NOT EXISTS (SELECT 1 FROM activity_link nl WHERE nl.activity_id = %[1]s.id)
	 OR EXISTS (SELECT 1 FROM activity_link l WHERE l.activity_id = %[1]s.id AND (
	      (l.person_id IS NOT NULL AND EXISTS (SELECT 1 FROM person sp WHERE sp.id = l.person_id AND %[2]s))
	   OR (l.organization_id IS NOT NULL AND EXISTS (SELECT 1 FROM organization so WHERE so.id = l.organization_id AND %[3]s))
	   OR (l.deal_id IS NOT NULL AND EXISTS (SELECT 1 FROM deal sd WHERE sd.id = l.deal_id AND %[4]s)))))`,
		alias, visible("sp"), visible("so"), visible("sd")), nil
}

// ensureActivityVisible is auth.EnsureVisible for activities, using the
// linked-entity scope above; out of scope reads as ErrNotFound.
func ensureActivityVisible(ctx context.Context, tx pgx.Tx, id ids.UUID) error {
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	idPos := arg(id)

	clause, err := activityScopeClause(ctx, "a", arg)
	if err != nil {
		return err
	}
	if clause == "" {
		return nil
	}
	var visible bool
	err = tx.QueryRow(ctx,
		sprintf(`SELECT EXISTS (SELECT 1 FROM activity a WHERE a.id = $%d AND %s)`, idPos, clause),
		args...).Scan(&visible)
	if err != nil {
		return err
	}
	if !visible {
		return apperrors.ErrNotFound
	}
	return nil
}
