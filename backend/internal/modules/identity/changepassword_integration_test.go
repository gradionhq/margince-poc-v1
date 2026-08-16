// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package identity

// Changing your own password. The property worth holding is what the CURRENT
// password buys and what a session does not: a live session admits the request,
// and only the current password authorizes the change, so a borrowed browser
// cannot lock its owner out.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/values"
)

const newMemberPassword = "a replacement password!"

// memberCtx binds the member as the authenticated caller, which is what
// ChangePassword reads to know whose password it is rotating.
func memberCtx(t *testing.T, e *revocationEnv) context.Context {
	t.Helper()
	id, _, err := e.svc.Login(e.wsOnlyCtx(), e.member.Email, memberPassword)
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	return withIdentity(e.wsOnlyCtx(), id)
}

func TestChangePasswordRotatesTheCredential(t *testing.T) {
	e := setupRevocationEnv(t, "changepw-ok")
	ctx := memberCtx(t, e)

	if err := e.svc.ChangePassword(ctx, memberPassword, newMemberPassword); err != nil {
		t.Fatalf("change: %v", err)
	}
	// The new one works and the old one does not — a rotation that leaves the
	// old password live has rotated nothing.
	if _, _, err := e.svc.Login(e.wsOnlyCtx(), e.member.Email, newMemberPassword); err != nil {
		t.Fatalf("login with the new password: %v", err)
	}
	if _, _, err := e.svc.Login(e.wsOnlyCtx(), e.member.Email, memberPassword); err == nil {
		t.Fatal("the old password still logs in after the change")
	}
}

func TestChangePasswordNeedsTheCurrentPasswordNotJustASession(t *testing.T) {
	e := setupRevocationEnv(t, "changepw-wrong")
	ctx := memberCtx(t, e)

	err := e.svc.ChangePassword(ctx, "not-the-current-password", newMemberPassword)
	if !errors.Is(err, ErrCurrentPasswordWrong) {
		t.Fatalf("change with a wrong current password = %v, want ErrCurrentPasswordWrong", err)
	}
	// And nothing moved: a session alone must not be able to set a password,
	// or a stolen laptop becomes a permanent takeover.
	if _, _, err := e.svc.Login(e.wsOnlyCtx(), e.member.Email, memberPassword); err != nil {
		t.Fatalf("the original password stopped working after a refused change: %v", err)
	}
}

func TestChangePasswordRefusesTheSamePassword(t *testing.T) {
	e := setupRevocationEnv(t, "changepw-same")
	ctx := memberCtx(t, e)

	if err := e.svc.ChangePassword(ctx, memberPassword, memberPassword); !errors.Is(err, ErrPasswordUnchanged) {
		t.Fatalf("re-setting the same password = %v, want ErrPasswordUnchanged", err)
	}
}

func TestChangePasswordHoldsTheLengthFloor(t *testing.T) {
	e := setupRevocationEnv(t, "changepw-short")
	ctx := memberCtx(t, e)

	// Rune-counted, so a handful of multi-byte characters cannot clear a
	// byte-length floor while being far shorter than it intends.
	var parseErr *values.ParseError
	err := e.svc.ChangePassword(ctx, memberPassword, "🔑🔑🔑🔑")
	if !errors.As(err, &parseErr) || parseErr.Field != "new_password" || parseErr.Code != "length" {
		t.Fatalf("a four-rune password gave %v, want a new_password/length refusal — sixteen bytes must not clear a twelve-CHARACTER floor", err)
	}
	if _, _, err := e.svc.Login(e.wsOnlyCtx(), e.member.Email, memberPassword); err != nil {
		t.Fatalf("the original password stopped working after a refused change: %v", err)
	}
}

func TestChangePasswordEndsEverySessionIncludingItsOwn(t *testing.T) {
	e := setupRevocationEnv(t, "changepw-revoke")
	wsCtx := e.wsOnlyCtx()

	// Two live sessions: the one making the call, and one standing elsewhere.
	id, callerToken, err := e.svc.Login(wsCtx, e.member.Email, memberPassword)
	if err != nil {
		t.Fatal(err)
	}
	_, otherToken, err := e.svc.Login(wsCtx, e.member.Email, memberPassword)
	if err != nil {
		t.Fatal(err)
	}

	// Both must work first: without this, a change that revoked nothing would
	// still satisfy the assertions below.
	for name, token := range map[string]string{"caller": callerToken, "other": otherToken} {
		if _, err := e.svc.Authenticate(wsCtx, token); err != nil {
			t.Fatalf("the %s session did not authenticate before the change: %v", name, err)
		}
	}

	if err := e.svc.ChangePassword(withIdentity(wsCtx, id), memberPassword, newMemberPassword); err != nil {
		t.Fatalf("change: %v", err)
	}
	// Both, not just the other one. A carve-out for "this browser" is a
	// carve-out for whoever is sitting at it.
	for name, token := range map[string]string{"caller": callerToken, "other": otherToken} {
		if _, err := e.svc.Authenticate(wsCtx, token); !errors.Is(err, apperrors.ErrNotFound) {
			t.Errorf("the %s session survived the password change (err = %v)", name, err)
		}
	}
}

func TestChangePasswordIsAudited(t *testing.T) {
	e := setupRevocationEnv(t, "changepw-audit")
	ctx := memberCtx(t, e)

	if err := e.svc.ChangePassword(ctx, memberPassword, newMemberPassword); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := database.WithInfraTx(context.Background(), e.svc.db.Pool(), func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT count(*) FROM system_log
			  WHERE action = 'password_changed' AND actor_id = $1`,
			"human:"+e.member.UserID.String()).Scan(&n)
	}); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("password_changed rows = %d, want 1 — a credential rotation with no record is a rotation nobody can review", n)
	}
}

func TestChangePasswordRefusesACallerWithNoUserBehindIt(t *testing.T) {
	e := setupRevocationEnv(t, "changepw-noidentity")
	// A workspace-bound context with no authenticated identity: an agent seat
	// or a system principal has no own password to change.
	err := e.svc.ChangePassword(e.wsOnlyCtx(), memberPassword, newMemberPassword)
	if !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Fatalf("change with no bound identity = %v, want ErrPermissionDenied", err)
	}
}

// The defences the login path already has for this same secret. Without them
// this route is a guessing oracle behind any borrowed session — unthrottled,
// uncounted, and leaving nothing in the trail.

func TestAWrongCurrentPasswordCountsTowardTheLockout(t *testing.T) {
	e := setupRevocationEnv(t, "changepw-lockout")
	ctx := memberCtx(t, e)

	for range 5 {
		if err := e.svc.ChangePassword(ctx, "wrong", newMemberPassword); !errors.Is(err, ErrCurrentPasswordWrong) {
			t.Fatalf("guess = %v, want ErrCurrentPasswordWrong", err)
		}
	}
	// The §27 lock now binds HERE, not only on the login route: the same secret
	// behind a different door must not stay open.
	if err := e.svc.ChangePassword(ctx, memberPassword, newMemberPassword); !errors.Is(err, errAccountLocked) {
		t.Fatalf("change while locked = %v, want errAccountLocked — the correct password got through a lockout", err)
	}
}

func TestAFailedChangeLeavesEvidence(t *testing.T) {
	e := setupRevocationEnv(t, "changepw-evidence")
	ctx := memberCtx(t, e)

	if err := e.svc.ChangePassword(ctx, "wrong", newMemberPassword); !errors.Is(err, ErrCurrentPasswordWrong) {
		t.Fatal(err)
	}
	var n int
	if err := database.WithInfraTx(context.Background(), e.svc.db.Pool(), func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT count(*) FROM system_log
			  WHERE action = 'password_change_failed' AND actor_id = $1`,
			"human:"+e.member.UserID.String()).Scan(&n)
	}); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("password_change_failed rows = %d, want 1 — an invisible brute force is exactly what the trail exists to catch", n)
	}
}

func TestChangePasswordRetiresAnOutstandingResetToken(t *testing.T) {
	e := setupRevocationEnv(t, "changepw-token")
	ctx := memberCtx(t, e)

	// The shape that makes this matter: someone else requested a reset for this
	// account and holds the token. The member notices, signs in, and rotates
	// their password — which the product tells them ends every credential.
	// Minted by the real writer, so this proves something about the token the
	// product actually issues rather than about a row shaped like one.
	if _, _, err := e.svc.IssuePasswordLink(e.wsCtx(e.admin), e.admin, e.member.UserID); err != nil {
		t.Fatalf("issuing the set-password link: %v", err)
	}

	if err := e.svc.ChangePassword(ctx, memberPassword, newMemberPassword); err != nil {
		t.Fatal(err)
	}

	var live int
	if err := database.WithInfraTx(context.Background(), e.svc.db.Pool(), func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT count(*) FROM auth_token
			  WHERE user_id = $1 AND purpose = 'password_reset' AND used_at IS NULL`,
			e.member.UserID).Scan(&live)
	}); err != nil {
		t.Fatal(err)
	}
	if live != 0 {
		t.Errorf("%d reset token(s) survived the change — whoever holds one can still take the account, after the member was told every credential was revoked", live)
	}
}

// The handler, end to end against a real database. The unit lane proves the
// refusals that happen before any query; these are the answers a client
// actually receives, including the one that only exists on the success path.

func changeOverHTTP(ctx context.Context, t *testing.T, e *revocationEnv, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	NewHandlers(e.svc).ChangePassword(rec,
		httptest.NewRequest(http.MethodPost, "/v1/auth/change-password",
			strings.NewReader(body)).WithContext(ctx))
	return rec
}

func TestChangePasswordOverHTTPClearsTheSessionCookie(t *testing.T) {
	e := setupRevocationEnv(t, "changepw-http-ok")
	ctx := memberCtx(t, e)

	rec := changeOverHTTP(ctx, t, e,
		`{"current_password":"`+memberPassword+`","new_password":"`+newMemberPassword+`"}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204: %s", rec.Code, rec.Body)
	}
	// The session it was made with is gone, so leaving the cookie would hand
	// the browser a token that authenticates nothing — a broken session rather
	// than a completed rotation.
	var cleared bool
	for _, c := range rec.Result().Cookies() {
		if c.Name == SessionCookieName && c.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Error("the session cookie was not cleared after a successful change")
	}
}

func TestChangePasswordOverHTTPSeparatesItsRefusals(t *testing.T) {
	e := setupRevocationEnv(t, "changepw-http-codes")
	ctx := memberCtx(t, e)

	// A wrong current password and a new password equal to the current one are
	// different mistakes with different fixes; a client that cannot tell them
	// apart sends the person to retype the wrong field.
	wrong := changeOverHTTP(ctx, t, e,
		`{"current_password":"not-it","new_password":"`+newMemberPassword+`"}`)
	if wrong.Code != http.StatusUnauthorized {
		t.Fatalf("wrong current password = %d, want 401: %s", wrong.Code, wrong.Body)
	}
	if code := problemCode(t, wrong); code != "current_password_invalid" {
		t.Errorf("wrong current password carries code %q, want current_password_invalid", code)
	}

	same := changeOverHTTP(ctx, t, e,
		`{"current_password":"`+memberPassword+`","new_password":"`+memberPassword+`"}`)
	if same.Code != http.StatusUnprocessableEntity {
		t.Errorf("re-setting the same password = %d, want 422: %s", same.Code, same.Body)
	}
}

func TestChangePasswordOverHTTPReportsALockedAccount(t *testing.T) {
	e := setupRevocationEnv(t, "changepw-http-locked")
	ctx := memberCtx(t, e)

	// Five wrong guesses through the real path, which is what folds the §27
	// counter — no hand-run recorder.
	for range 5 {
		if rec := changeOverHTTP(ctx, t, e,
			`{"current_password":"not-it","new_password":"`+newMemberPassword+`"}`); rec.Code != http.StatusUnauthorized {
			t.Fatalf("guess = %d, want 401", rec.Code)
		}
	}
	rec := changeOverHTTP(ctx, t, e,
		`{"current_password":"`+memberPassword+`","new_password":"`+newMemberPassword+`"}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("change while locked = %d, want 401: %s", rec.Code, rec.Body)
	}
	if code := problemCode(t, rec); code != "account_locked" {
		t.Errorf("a locked account carries code %q, want account_locked — the caller's remedy is to wait, not to retype", code)
	}
}
