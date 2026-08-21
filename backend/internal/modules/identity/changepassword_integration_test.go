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
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/values"
)

const newMemberPassword = "a replacement password!"

// newAdminPassword is what the flagged admin chooses for themselves — the act
// that is supposed to end the forced rotation.
const newAdminPassword = "the password I picked!"

// memberCtx binds the member as the authenticated caller, which is what
// ChangePassword reads to know whose password it is rotating.
func memberCtx(t *testing.T, e *revocationEnv) context.Context {
	t.Helper()
	return loginCtx(t, e, e.member.Email, memberPassword)
}

// adminCtx binds the bootstrap admin — the account a configured installation
// flags — as the authenticated caller.
func adminCtx(t *testing.T, e *revocationEnv) context.Context {
	t.Helper()
	return loginCtx(t, e, e.admin.Email, bootstrapPassword)
}

func loginCtx(t *testing.T, e *revocationEnv, email, pw string) context.Context {
	t.Helper()
	id, _, err := e.svc.Login(e.wsOnlyCtx(), email, pw)
	if err != nil {
		t.Fatalf("login as %s: %v", email, err)
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

// The forced rotation. The flag exists to answer one question — is this
// account still using a password somebody else chose — so what matters is that
// it is set on exactly one path, cleared by exactly one act, and enforced
// against a client that ignores it.

func mustChangeFor(t *testing.T, svc *Service, userID ids.UserID) bool {
	t.Helper()
	var forced bool
	if err := database.WithInfraTx(context.Background(), svc.db.Pool(), func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT must_change_password FROM app_user WHERE id = $1`, userID).Scan(&forced)
	}); err != nil {
		t.Fatal(err)
	}
	return forced
}

func TestAConfiguredBootstrapForcesTheAdminToChoosePassword(t *testing.T) {
	svc := newSetupService(t)
	ctx := context.Background()
	_, created, _, err := svc.BootstrapInstallation(ctx, func() (InstallationBootstrap, error) {
		return claimInput("configuredforce"), nil
	}, nil)
	if err != nil || !created {
		t.Fatalf("bootstrap: created=%v err=%v", created, err)
	}
	var adminID ids.UserID
	if err := database.WithInfraTx(ctx, svc.db.Pool(), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT id FROM app_user WHERE is_agent = false`).Scan(&adminID)
	}); err != nil {
		t.Fatal(err)
	}
	if !mustChangeFor(t, svc, adminID) {
		t.Error("an operator-supplied password was not flagged for replacement — it stays valid for the life of the installation")
	}
}

func TestAClaimDoesNotForceTheAdminToChooseAgain(t *testing.T) {
	svc := newSetupService(t)
	ctx := context.Background()
	// The claim path has the human choose their own credential during the
	// claim. Forcing a change would ask them to replace a password they set
	// seconds earlier and that nobody else has ever known.
	token, err := svc.MintSetupToken(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = svc.ClaimInstallation(ctx, token, claimInput("claimedforce"), nil)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	var adminID ids.UserID
	if err := database.WithInfraTx(ctx, svc.db.Pool(), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT id FROM app_user WHERE is_agent = false`).Scan(&adminID)
	}); err != nil {
		t.Fatal(err)
	}
	if mustChangeFor(t, svc, adminID) {
		t.Error("a claimed installation's admin was told to replace the password they had just chosen")
	}
}

func TestChangingThePasswordClearsTheForcedFlag(t *testing.T) {
	// Driven from the admin, who is flagged because setupRevocationEnv boots a
	// CONFIGURED installation — the state production actually reaches. A member
	// hand-flagged by the test would prove the UPDATE clears a column, not that
	// the account the requirement lands on can satisfy it.
	e := setupRevocationEnv(t, "forced-cleared")
	if !mustChangeFor(t, e.svc, e.admin.UserID) {
		t.Fatal("a configured bootstrap left its admin unflagged, so this proves nothing")
	}
	if err := e.svc.ChangePassword(adminCtx(t, e), bootstrapPassword, newAdminPassword); err != nil {
		t.Fatalf("change: %v", err)
	}
	// Otherwise the requirement can never be satisfied and the account is
	// permanently locked out of everything but this one route.
	if mustChangeFor(t, e.svc, e.admin.UserID) {
		t.Error("the flag survived the change that was supposed to satisfy it")
	}
}

func TestAForcedAccountReachesNothingButTheChangeRoute(t *testing.T) {
	// The admin of a CONFIGURED installation is flagged by the real writer, so
	// this drives the shipped combination end to end: bootstrap, sign in, and
	// find every door shut but one.
	e := setupRevocationEnv(t, "forced-gate")
	_, sessionToken, err := e.svc.Login(e.wsOnlyCtx(), e.admin.Email, bootstrapPassword)
	if err != nil {
		t.Fatal(err)
	}

	// Through the real middleware, because that is where the requirement is
	// enforced — a client that ignores the login response must still be refused.
	h := NewHandlers(e.svc)
	reached := false
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true })

	for _, tc := range []struct{ name, path, method string }{
		{"a read", "/v1/people", http.MethodGet},
		{"a write", "/v1/people", http.MethodPost},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reached = false
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(tc.method, tc.path, nil).WithContext(e.wsOnlyCtx())
			req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: sessionToken})
			h.Middleware(next).ServeHTTP(rec, req)
			if reached {
				t.Fatalf("%s reached the handler while the account owed a password change", tc.name)
			}
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403: %s", rec.Code, rec.Body)
			}
			// 403 alone would also be a seat-tier or RBAC refusal, either of
			// which would let this pass for the wrong reason — and the code is
			// what the SPA branches on to know which screen to show.
			if code := problemCode(t, rec); code != "password_change_required" {
				t.Errorf("code = %q, want password_change_required", code)
			}
		})
	}

	// The consent entry is the third admission door and serves either an
	// identified or an anonymous caller, so it is the one that could quietly
	// admit a flagged human. Arming a consent nonce for an account still on an
	// operator's password starts a grant that the decision then refuses — a
	// dead end at the approve click, on behalf of a credential its holder never
	// chose.
	reached = false
	consent := httptest.NewRecorder()
	consentReq := httptest.NewRequest(http.MethodGet, authorizePath, nil).WithContext(e.wsOnlyCtx())
	consentReq.AddCookie(&http.Cookie{Name: SessionCookieName, Value: sessionToken})
	h.Middleware(next).ServeHTTP(consent, consentReq)
	if reached {
		t.Error("the consent entry admitted an account that owed a password change")
	}
	if code := problemCode(t, consent); code != "password_change_required" {
		t.Errorf("consent entry code = %q, want password_change_required (status %d)", code, consent.Code)
	}

	// And the one route it must reach is reachable.
	reached = false
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/auth/change-password", nil).WithContext(e.wsOnlyCtx())
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: sessionToken})
	h.Middleware(next).ServeHTTP(rec, req)
	if !reached {
		t.Errorf("the change-password route was refused too, so the requirement can never be satisfied: %d %s", rec.Code, rec.Body)
	}
}

// The forced rotation is a property of the COLUMN, not of one writer, so every
// statement that sets a password answers for it. These three are the siblings
// of the change route: two that write a password and one that spends the
// authority of an account that owes a rotation.

func TestAnOperatorResetForcesTheSubjectToChooseTheirOwn(t *testing.T) {
	// §9.1's CLI recovery is the second place an operator picks a password for
	// somebody else's account — the exact condition the flag exists to end.
	e := setupRevocationEnv(t, "operator-reset-forces")
	if mustChangeFor(t, e.svc, e.member.UserID) {
		t.Fatal("the member is flagged before the reset, so this would prove nothing")
	}
	operatorReset(t, e, e.member.Email, "operator recovery password")
	if !mustChangeFor(t, e.svc, e.member.UserID) {
		t.Error("an operator chose this account's password and nothing requires the owner to replace it")
	}
}

func TestAnOperatorResetHoldsTheSameLengthFloorAsEveryOtherRoute(t *testing.T) {
	// Eleven emoji is eleven characters and forty-four bytes: it clears a
	// byte-counted floor of twelve and fails a character-counted one. The CLI
	// path spelled its own rule and counted bytes while saying "characters",
	// so it accepted passwords the HTTP routes refuse.
	e := setupRevocationEnv(t, "operator-reset-length")
	err := withOperatorTx(t, e, func(tx pgx.Tx) error {
		return OperatorResetPassword(context.Background(), tx, e.ws, e.member.Email, strings.Repeat("🔑", 11))
	})
	var parseErr *values.ParseError
	if !errors.As(err, &parseErr) || parseErr.Code != "length" {
		t.Fatalf("an eleven-character password gave %v, want a length refusal", err)
	}
	// And the account is untouched: a refused reset must not have written.
	if _, _, loginErr := e.svc.Login(e.wsOnlyCtx(), e.member.Email, memberPassword); loginErr != nil {
		t.Errorf("the original password stopped working after a refused reset: %v", loginErr)
	}
}

func TestAnOperatorResetRefusesTheAgentSeat(t *testing.T) {
	// The agent seat carries an address, so it is reachable by the email
	// lookup, but it holds no password. Refused by name — letting the write
	// reach the database would answer a reasonable question with a constraint
	// violation.
	e := setupRevocationEnv(t, "operator-reset-agent")
	var agentEmail string
	if err := database.WithInfraTx(context.Background(), e.svc.db.Pool(), func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT email FROM app_user WHERE is_agent = true`).Scan(&agentEmail)
	}); err != nil {
		t.Fatal(err)
	}
	err := withOperatorTx(t, e, func(tx pgx.Tx) error {
		return OperatorResetPassword(context.Background(), tx, e.ws, agentEmail, "a password it cannot use")
	})
	if err == nil {
		t.Fatal("the agent seat accepted a password reset; it is an identity, not an authority")
	}
	if !strings.Contains(err.Error(), "agent seat") {
		t.Errorf("err = %v; an operator needs to be told WHICH account this is, not that a constraint fired", err)
	}
}

func TestRedeemingAResetLinkSettlesTheForcedRotation(t *testing.T) {
	// The subject chose this password themselves through their own mailbox, so
	// the question the flag asks is answered. Left raised, it would refuse
	// every route to someone holding a credential only they have ever known —
	// and the obvious next move, another reset link, reproduces it forever.
	e := setupRevocationEnv(t, "reset-settles-forced")
	ctx := e.wsOnlyCtx()
	operatorReset(t, e, e.member.Email, "operator recovery password")
	if !mustChangeFor(t, e.svc, e.member.UserID) {
		t.Fatal("the operator reset did not flag the account, so this proves nothing")
	}

	rawToken, err := e.svc.CreatePasswordReset(ctx, e.member.Email)
	if err != nil || rawToken == "" {
		t.Fatalf("CreatePasswordReset: token=%q err=%v", rawToken, err)
	}
	if err := e.svc.RedeemPasswordReset(ctx, rawToken, "the one I chose myself!"); err != nil {
		t.Fatalf("RedeemPasswordReset: %v", err)
	}
	if mustChangeFor(t, e.svc, e.member.UserID) {
		t.Error("the account still owes a rotation after its owner chose their own password")
	}
}

func TestAPassportStopsWorkingWhileItsHumanOwesARotation(t *testing.T) {
	// "Agent ≤ human" is a runtime property, so a human held to nothing over
	// REST keeps nothing through the agents they granted either.
	//
	// The flag is raised HERE by the test rather than through a writer, and
	// that is the point of the test rather than a shortcut around it. Both
	// production writers raise it beside a credential cascade that revokes
	// these passports anyway — so a test driving one of them would pass with
	// this rule deleted, which is exactly what it did before this comment
	// existed. What is being pinned is the auth query's own predicate: the
	// reason the cascade is not the only thing standing between a capped human
	// and an uncapped agent, and the reason the next writer to raise this flag
	// does not have to remember the cascade to be safe.
	e := setupRevocationEnv(t, "forced-passport")
	ctx := e.wsOnlyCtx()
	issued, err := e.svc.IssuePassport(ctx, e.member, IssuePassportInput{Scopes: []string{"read"}})
	if err != nil {
		t.Fatalf("issue passport: %v", err)
	}
	if _, err := e.svc.AuthenticateAgent(ctx, issued.Token); err != nil {
		t.Fatalf("the passport must authenticate before the rotation is owed: %v", err)
	}

	if err := database.WithWorkspaceTx(e.wsOnlyCtx(), e.svc.db.Pool(), func(tx pgx.Tx) error {
		_, execErr := tx.Exec(context.Background(),
			`UPDATE app_user SET must_change_password = true WHERE id = $1`, e.member.UserID)
		return execErr
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := e.svc.AuthenticateAgent(ctx, issued.Token); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("the passport still authenticates while its human owes a rotation: err = %v, want not-found", err)
	}
	// The trusted-process path resolves the same row without a bearer secret,
	// so a parked job must not be the way around the cap.
	if _, err := e.svc.AuthenticateAgentByID(ctx, issued.ID); !errors.Is(err, apperrors.ErrNotFound) {
		t.Errorf("the passport still resolves by id while its human owes a rotation: err = %v, want not-found", err)
	}
}

// operatorReset drives the §9.1 CLI path the way cmd/migrate does: the caller
// owns the transaction and binds the workspace GUC itself.
func operatorReset(t *testing.T, e *revocationEnv, email, newPassword string) {
	t.Helper()
	if err := withOperatorTx(t, e, func(tx pgx.Tx) error {
		return OperatorResetPassword(context.Background(), tx, e.ws, email, newPassword)
	}); err != nil {
		t.Fatalf("OperatorResetPassword(%s): %v", email, err)
	}
}

// withOperatorTx runs one operator-CLI transaction and commits it, so a caller
// that expects a REFUSAL sees the refusal rather than a rollback of it.
func withOperatorTx(t *testing.T, e *revocationEnv, fn func(pgx.Tx) error) error {
	t.Helper()
	ctx := context.Background()
	tx, err := e.svc.db.Pool().Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	//craft:ignore swallowed-errors error-path safety net only — the commit below is asserted, after which this rollback is a designed no-op
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx,
		`SELECT set_config('app.workspace_id', $1, true)`, e.ws.String()); err != nil {
		t.Fatal(err)
	}
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return nil
}
