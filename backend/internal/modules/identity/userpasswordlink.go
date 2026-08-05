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
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// auditVerbPasswordLinkIssued is the ledger verb for this operation. It is its
// own rather than an `update` on `user` because no app_user column changes —
// an `update` row would carry an empty before/after image and claim a record
// mutation that never happened.
const auditVerbPasswordLinkIssued = "password_link_issued"

// errMemberNotActive refuses a link for a member who is not active. Redemption
// updates only an active, non-archived account (see ResetPassword), so issuing
// here would hand the admin a link that is dead on arrival — recreating exactly
// the silent-failure this whole feature exists to remove.
var errMemberNotActive = errors.New("identity: the member is not active")

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
	// The seat ceiling sits ABOVE RBAC (A62/ADR-0047) and the HTTP middleware
	// already refuses a read seat every mutating method. It is re-checked here
	// because THIS is the gate the arch fitness waiver names as the authority:
	// a future non-HTTP caller must not be able to mint an account-takeover
	// credential on the strength of a role alone.
	if !principal.SeatType(actor.SeatType).CanMutate() {
		return "", time.Time{}, apperrors.ErrSeatTierInsufficient
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

// lockMemberForTokenIssue serializes every issuer of one member's set-password
// tokens — this path and the forgot-password mint — for the rest of the
// transaction.
//
// It is an ADVISORY lock rather than `SELECT … FOR UPDATE` on app_user, and the
// reason is deadlock avoidance rather than taste. Redemption locks the
// `auth_token` row first and then writes `app_user`; an issuer that locked
// `app_user` first and then wrote `auth_token` would take the same two locks in
// the opposite order, so a redeem racing an issue could each hold what the
// other waits for and one would die with `deadlock detected` — in the
// forgot-password case, silently, since that mint runs detached from the
// request. Holding no app_user row lock at all removes the cycle, and the
// advisory lock still serializes issuers against each other, which a row lock
// on a token that may not exist yet could never do.
func lockMemberForTokenIssue(ctx context.Context, tx pgx.Tx, userID ids.UserID) error {
	// The key is NAMESPACED, as every other advisory lock in the tree is
	// (`offer_number:`, `lead_routing:`, `customfields:`). pg_advisory_xact_lock
	// shares one 64-bit space across the whole database, so hashing a bare UUID
	// would rely on no other domain ever hashing one too — isolation by
	// coincidence rather than by design.
	_, err := tx.Exec(ctx,
		`SELECT pg_advisory_xact_lock(hashtextextended('set_password_token:' || $1::text, 0))`,
		userID.String())
	return err
}

// supersedeSetPasswordTokens refuses a non-active member and consumes their
// outstanding unused set-password tokens, reporting how many it consumed for
// the audit image.
//
// The member is read WITHOUT a row lock (see lockMemberForTokenIssue). A
// deactivation committing between this check and the insert would leave a token
// that redemption then refuses anyway, because redemption re-checks that the
// account is active — so the window costs a wasted link, never a usable one.
func supersedeSetPasswordTokens(ctx context.Context, tx pgx.Tx, userID ids.UserID) (int64, error) {
	if err := lockMemberForTokenIssue(ctx, tx, userID); err != nil {
		return 0, err
	}
	var status string
	err := tx.QueryRow(ctx,
		`SELECT status FROM app_user WHERE id = $1 AND archived_at IS NULL`,
		userID).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, apperrors.ErrNotFound
	}
	if err != nil {
		return 0, err
	}
	if status != userStatusActive {
		return 0, errMemberNotActive
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
