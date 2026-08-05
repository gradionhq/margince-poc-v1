// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

// The admin-issued set-password link (ADR-0061 Amendment 1): the provisioning
// path for an installation with no outbound-email channel, where the invite
// mail cannot be sent and self-service recovery is unavailable, so a new member
// would otherwise be created active and permanently unable to sign in.
//
// The token is the same one the invite mints — same table, same
// `password_reset` purpose, same seven-day TTL, redeemed by the same
// ResetPassword path. Only its delivery differs: the admin receives it once,
// over the response body, and hands it to the member out of band.
//
// A link can be minted for a member who ALREADY has a password, which makes
// this an account-takeover-capable operation. That is deliberate — an admin can
// already re-role and deactivate anyone, so they are the trust boundary — but
// it is why the audit row is not incidental bookkeeping here: it is the control.

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// auditVerbPasswordLinkIssued is the ledger verb for this operation. It is its
// own rather than an `update` on `user` because no app_user column changes —
// an `update` row would carry an empty before/after image and claim a record
// mutation that never happened.
const auditVerbPasswordLinkIssued = "password_link_issued"

// ErrMemberNotActive refuses a link for a member who is not active. Redemption
// updates only an active, non-archived account (see ResetPassword), so issuing
// here would hand the admin a link that is dead on arrival — recreating exactly
// the silent-failure this whole feature exists to remove.
var ErrMemberNotActive = errors.New("identity: the member is not active")

// IssuePasswordLink mints a single-use set-password token for a member and
// returns the raw token with its expiry, for the caller to render as a link.
// Admin-only.
//
// Issuing SUPERSEDES the member's outstanding unused tokens, so at most one is
// ever live — the same rule the reset path applies. Everything commits in ONE
// transaction: the supersede, the new token, the audit row and the
// user.password_link_issued event.
//
// The raw token is returned and never stored, logged, or carried in the audit
// image or the event payload. Losing it means issuing another, never recovering
// this one.
func (s *Service) IssuePasswordLink(ctx context.Context, actor Identity, userID ids.UserID) (string, time.Time, error) {
	if !actor.hasRole(roleAdmin) {
		return "", time.Time{}, apperrors.ErrPermissionDenied
	}
	wsID, ok := workspaceFrom(ctx)
	if !ok {
		return "", time.Time{}, apperrors.ErrNotFound
	}
	raw, tokenHash, err := mintSessionToken()
	if err != nil {
		return "", time.Time{}, err
	}
	ctx = actorCtx(ctx, actor)
	var expiresAt time.Time
	err = database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		superseded, err := supersedeSetPasswordTokens(ctx, tx, userID)
		if err != nil {
			return err
		}
		if err := tx.QueryRow(ctx,
			`INSERT INTO auth_token (workspace_id, user_id, purpose, token_hash, expires_at)
			 VALUES ($1, $2, 'password_reset', $3, now() + $4::interval)
			 RETURNING expires_at`,
			wsID, userID, tokenHash, inviteTokenTTL.String()).Scan(&expiresAt); err != nil {
			return err
		}
		auditID, err := storekit.Audit(ctx, tx, auditVerbPasswordLinkIssued, "user", userID.UUID,
			nil, map[string]any{"expires_at": expiresAt, "superseded_tokens": superseded})
		if err != nil {
			return err
		}
		return storekit.EmitEvent(ctx, tx, auditID, userID.UUID,
			passwordLinkIssuedPayload(userID, actor.UserID, expiresAt))
	})
	if err != nil {
		return "", time.Time{}, err
	}
	return raw, expiresAt, nil
}

// supersedeSetPasswordTokens locks the target member, refuses a non-active one,
// and consumes their outstanding unused set-password tokens. It reports how
// many it consumed, which the audit image records.
//
// The lock is the point of doing this in one place: without FOR UPDATE, two
// admins issuing concurrently can each miss the other's uncommitted insert at
// READ COMMITTED and both leave a live token, quietly breaking the
// one-outstanding-token rule. It also serializes the status check against a
// concurrent deactivation.
func supersedeSetPasswordTokens(ctx context.Context, tx pgx.Tx, userID ids.UserID) (int64, error) {
	var status string
	err := tx.QueryRow(ctx,
		`SELECT status FROM app_user WHERE id = $1 AND archived_at IS NULL FOR UPDATE`,
		userID).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, apperrors.ErrNotFound
	}
	if err != nil {
		return 0, err
	}
	if status != userStatusActive {
		return 0, ErrMemberNotActive
	}
	tag, err := tx.Exec(ctx,
		`UPDATE auth_token SET used_at = now()
		 WHERE user_id = $1 AND purpose = 'password_reset' AND used_at IS NULL`, userID)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// passwordLinkIssuedPayload builds user.password_link_issued's typed payload.
// It names actor, target and expiry — and deliberately no token: the event
// reaches the bus, and a credential must not.
func passwordLinkIssuedPayload(userID, by ids.UserID, expiresAt time.Time) crmcontracts.PublicEventUserPasswordLinkIssued {
	return crmcontracts.PublicEventUserPasswordLinkIssued{
		UserId:    openapi_types.UUID(userID.UUID),
		By:        openapi_types.UUID(by.UUID),
		ExpiresAt: expiresAt,
	}
}
