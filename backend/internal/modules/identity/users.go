// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

// The access-revocation cascade (events.md §5.6a, B-EP03.10): deactivating
// a user or changing a role must propagate over the bus so read-models,
// webhook fan-out and RBAC caches drop access promptly. The REST surface
// for user administration is a contract fast-follow (crm.yaml notes
// /users and /roles as schema-only today); these service methods are the
// write paths it will call, and the MCP/compose layers can already drive.

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// InviteUserInput carries the admin-supplied details for a new member. No
// password is set here — the invite issues a single-use set-password token.
type InviteUserInput struct {
	Email       string
	DisplayName string
	Role        string
}

// InviteUser provisions a new active member with the one target system role and
// no password, mints a single-use set-password token, and returns the raw token
// so the caller can deliver the invite link. Admin-only. The whole thing — the
// user row, the role grant, the token, the audit row and the user.invited event
// — commits in ONE transaction. A duplicate email answers ErrConflict.
func (s *Service) InviteUser(ctx context.Context, actor Identity, in InviteUserInput) (ids.UserID, string, error) {
	if !actor.hasRole("admin") {
		return ids.UserID{}, "", apperrors.ErrPermissionDenied
	}
	wsID, ok := workspaceFrom(ctx)
	if !ok {
		return ids.UserID{}, "", apperrors.ErrNotFound
	}
	raw, tokenHash, err := mintSessionToken()
	if err != nil {
		return ids.UserID{}, "", err
	}
	ctx = actorCtx(ctx, actor)
	var newUserID ids.UserID
	err = database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		var roleID ids.UUID
		roleErr := tx.QueryRow(ctx, `SELECT id FROM role WHERE key = $1`, in.Role).Scan(&roleID)
		if errors.Is(roleErr, pgx.ErrNoRows) {
			return apperrors.ErrNotFound
		}
		if roleErr != nil {
			return roleErr
		}
		insErr := tx.QueryRow(ctx,
			`INSERT INTO app_user (workspace_id, email, password_hash, display_name, status)
			 VALUES ($1, lower($2), NULL, $3, 'active') RETURNING id`,
			wsID, in.Email, in.DisplayName).Scan(&newUserID)
		var pgErr *pgconn.PgError
		if errors.As(insErr, &pgErr) && pgErr.Code == "23505" {
			return apperrors.ErrConflict
		}
		if insErr != nil {
			return insErr
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO role_assignment (workspace_id, role_id, user_id) VALUES ($1, $2, $3)`,
			wsID, roleID, newUserID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO auth_token (workspace_id, user_id, purpose, token_hash, expires_at)
			 VALUES ($1, $2, 'password_reset', $3, now() + $4::interval)`,
			wsID, newUserID, tokenHash, inviteTokenTTL.String()); err != nil {
			return err
		}
		auditID, err := storekit.Audit(ctx, tx, "create", "user", newUserID.UUID,
			nil, map[string]any{"email": in.Email, "role": in.Role, "status": "active"})
		if err != nil {
			return err
		}
		return storekit.Emit(ctx, tx, auditID, "user.invited", "user", newUserID.UUID,
			map[string]any{"user_id": newUserID, "role": in.Role, "by": actor.UserID})
	})
	if err != nil {
		return ids.UserID{}, "", err
	}
	return newUserID, raw, nil
}

// ReactivateUser returns a deactivated member to 'active' so they may sign in
// again; existing sessions stay revoked and are re-minted on the next login.
// Idempotent on an already-active member. Admin-only. Emits user.reactivated.
func (s *Service) ReactivateUser(ctx context.Context, actor Identity, userID ids.UserID) error {
	if !actor.hasRole("admin") {
		return apperrors.ErrPermissionDenied
	}
	ctx = actorCtx(ctx, actor)
	return database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		var status string
		err := tx.QueryRow(ctx,
			`SELECT status FROM app_user WHERE id = $1 AND archived_at IS NULL FOR UPDATE`,
			userID).Scan(&status)
		if errors.Is(err, pgx.ErrNoRows) {
			return apperrors.ErrNotFound
		}
		if err != nil {
			return err
		}
		if status == "active" {
			return nil
		}
		if _, err := tx.Exec(ctx,
			`UPDATE app_user SET status = 'active' WHERE id = $1`, userID); err != nil {
			return err
		}
		auditID, err := storekit.Audit(ctx, tx, "update", "user", userID.UUID,
			map[string]any{"status": status}, map[string]any{"status": "active"})
		if err != nil {
			return err
		}
		return storekit.Emit(ctx, tx, auditID, "user.reactivated", "user", userID.UUID,
			map[string]any{"user_id": userID, "by": actor.UserID})
	})
}

// hasRole is the identity module's own admin gate for the operations
// RBAC policy documents do not cover (user administration is not a
// record-type permission).
func (id Identity) hasRole(key string) bool {
	for _, r := range id.Roles {
		if r == key {
			return true
		}
	}
	return false
}

// actorCtx binds the acting identity as the storekit principal. The
// methods that take an explicit Identity are their own admission gate,
// so they must not depend on a transport middleware having bound the
// actor for the audit stamp — a direct service caller is just as valid.
func actorCtx(ctx context.Context, id Identity) context.Context {
	return principal.WithActor(ctx, principal.Principal{
		Type:        principal.PrincipalHuman,
		ID:          "human:" + id.UserID.String(),
		UserID:      id.UserID.UUID,
		TeamIDs:     rawTeamIDs(id.Teams),
		SeatType:    principal.SeatType(id.SeatType),
		Permissions: id.Permissions,
	})
}

// DeactivateUserInput carries the optional operator-supplied reason that
// rides the event payload (events.md §5.6a: {user_id, by, reason?}).
type DeactivateUserInput struct {
	UserID ids.UserID
	Reason *string
}

// DeactivateUser flips the user to 'deactivated' and hard-revokes every
// live session and every passport they bound, in ONE transaction with
// the audit row and the user.deactivated event (§5.6a: the cascade seam
// — per-call re-auth already refuses a deactivated principal, the event
// lets caches and fan-outs drop access without polling). Admin-only;
// idempotent on an already-deactivated user (no duplicate event).
func (s *Service) DeactivateUser(ctx context.Context, actor Identity, in DeactivateUserInput) error {
	if !actor.hasRole("admin") {
		return apperrors.ErrPermissionDenied
	}
	ctx = actorCtx(ctx, actor)
	return database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		var status string
		err := tx.QueryRow(ctx,
			`SELECT status FROM app_user WHERE id = $1 AND archived_at IS NULL FOR UPDATE`,
			in.UserID).Scan(&status)
		if errors.Is(err, pgx.ErrNoRows) {
			return apperrors.ErrNotFound
		}
		if err != nil {
			return err
		}
		if status == "deactivated" {
			return nil
		}
		if _, err := tx.Exec(ctx,
			`UPDATE app_user SET status = 'deactivated' WHERE id = $1`, in.UserID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`UPDATE session SET revoked_at = now() WHERE user_id = $1 AND revoked_at IS NULL`,
			in.UserID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`UPDATE passport SET revoked_at = now() WHERE on_behalf_of = $1 AND revoked_at IS NULL`,
			in.UserID); err != nil {
			return err
		}
		auditID, err := storekit.Audit(ctx, tx, "update", "user", in.UserID.UUID,
			map[string]any{"status": status}, map[string]any{"status": "deactivated"})
		if err != nil {
			return err
		}
		payload := map[string]any{"user_id": in.UserID, "by": actor.UserID}
		if in.Reason != nil {
			payload["reason"] = *in.Reason
		}
		return storekit.Emit(ctx, tx, auditID, "user.deactivated", "user", in.UserID.UUID, payload)
	})
}

// ChangeUserRole replaces the user's role assignments with the single
// target system role and emits role.changed (§5.6a: {user_id, from_role?,
// to_role, by}) so the effective-permission caches never serve a stale
// grant. from_role rides the payload only when the previous state was a
// single role — a multi-role history has no one "from". Admin-only.
func (s *Service) ChangeUserRole(ctx context.Context, actor Identity, userID ids.UserID, toRole string) error {
	if !actor.hasRole("admin") {
		return apperrors.ErrPermissionDenied
	}
	ctx = actorCtx(ctx, actor)
	return database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM app_user WHERE id = $1 AND archived_at IS NULL)`,
			userID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return apperrors.ErrNotFound
		}
		var roleID ids.UUID
		err := tx.QueryRow(ctx, `SELECT id FROM role WHERE key = $1`, toRole).Scan(&roleID)
		if errors.Is(err, pgx.ErrNoRows) {
			return apperrors.ErrNotFound
		}
		if err != nil {
			return err
		}

		rows, err := tx.Query(ctx,
			`SELECT r.key FROM role_assignment ra JOIN role r ON r.id = ra.role_id WHERE ra.user_id = $1`,
			userID)
		if err != nil {
			return err
		}
		fromRoles, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		if len(fromRoles) == 1 && fromRoles[0] == toRole {
			return nil // already exactly this role; no event to publish
		}

		if _, err := tx.Exec(ctx,
			`DELETE FROM role_assignment WHERE user_id = $1`, userID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO role_assignment (workspace_id, role_id, user_id)
			 VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid, $1, $2)`,
			roleID, userID); err != nil {
			return err
		}
		auditID, err := storekit.Audit(ctx, tx, "assign", "user", userID.UUID,
			map[string]any{"roles": fromRoles}, map[string]any{"roles": []string{toRole}})
		if err != nil {
			return err
		}
		payload := map[string]any{"user_id": userID, "to_role": toRole, "by": actor.UserID}
		if len(fromRoles) == 1 {
			payload["from_role"] = fromRoles[0]
		}
		return storekit.Emit(ctx, tx, auditID, "role.changed", "user", userID.UUID, payload)
	})
}
