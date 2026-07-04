package auth

// Object-level RBAC + row-level scoping (B-EP03.2/.3a, features/04 §1),
// entity-agnostic and table-parameterized — the per-module stores call
// these at every entry point so every caller — HTTP, the MCP tool
// surface — rides the same enforcement path (architecture/06: no agent
// bypass; ADR-0054 §8: authorization is platform policy, not a domain
// module). Object denial answers ErrPermissionDenied (403); a row outside
// the caller's scope answers ErrNotFound, exactly like a row in another
// tenant (existence is not disclosed).

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// rbacActor resolves the acting principal; a missing actor is a
// programming error (the middleware always binds one).
func rbacActor(ctx context.Context) (principal.Principal, error) {
	p, ok := principal.Actor(ctx)
	if !ok {
		return principal.Principal{}, errors.New("auth: no actor bound to context")
	}
	return p, nil
}

// Require is the object-level admission gate: the actor's merged role
// policy must grant the action on the object type. The system principal
// (workspace provisioning) is trusted by construction and has no role.
func Require(ctx context.Context, object string, action principal.Action) error {
	p, err := rbacActor(ctx)
	if err != nil {
		return err
	}
	if p.Type == principal.PrincipalSystem {
		return nil
	}
	if !p.Permissions.Allows(object, action) {
		return fmt.Errorf("%s.%s: %w", object, action, apperrors.ErrPermissionDenied)
	}
	return nil
}

// Unbounded reports whether the actor sees every row of a permitted
// object: the system principal, or row_scope=all.
func Unbounded(p principal.Principal) bool {
	return p.Type == principal.PrincipalSystem || p.Permissions.RowScope == principal.RowScopeAll
}

// OwnerPredicate renders the own/team visibility test over one table's
// owner_id (qualified by alias when non-empty). It returns a FUNCTION so
// callers embedding the predicate for several tables (the activity link
// walk) register $me/$teams once and reuse the positions.
func OwnerPredicate(p principal.Principal, arg func(any) int) func(alias string) string {
	me := arg(p.UserID)
	col := func(alias string) string {
		if alias == "" {
			return "owner_id"
		}
		return alias + ".owner_id"
	}
	if p.Permissions.RowScope == principal.RowScopeTeam {
		teams := arg(p.TeamIDs)
		return func(alias string) string {
			return fmt.Sprintf(`(%[1]s IS NULL OR %[1]s = $%[2]d OR %[1]s IN (
			   SELECT tm.user_id FROM team_membership tm WHERE tm.team_id = ANY($%[3]d)))`,
				col(alias), me, teams)
		}
	}
	// own — and the zero value: an unresolved scope never widens.
	return func(alias string) string {
		return fmt.Sprintf(`(%[1]s IS NULL OR %[1]s = $%[2]d)`, col(alias), me)
	}
}

// ScopeClause renders the own/team/all row-visibility predicate over an
// owner_id column (B-EP03.3a). arg registers a query argument and
// returns its 1-based position, matching the list builders' convention.
// An empty clause means unbounded (row_scope=all, or the system actor).
// Ownerless rows (owner_id IS NULL) are workspace-shared and visible at
// every tier (decisions/0006).
func ScopeClause(ctx context.Context, arg func(any) int) (string, error) {
	p, err := rbacActor(ctx)
	if err != nil {
		return "", err
	}
	if Unbounded(p) {
		return "", nil
	}
	return OwnerPredicate(p, arg)(""), nil
}

// EnsureLinkTarget verifies an activity link's target row exists AND is
// visible to the caller — an explicit RLS-scoped probe, because the FK
// that would otherwise catch a bad id is checked as the table owner and
// so bypasses RLS: without this, a guessed foreign UUID would persist a
// cross-tenant link. Unlike EnsureVisible, unbounded actors do not skip
// the existence half.
func EnsureLinkTarget(ctx context.Context, tx pgx.Tx, table string, id ids.UUID) error {
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	idPos := arg(id)

	clause, err := ScopeClause(ctx, arg)
	if err != nil {
		return err
	}
	q := fmt.Sprintf(`SELECT EXISTS (SELECT 1 FROM %s WHERE id = $%d AND archived_at IS NULL`, table, idPos)
	if clause != "" {
		q += " AND " + clause
	}
	q += ")"

	var visible bool
	if err := tx.QueryRow(ctx, q, args...).Scan(&visible); err != nil {
		return err
	}
	if !visible {
		return apperrors.ErrNotFound
	}
	return nil
}

// VisibleTo probes whether one row passes the caller's row scope WITHOUT
// erroring — for the dedupe pre-checks, which must answer 409 either way
// but may only disclose the existing row's id when the caller could read
// it (existence-hiding must survive the conflict path).
func VisibleTo(ctx context.Context, tx pgx.Tx, table string, id ids.UUID) (bool, error) {
	err := EnsureVisible(ctx, tx, table, id)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, apperrors.ErrNotFound):
		return false, nil
	default:
		return false, err
	}
}

// EnsureVisible applies the row scope to a single-row operation: get,
// update, archive, advance. Out of scope reads as ErrNotFound — the
// caller cannot distinguish "not yours" from "not there", by design.
// Activities scope through their links (the activities module's
// link-walk clause); pipelines have no owner and are governed by object
// grants only.
func EnsureVisible(ctx context.Context, tx pgx.Tx, table string, id ids.UUID) error {
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	idPos := arg(id)

	clause, err := ScopeClause(ctx, arg)
	if err != nil {
		return err
	}
	if clause == "" {
		return nil
	}

	var visible bool
	err = tx.QueryRow(ctx,
		fmt.Sprintf(`SELECT EXISTS (SELECT 1 FROM %s WHERE id = $%d AND %s)`, table, idPos, clause),
		args...).Scan(&visible)
	if err != nil {
		return err
	}
	if !visible {
		return apperrors.ErrNotFound
	}
	return nil
}

// auditActionGrant maps each audit_log.action verb onto the CRUD grant
// that authorizes it. Package-level: AuthzRule sits on every write path.
var auditActionGrant = map[string]principal.Action{
	"create":        principal.ActionCreate,
	"update":        principal.ActionUpdate,
	"assign":        principal.ActionUpdate,
	"advance_stage": principal.ActionUpdate,
	"restore":       principal.ActionUpdate,
	"archive":       principal.ActionDelete,
	"merge":         principal.ActionUpdate,
	"promote":       principal.ActionUpdate,
}

// AuthzRule renders the audit_log.authorization_rule attribution for a
// permitted mutation: which merged role policy allowed which action.
func AuthzRule(p principal.Principal, entityType string, auditAction string) string {
	if p.Type == principal.PrincipalSystem {
		return "system"
	}
	action, ok := auditActionGrant[auditAction]
	if !ok {
		return ""
	}
	return p.Permissions.Rule(entityType, action)
}
