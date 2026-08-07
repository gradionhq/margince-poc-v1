// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

// Account recovery (A74/ADR-0056, UI-gated by the A107 capabilities
// probe): the forgot/reset password pair. Enumeration-resistant end to
// end — the request always answers 202, and an invalid, used, or expired
// token is one neutral refusal.
//
// The two halves are gated separately, because they are different
// capabilities (ADR-0061 Amendment 1). ASKING for a reset needs the
// outbound-email channel: without a mailer there is nothing to send, so
// RequestPasswordReset answers 501 and the capabilities probe reports
// password_reset=false — the login UI never renders a self-service link
// this flow cannot honor. REDEEMING a token the holder already has needs
// only that some channel could have delivered it, so ResetPassword also
// serves an installation whose only channel is the admin-issued
// set-password link.
//
// A raw token appears in exactly two places and nowhere else: the reset
// mail, and the one response that hands an admin a link to pass on.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/identity/internal/password"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/platform/httpserver"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/values"
)

// resetTokenTTL is the reset link's lifetime — short, because the token
// is a live credential in an inbox (AUTH-DDL-1: reset ~1h).
const resetTokenTTL = time.Hour

// inviteTokenTTL is the set-password link's lifetime for a new member — longer
// than a reset because an invited person has no account yet and may take a few
// days to act on the mail.
const inviteTokenTTL = 7 * 24 * time.Hour

// RequestPasswordReset implements (POST /auth/forgot-password): mint a
// single-use token and email its link. Always 202 — the response never
// discloses whether the address maps to an account.
func (h Handlers) RequestPasswordReset(w http.ResponseWriter, r *http.Request) {
	// Both halves, not just the mailer: sending a link built on an empty base
	// would mint a live token and mail an unusable URL, consuming the one
	// recovery attempt the owner gets. The capabilities probe answers from this
	// same predicate, so the login UI never offers what this would refuse.
	if !h.canSendPasswordLink() {
		httperr.NotImplemented(w, r, "RequestPasswordReset")
		return
	}
	// Throttle FIRST — before any parsing or work, so a malformed flood
	// costs the same as a well-formed one. Per (email, IP) so an attacker
	// cannot silence a real owner's reset from elsewhere, plus a per-IP
	// ceiling — each attempt can cost the operator an outbound mail.
	var req struct {
		Email string `json:"email"`
	}
	if !h.resetPerIP.Allow(httpserver.ClientIP(r)) {
		httperr.Write(w, r, apperrors.ErrBudgetExceeded)
		return
	}
	if !httperr.Decode(w, r, &req) {
		return
	}
	email, err := values.ParseEmail(req.Email)
	if err != nil {
		httperr.Write(w, r, httperr.Validation("email", "invalid_email", "a valid email address is required"))
		return
	}
	if !h.resetPerEmail.Allow(strings.ToLower(email.String()) + "|" + httpserver.ClientIP(r)) {
		httperr.Write(w, r, apperrors.ErrBudgetExceeded)
		return
	}

	// EVERYTHING account-dependent runs off the request path — lookup,
	// token mint, and the SMTP round-trip alike. The 202 leaves before
	// any of it, so neither the response body nor its timing can
	// disclose whether the address maps to an account. Failures on the
	// async path are operator incidents, logged — never a different
	// answer to the caller.
	workCtx := context.WithoutCancel(r.Context())
	done := h.resetSendStarted // test seam; nil in production
	go func() {
		if done != nil {
			defer done()
		}
		// This goroutine OUTLIVES the request, so the chassis's recovery
		// middleware — which wraps the handler — cannot see a panic in here, and an
		// unrecovered panic in any goroutine takes the whole process down. The
		// endpoint is unauthenticated, which is what makes that unacceptable rather
		// than merely untidy: it would be a one-request denial of service for
		// anybody who could reach a panicking path. Nothing below panics today; the
		// point is that a future edit here must not be able to.
		defer func() {
			if panicked := recover(); panicked != nil {
				// The stack, not just the panic value: this runs off the
				// request goroutine, so there is no request log, trace, or
				// stack frame anywhere else an operator could use to find the
				// failing call site. Never returned to a client — this
				// handler already left with its 202 before this goroutine
				// started.
				slog.Error("password-reset send panicked", "panic", panicked, "stack", string(debug.Stack()))
			}
		}()
		rawToken, err := h.svc.CreatePasswordReset(workCtx, email.String())
		if err != nil {
			slog.Error("password-reset token mint failed", "err", err)
			return
		}
		if rawToken == "" {
			return
		}
		link := passwordLink(h.passwordLinkBaseURL, rawToken)
		body := "Someone requested a password reset for your Margince account.\n\n" +
			"Reset your password within one hour:\n\n  " + link + "\n\n" +
			"If this wasn't you, ignore this email — your password is unchanged."
		if err := h.resetMailer.Send(workCtx, email.String(), "Reset your Margince password", body); err != nil {
			slog.Error("password-reset email failed", "err", err)
		}
	}()
	w.WriteHeader(http.StatusAccepted)
}

// ResetPassword implements (POST /auth/reset-password): redeem the
// single-use token, set the new password, and revoke every session of
// the account.
// Redemption carries NO delivery-configuration gate, and that is the whole
// correction (ADR-0061 Amendment 1). Asking for a token by email needs a
// mailer; redeeming one you already hold needs only the token, whose
// possession IS the authority. Gating this on the mailer made an
// admin-issued link unredeemable on exactly the installations it exists for.
// Gating it on any current configuration would be the same mistake one step
// removed: a token lives seven days, so an operator who changes the mail or
// base-URL settings in that window would strand a credential already handed
// to a human. A token nobody could have been given simply never verifies.
func (h Handlers) ResetPassword(w http.ResponseWriter, r *http.Request) {
	if !h.resetPerIP.Allow(httpserver.ClientIP(r)) {
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
		// Serialize against every other issuer of this member's set-password
		// tokens — a concurrent forgot-password, or an admin issuing a link.
		// Without it, two transactions at READ COMMITTED each miss the other's
		// uncommitted insert and both leave a live token, so "one outstanding
		// token" would hold only when nobody raced.
		if err := lockMemberForTokenIssue(ctx, tx, userID); err != nil {
			return err
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
		// brute-force streak. Zero rows means the account was archived or
		// deactivated after the token was issued — the reset must refuse
		// (same neutral answer), never consume the token around an
		// unchanged password.
		tag, err := tx.Exec(ctx,
			`UPDATE app_user SET password_hash = $2, failed_login_count = 0, locked_until = NULL
			 WHERE id = $1 AND status = 'active' AND archived_at IS NULL`, userID, hash)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return apperrors.ErrNotFound
		}
		if _, err := tx.Exec(ctx,
			`UPDATE auth_token SET used_at = now() WHERE id = $1`, tokenID); err != nil {
			return err
		}
		// A completed reset ends every credential that could act as the
		// account, not just the session cookie that may have been stolen:
		// OAuth grants (and their refresh chains), unconsumed authorization
		// codes, sessions, and locally minted passports. Possession of the
		// token IS the authority here (see the doc comment above), so the
		// cascade is attributed to the account owner themselves — there is
		// no admin actor on this call the way DeactivateUser has one.
		if err := endCredentialAuthority(passwordOwnerCtx(ctx, userID), tx, userID, passwordResetRevokeReason); err != nil {
			return err
		}
		return logAuthEvent(ctx, tx, wsID, userID, "password_reset", "password reset completed; every borrowed credential revoked")
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
// connection and the workspace GUC); ends every credential that could
// still act as the account and writes the system_log evidence with an
// operator provenance. Never exposed over HTTP.
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
	// Same cascade the HTTP redemption runs: an operator recovering a
	// locked-out or compromised account must end every credential that
	// could still act as it, not just the session.
	if err := endCredentialAuthority(operatorCtx(ctx, wsID, userID), tx, userID, operatorResetRevokeReason); err != nil {
		return err
	}
	_, err = tx.Exec(ctx,
		`INSERT INTO system_log (workspace_id, actor_type, actor_id, action, detail)
		 VALUES ($1, 'system', 'operator-cli', 'password_reset', jsonb_build_object('detail', 'operator password reset; every borrowed credential revoked', 'user_id', $2::text))`,
		wsID, userID.String())
	return err
}

// passwordResetRevokeReason and operatorResetRevokeReason are what the
// oauth_grant audit rows say when a connection died because the human
// recovered account control, rather than an admin ending it for them.
const (
	passwordResetRevokeReason = "the account was recovered via password reset"
	operatorResetRevokeReason = "the account was recovered via an operator-issued password reset"
)

// passwordOwnerCtx binds the account owner as the storekit actor for the
// credential cascade a self-service reset triggers. There is no session or
// resolved Identity on this call — the redeemed token IS the authority
// (ResetPassword's doc comment) — so this carries only what the audit trail
// and the passport.revoked event need: which human it was.
func passwordOwnerCtx(ctx context.Context, userID ids.UserID) context.Context {
	return principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:" + userID.String(), UserID: userID.UUID,
	})
}

// operatorCtx binds the operator-cli system actor, the target workspace, and
// a correlation id for the credential cascade OperatorResetPassword
// triggers. The caller's tx already runs under the workspace GUC (cmd/migrate
// sets it); the workspace binding here only supplies what
// storekit.AuditWithEvidence reads off ctx rather than the GUC — the wsID
// column value and the actor attribution — mirroring the system_log row this
// function already writes by hand. The correlation id is not optional: if
// the reset user holds a live OAuth grant, the cascade's passport.revoked
// events go through storekit.Emit, which refuses to stage an event with none
// bound — and cmd/migrate's bare command context carries no operation scope
// of its own the way an HTTP request or a bus consumer would.
func operatorCtx(ctx context.Context, wsID ids.WorkspaceID, userID ids.UserID) context.Context {
	ctx = principal.WithWorkspaceID(ctx, wsID.UUID)
	ctx = principal.WithCorrelationID(ctx, ids.NewV7())
	return principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalSystem, ID: "operator-cli", UserID: userID.UUID,
	})
}
