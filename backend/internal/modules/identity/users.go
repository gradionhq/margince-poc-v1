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
	"fmt"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// userStatusActive is the app_user.status an invited/reactivated member holds;
// userAuditKeyStatus is the audit before/after image key for that column.
const (
	userStatusActive      = "active"
	userStatusDeactivated = "deactivated"
	userAuditKeyStatus    = "status"
	roleAdmin             = "admin"
)

// The distinct refusals this surface can answer with. Each WRAPS
// apperrors.ErrConflict, so every caller that only asks "was this a conflict?"
// is unaffected, while a handler can tell them apart and say which one it was:
// the bare sentinel reaches the operator as the single word "conflict", which
// names neither what happened nor what to do about it. The advice differs per
// verb (deactivating vs demoting the last admin), so the handler supplies the
// wording and these carry only the cause.
var (
	errEmailTaken      = fmt.Errorf("%w: a member with this email already exists", apperrors.ErrConflict)
	errNotDeactivated  = fmt.Errorf("%w: the member is not deactivated", apperrors.ErrConflict)
	errLastActiveAdmin = fmt.Errorf("%w: the member is the only active administrator", apperrors.ErrConflict)
	// The agent seat holds no role, ever. Its authority is the passport granting
	// it intersected with the human that passport names, so a role on its own
	// row grants nothing today and is a standing grant nothing bounds tomorrow —
	// and while it held `admin` it would count toward the last-active-admin
	// guard below, letting an operator deactivate the final human administrator
	// on the strength of an identity that can never sign in.
	errAgentSeatHoldsNoRole = fmt.Errorf("%w: the agent seat holds no role", apperrors.ErrConflict)
	// A role key nobody defines is a 404 like a missing member, but it is a
	// DIFFERENT 404: the admin mistyped a role, not a person. Wrapping keeps
	// the status while letting the handler say which of the two happened.
	errUnknownRole = fmt.Errorf("%w: no role with this key is defined", apperrors.ErrNotFound)
)

// InviteUserInput carries the admin-supplied details for a new member. No
// password is set here — the invite issues a single-use set-password token.
type InviteUserInput struct {
	Email       string
	DisplayName string
	Role        string
	// TeamIDs are the teams the member joins on arrival, in the same
	// transaction as the seat and the role.
	TeamIDs []ids.UUID
}

// InviteUser provisions a new active member with the one target system role and
// no password, mints a single-use set-password token, and returns the raw token
// so the caller can deliver the invite link. Admin-only. The whole thing — the
// user row, the role grant, the token, the audit row and the user.invited event
// — commits in ONE transaction. A duplicate email answers ErrConflict.
func (s *Service) InviteUser(ctx context.Context, actor Identity, in InviteUserInput) (ids.UserID, string, error) {
	if !actor.hasRole(roleAdmin) {
		return ids.UserID{}, "", apperrors.ErrPermissionDenied
	}
	teams, err := validTeamIDs(in.TeamIDs)
	if err != nil {
		return ids.UserID{}, "", err
	}
	in.TeamIDs = teams
	raw, tokenHash, err := mintSessionToken()
	if err != nil {
		return ids.UserID{}, "", err
	}
	ctx = actorCtx(ctx, actor)
	var newUserID ids.UserID
	err = s.db.Tx(ctx, func(tx pgx.Tx) error {
		// An invited member is a full seat — the insert below takes the column's
		// default and there is no read-seat invite — so every invite is one more
		// seat against the licensed ceiling, and it is refused here rather than
		// after the member exists.
		if err := s.refuseWhenNoSeatIsLeft(ctx, tx); err != nil {
			return err
		}
		var roleID ids.UUID
		roleErr := tx.QueryRow(ctx, `SELECT id FROM role WHERE key = $1`, in.Role).Scan(&roleID)
		if errors.Is(roleErr, pgx.ErrNoRows) {
			return errUnknownRole
		}
		if roleErr != nil {
			return roleErr
		}
		insErr := tx.QueryRow(ctx,
			`INSERT INTO app_user (email, password_hash, display_name, status)
			 VALUES (lower($1), NULL, $2, 'active') RETURNING id`,
			in.Email, in.DisplayName).Scan(&newUserID)
		if storekit.IsUniqueViolation(insErr) {
			return errEmailTaken
		}
		if insErr != nil {
			return insErr
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO role_assignment (role_id, user_id) VALUES ($1, $2)`,
			roleID, newUserID); err != nil {
			return err
		}
		if err := joinTeamsTx(ctx, tx, newUserID.UUID, in.TeamIDs); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO auth_token (user_id, purpose, token_hash, expires_at)
			 VALUES ($1, 'password_reset', $2, now() + $3::interval)`,
			newUserID, tokenHash, inviteTokenTTL.String()); err != nil {
			return err
		}
		auditID, err := storekit.Audit(ctx, tx, "create", "user", newUserID.UUID,
			nil, map[string]any{"email": in.Email, "role": in.Role, "team_ids": in.TeamIDs, userAuditKeyStatus: userStatusActive})
		if err != nil {
			return err
		}
		return storekit.EmitEvent(ctx, tx, auditID, newUserID.UUID,
			userInvitedPayload(newUserID, in.Role, actor.UserID, in.TeamIDs))
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
	if !actor.hasRole(roleAdmin) {
		return apperrors.ErrPermissionDenied
	}
	ctx = actorCtx(ctx, actor)
	return s.db.Tx(ctx, func(tx pgx.Tx) error {
		var status, seat string
		err := tx.QueryRow(ctx,
			`SELECT status, seat_type FROM app_user WHERE id = $1 AND archived_at IS NULL FOR UPDATE`,
			userID).Scan(&status, &seat)
		if errors.Is(err, pgx.ErrNoRows) {
			return apperrors.ErrNotFound
		}
		if err != nil {
			return err
		}
		if status == userStatusActive {
			return nil
		}
		// Reactivation is the inverse of deactivation only — a 'suspended' member
		// is held for a different reason (e.g. lockout) and must not be silently
		// cleared by this path.
		if status != userStatusDeactivated {
			return errNotDeactivated
		}
		// A deactivated member counts against nothing, so returning one to active
		// takes a seat exactly as an invite does. Only a FULL one: read seats are
		// unlimited and never metered, and refusing a viewer their account back on
		// a full-seat ceiling would meter the one thing the license does not.
		if principal.SeatType(seat) == principal.SeatFull {
			if err := s.refuseWhenNoSeatIsLeft(ctx, tx); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx,
			`UPDATE app_user SET status = 'active' WHERE id = $1`, userID); err != nil {
			return err
		}
		auditID, err := storekit.Audit(ctx, tx, "update", "user", userID.UUID,
			map[string]any{userAuditKeyStatus: status}, map[string]any{userAuditKeyStatus: userStatusActive})
		if err != nil {
			return err
		}
		return storekit.EmitEvent(ctx, tx, auditID, userID.UUID,
			userReactivatedPayload(userID, actor.UserID))
	})
}

// userReactivatedPayload builds user.reactivated's typed payload.
func userReactivatedPayload(userID ids.UserID, by ids.UserID) crmcontracts.PublicEventUserReactivated {
	return crmcontracts.PublicEventUserReactivated{
		UserId: openapi_types.UUID(userID.UUID),
		By:     openapi_types.UUID(by.UUID),
	}
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

// DeactivateUser flips the user to 'deactivated' and hard-revokes
// everything that borrows their authority (revokeBorrowedAuthority) in
// ONE transaction with the audit row and the user.deactivated event
// (§5.6a: the cascade seam — per-call re-auth already refuses a
// deactivated principal, the event lets caches and fan-outs drop access
// without polling). Admin-only; idempotent on an already-deactivated
// user (no duplicate event).
func (s *Service) DeactivateUser(ctx context.Context, actor Identity, in DeactivateUserInput) error {
	if !actor.hasRole(roleAdmin) {
		return apperrors.ErrPermissionDenied
	}
	ctx = actorCtx(ctx, actor)
	return s.db.Tx(ctx, func(tx pgx.Tx) error {
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
		if status == userStatusDeactivated {
			return nil
		}
		// Never deactivate the last active admin — it would lock the whole
		// organization out of user administration with no recovery in-app.
		lastAdmin, err := lastActiveAdmin(ctx, tx, in.UserID)
		if err != nil {
			return err
		}
		if lastAdmin {
			return errLastActiveAdmin
		}
		if _, err := tx.Exec(ctx,
			`UPDATE app_user SET status = 'deactivated' WHERE id = $1`, in.UserID); err != nil {
			return err
		}
		if err := s.revokeBorrowedAuthority(ctx, tx, in.UserID); err != nil {
			return err
		}
		auditID, err := storekit.Audit(ctx, tx, "update", "user", in.UserID.UUID,
			map[string]any{userAuditKeyStatus: status}, map[string]any{userAuditKeyStatus: userStatusDeactivated})
		if err != nil {
			return err
		}
		return storekit.EmitEvent(ctx, tx, auditID, in.UserID.UUID,
			userDeactivatedPayload(in.UserID, actor.UserID, in.Reason))
	})
}

// revokeBorrowedAuthority ends everything that answers to this human rather
// than being owned by them, inside the caller's transaction and under the
// app_user lock it already holds: the credential cascade (endCredentialAuthority)
// plus the one piece of DEACTIVATION-specific fallout, their imported LinkedIn
// network.
//
// A departing colleague's imported LinkedIn network goes with them. It is
// THEIR address book — thousands of third parties who never agreed to be in
// this CRM and whose only tie to it was that one person's employment. Keeping
// it would leave the company holding a private contact list belonging to
// someone who no longer works here, long after the relationship that
// justified holding it ended. Deleted rather than tombstoned: a tombstone
// still holds the names. This step is deliberately NOT part of
// endCredentialAuthority: a password reset or an operator recovery ends the
// same credentials without the human leaving, and must not cost them their
// address book.
func (s *Service) revokeBorrowedAuthority(ctx context.Context, tx pgx.Tx, userID ids.UserID) error {
	if err := endCredentialAuthority(ctx, tx, userID, deactivatedUserRevokeReason); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `DELETE FROM linkedin_connection WHERE owner_user_id = $1`, userID)
	return err
}

// endCredentialAuthority is the credential cascade every path that ends a
// human's control of their account — deactivation, a password reset, an
// operator-driven recovery — must run, inside the caller's transaction. It
// is the whole cascade in ORDER, and the order is load-bearing at both ends.
//
// The OAuth connections go FIRST, through the one grant cascade
// (revokeGrantsOfUserTx). Revoking the human's passports alone would leave
// the grants alive, and a connector's next renewal mints a replacement while
// sliding its 90-day window forward — so a stolen session that planted a
// connection here would keep it after the credential that planted it is
// gone. The cascade also takes the grant → refresh → passport lock order
// (oauth_grant.go), and a bulk passport UPDATE ahead of it would take a
// passport lock first and deadlock against a rotation racing this call.
//
// The consent nobody redeemed yet ends next. An authorization code carries
// the lent scopes and the human's id, and redemption re-checks only that the
// human is live — so a code minted in the minutes before this call would
// still exchange for a passport afterward, on a consent given under
// authority that no longer exists. That is the same restoration the grant
// cascade above exists to prevent; a code is only shorter lived, not
// different in kind. The window is ended rather than the row marked consumed:
// nothing redeemed it, and a consumed_at would claim an exchange that never
// happened to everyone who reads the row afterwards.
//
// The locally minted passports go LAST, because they are what is left over:
// the A1 path, which answers to no grant.
//
// The actor must already be bound on ctx (actorCtx, or an equivalent minimal
// principal for a caller with no live Identity): the audit rows this writes
// and the passport.revoked event both name who ended the authority.
func endCredentialAuthority(ctx context.Context, tx pgx.Tx, userID ids.UserID, reason string) error {
	if err := revokeGrantsOfUserTx(ctx, tx, userID, reason); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`UPDATE oauth_authorization_code SET expires_at = now()
		  WHERE user_id = $1 AND consumed_at IS NULL AND expires_at > now()`,
		userID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`UPDATE session SET revoked_at = now() WHERE user_id = $1 AND revoked_at IS NULL`,
		userID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`UPDATE passport SET revoked_at = now() WHERE on_behalf_of = $1 AND revoked_at IS NULL`,
		userID); err != nil {
		return err
	}
	// An unredeemed set-password token is a credential like any other here: it
	// needs nothing but itself to set an arbitrary password on this account,
	// and the three writers that mint one (a reset request, an admin's
	// set-password link, an invite) all give it a multi-day life.
	//
	// Leaving it outstanding is the failure this whole function exists to
	// prevent, and it is worst on the path that reads most like safety: someone
	// who notices a reset mail they did not request, signs in, and rotates
	// their password would end every session and grant while the token that
	// prompted their alarm stayed live until its TTL.
	_, err := tx.Exec(ctx,
		`UPDATE auth_token SET used_at = now()
		  WHERE user_id = $1 AND purpose = 'password_reset' AND used_at IS NULL`,
		userID)
	return err
}

// userDeactivatedPayload builds user.deactivated's typed payload. reason
// rides the payload only when the operator supplied one.
func userDeactivatedPayload(userID ids.UserID, by ids.UserID, reason *string) crmcontracts.PublicEventUserDeactivated {
	return crmcontracts.PublicEventUserDeactivated{
		UserId: openapi_types.UUID(userID.UUID),
		By:     openapi_types.UUID(by.UUID),
		Reason: reason,
	}
}

// ChangeUserRole replaces the user's role assignments with the single
// target system role and emits role.changed (§5.6a: {user_id, from_role?,
// to_role, by}) so the effective-permission caches never serve a stale
// grant. from_role rides the payload only when the previous state was a
// single role — a multi-role history has no one "from". Admin-only.
func (s *Service) ChangeUserRole(ctx context.Context, actor Identity, userID ids.UserID, toRole string) error {
	if !actor.hasRole(roleAdmin) {
		return apperrors.ErrPermissionDenied
	}
	ctx = actorCtx(ctx, actor)
	return s.db.Tx(ctx, func(tx pgx.Tx) error {
		// The target is read rather than merely proved to exist, because what it
		// IS decides the answer: an agent seat holds no role at all.
		var isAgent bool
		targetErr := tx.QueryRow(ctx,
			`SELECT is_agent FROM app_user WHERE id = $1 AND archived_at IS NULL`,
			userID).Scan(&isAgent)
		if errors.Is(targetErr, pgx.ErrNoRows) {
			return apperrors.ErrNotFound
		}
		if targetErr != nil {
			return targetErr
		}
		if isAgent {
			return errAgentSeatHoldsNoRole
		}
		var roleID ids.UUID
		err := tx.QueryRow(ctx, `SELECT id FROM role WHERE key = $1`, toRole).Scan(&roleID)
		if errors.Is(err, pgx.ErrNoRows) {
			return errUnknownRole
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
		// Never demote the last active admin — the same lockout as deactivation.
		if toRole != roleAdmin {
			lastAdmin, err := lastActiveAdmin(ctx, tx, userID)
			if err != nil {
				return err
			}
			if lastAdmin {
				return errLastActiveAdmin
			}
		}

		if _, err := tx.Exec(ctx,
			`DELETE FROM role_assignment WHERE user_id = $1`, userID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO role_assignment (role_id, user_id) VALUES ($1, $2)`,
			roleID, userID); err != nil {
			return err
		}
		auditID, err := storekit.Audit(ctx, tx, "assign", "user", userID.UUID,
			map[string]any{"roles": fromRoles}, map[string]any{"roles": []string{toRole}})
		if err != nil {
			return err
		}
		var fromRole *string
		if len(fromRoles) == 1 {
			fromRole = &fromRoles[0]
		}
		return storekit.EmitEvent(ctx, tx, auditID, userID.UUID,
			roleChangedPayload(userID, toRole, actor.UserID, fromRole))
	})
}

// roleChangedPayload builds role.changed's typed payload. fromRole rides
// the payload only when the previous state was a single role — a
// multi-role history has no one "from".
func roleChangedPayload(userID ids.UserID, toRole string, by ids.UserID, fromRole *string) crmcontracts.PublicEventRoleChanged {
	return crmcontracts.PublicEventRoleChanged{
		UserId:   openapi_types.UUID(userID.UUID),
		ToRole:   toRole,
		By:       openapi_types.UUID(by.UUID),
		FromRole: fromRole,
	}
}
