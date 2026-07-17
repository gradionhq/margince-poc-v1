// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

// Account recovery (A74/ADR-0056, UI-gated by the A107 capabilities
// probe): the forgot/reset password pair over the operator's
// transactional-email channel. Enumeration-resistant end to end — the
// request always answers 202, an invalid, used, or expired token is one
// neutral refusal, and the reset email is the only place the raw token
// ever appears. The surface exists only when a mailer is wired; without
// one both operations answer their explicit 501 and the capabilities
// probe reports password_reset=false, so the login UI never renders a
// link this flow cannot honor.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/identity/internal/password"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/values"
)

// resetTokenTTL is the reset link's lifetime — short, because the token
// is a live credential in an inbox (AUTH-DDL-1: reset ~1h).
const resetTokenTTL = time.Hour

// RequestPasswordReset implements (POST /auth/forgot-password): mint a
// single-use token and email its link. Always 202 — the response never
// discloses whether the address maps to an account.
func (h Handlers) RequestPasswordReset(w http.ResponseWriter, r *http.Request) {
	if h.resetMailer == nil {
		httperr.NotImplemented(w, r, "RequestPasswordReset")
		return
	}
	var req struct {
		Email string `json:"email"`
	}
	if !httperr.Decode(w, r, &req) {
		return
	}
	email, err := values.ParseEmail(req.Email)
	if err != nil {
		httperr.Write(w, r, httperr.Validation("email", "invalid_email", "a valid email address is required"))
		return
	}
	// The throttle mirrors login's shape: per (email, IP) so an attacker
	// cannot silence a real owner's reset from elsewhere, plus a per-IP
	// ceiling — each attempt can cost the operator an outbound mail.
	accountKey := strings.ToLower(email.String()) + "|" + clientIP(r)
	if !h.resetPerIP.Allow(clientIP(r)) || !h.resetPerEmail.Allow(accountKey) {
		httperr.Write(w, r, apperrors.ErrBudgetExceeded)
		return
	}

	rawToken, err := h.svc.CreatePasswordReset(r.Context(), email.String())
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	if rawToken != "" {
		// The mail leaves AFTER the token committed, outside the
		// transaction. A relay failure is an operator incident, logged —
		// but the response stays 202: answering differently would
		// disclose that the address exists.
		link := h.resetBaseURL + "/reset-password?token=" + rawToken
		body := "Someone requested a password reset for your Margince account.\n\n" +
			"Reset your password within one hour:\n\n  " + link + "\n\n" +
			"If this wasn't you, ignore this email — your password is unchanged."
		if err := h.resetMailer.Send(r.Context(), email.String(), "Reset your Margince password", body); err != nil {
			slog.Error("password-reset email failed", "err", err)
		}
	}
	w.WriteHeader(http.StatusAccepted)
}

// ResetPassword implements (POST /auth/reset-password): redeem the
// single-use token, set the new password, and revoke every session of
// the account.
func (h Handlers) ResetPassword(w http.ResponseWriter, r *http.Request) {
	if h.resetMailer == nil {
		httperr.NotImplemented(w, r, "ResetPassword")
		return
	}
	if !h.resetPerIP.Allow(clientIP(r)) {
		httperr.Write(w, r, apperrors.ErrBudgetExceeded)
		return
	}
	var req struct {
		Token       string `json:"token"`
		NewPassword string `json:"new_password"`
	}
	if !httperr.Decode(w, r, &req) {
		return
	}
	if req.Token == "" {
		httperr.Write(w, r, httperr.Validation("token", "required", "the reset token is required"))
		return
	}
	if len(req.NewPassword) < 12 || len(req.NewPassword) > 256 {
		httperr.Write(w, r, httperr.Validation("new_password", "length", "the new password must be 12–256 characters"))
		return
	}

	err := h.svc.RedeemPasswordReset(r.Context(), req.Token, req.NewPassword)
	if errors.Is(err, apperrors.ErrNotFound) {
		// One neutral refusal for unknown, used, and expired alike — the
		// distinction would let a token be probed.
		httperr.Unauthorized(w, r, "invalid, used, or expired reset token")
		return
	}
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// CreatePasswordReset mints a reset token for the address when it maps
// to an active account, invalidating any outstanding reset first. The
// empty return means "no account" — the caller must behave identically
// either way (enumeration resistance); only the presence of an email in
// an inbox may differ.
func (s *Service) CreatePasswordReset(ctx context.Context, email string) (string, error) {
	wsID, ok := workspaceFrom(ctx)
	if !ok {
		// Pre-bootstrap there is no account to reset; the neutral no-op
		// answer is the same one an unknown address gets.
		return "", nil
	}
	raw, tokenHash, err := mintSessionToken()
	if err != nil {
		return "", err
	}

	minted := false
	err = database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		var userID ids.UserID
		lookupErr := tx.QueryRow(ctx,
			`SELECT id FROM app_user
			 WHERE email = lower($1) AND status = 'active' AND archived_at IS NULL AND password_hash IS NOT NULL`,
			email).Scan(&userID)
		if errors.Is(lookupErr, pgx.ErrNoRows) {
			return nil
		}
		if lookupErr != nil {
			return lookupErr
		}
		// One outstanding reset per account: a new request supersedes any
		// earlier unredeemed token.
		if _, err := tx.Exec(ctx,
			`UPDATE auth_token SET used_at = now()
			 WHERE user_id = $1 AND purpose = 'password_reset' AND used_at IS NULL`, userID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO auth_token (workspace_id, user_id, purpose, token_hash, expires_at)
			 VALUES ($1, $2, 'password_reset', $3, now() + $4::interval)`,
			wsID, userID, tokenHash, resetTokenTTL.String()); err != nil {
			return err
		}
		minted = true
		return logAuthEvent(ctx, tx, wsID, userID, "password_reset_requested", "reset token issued")
	})
	if err != nil || !minted {
		return "", err
	}
	return raw, nil
}

// RedeemPasswordReset validates the single-use token, sets the new
// password, consumes the token, and revokes every live session of the
// account. Unknown, used, and expired tokens all answer
// apperrors.ErrNotFound — the caller writes one neutral refusal.
func (s *Service) RedeemPasswordReset(ctx context.Context, rawToken, newPassword string) error {
	wsID, ok := workspaceFrom(ctx)
	if !ok {
		return apperrors.ErrNotFound
	}
	hash, err := password.Hash(newPassword)
	if err != nil {
		return err
	}
	return database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		var tokenID ids.UUID
		var userID ids.UserID
		lookupErr := tx.QueryRow(ctx,
			`SELECT id, user_id FROM auth_token
			 WHERE token_hash = $1 AND purpose = 'password_reset'
			   AND used_at IS NULL AND now() < expires_at
			 FOR UPDATE`,
			hashToken(rawToken)).Scan(&tokenID, &userID)
		if errors.Is(lookupErr, pgx.ErrNoRows) {
			return apperrors.ErrNotFound
		}
		if lookupErr != nil {
			return lookupErr
		}
		// The reset also clears the §27 lockout state: the account owner
		// just proved control of the mailbox, which outranks a stale
		// brute-force streak.
		if _, err := tx.Exec(ctx,
			`UPDATE app_user SET password_hash = $2, failed_login_count = 0, locked_until = NULL
			 WHERE id = $1 AND status = 'active' AND archived_at IS NULL`, userID, hash); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`UPDATE auth_token SET used_at = now() WHERE id = $1`, tokenID); err != nil {
			return err
		}
		// A completed reset ends every existing session: whoever held a
		// stolen cookie is out the moment the owner recovers the account.
		if _, err := tx.Exec(ctx,
			`UPDATE session SET revoked_at = now() WHERE user_id = $1 AND revoked_at IS NULL`, userID); err != nil {
			return err
		}
		return logAuthEvent(ctx, tx, wsID, userID, "password_reset", "password reset completed; sessions revoked")
	})
}

// workspaceFrom narrows the context's workspace binding to the typed id
// the reset SQL needs.
func workspaceFrom(ctx context.Context) (ids.WorkspaceID, bool) {
	raw, ok := principal.WorkspaceID(ctx)
	if !ok {
		return ids.WorkspaceID{}, false
	}
	return ids.From[ids.WorkspaceKind](raw), true
}

// OperatorResetPassword is the operator-only recovery path (A107/ADR-0061
// §9.1): reset a named user's password directly against the database —
// for installations without outbound email and for administrator
// lockout. Runs in the caller's transaction (the operator CLI owns the
// connection and the workspace GUC); revokes every session and writes
// the system_log evidence with an operator provenance. Never exposed
// over HTTP.
func OperatorResetPassword(ctx context.Context, tx pgx.Tx, wsID ids.WorkspaceID, email, newPassword string) error {
	if len(newPassword) < 12 {
		return errors.New("identity: the new password must be at least 12 characters")
	}
	hash, err := password.Hash(newPassword)
	if err != nil {
		return err
	}
	var userID ids.UserID
	lookupErr := tx.QueryRow(ctx,
		`SELECT id FROM app_user WHERE email = lower($1) AND archived_at IS NULL`, email).Scan(&userID)
	if errors.Is(lookupErr, pgx.ErrNoRows) {
		return fmt.Errorf("identity: no user with email %q", email)
	}
	if lookupErr != nil {
		return lookupErr
	}
	if _, err := tx.Exec(ctx,
		`UPDATE app_user SET password_hash = $2, failed_login_count = 0, locked_until = NULL
		 WHERE id = $1`, userID, hash); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`UPDATE session SET revoked_at = now() WHERE user_id = $1 AND revoked_at IS NULL`, userID); err != nil {
		return err
	}
	_, err = tx.Exec(ctx,
		`INSERT INTO system_log (workspace_id, actor_type, actor_id, action, detail)
		 VALUES ($1, 'system', 'operator-cli', 'password_reset', jsonb_build_object('detail', 'operator password reset; sessions revoked', 'user_id', $2::text))`,
		wsID, userID.String())
	return err
}
