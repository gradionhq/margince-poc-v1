// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package identity

// The A74 forgot/reset flow end to end: enumeration-resistant request,
// single-use short-TTL token, the redeem that swaps the hash and ends
// every session, and the neutral refusal for unknown, used, and expired
// tokens alike. The mailer is a captured fake — the only true boundary.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// capturedMail is the fake A74 transport: it records what would have
// left the installation.
type capturedMail struct {
	to, subject, body string
	sent              int
}

func (m *capturedMail) Send(_ context.Context, to, subject, body string) error {
	m.to, m.subject, m.body = to, subject, body
	m.sent++
	return nil
}

var resetLinkToken = regexp.MustCompile(`token=([A-Za-z0-9_-]+)`)

func (e *revocationEnv) wsOnlyCtx() context.Context {
	return principal.WithWorkspaceID(context.Background(), e.admin.WorkspaceID.UUID)
}

func TestPasswordResetFlowEndToEnd(t *testing.T) {
	e := setupRevocationEnv(t, "reset-e2e")
	ctx := e.wsOnlyCtx()
	mail := &capturedMail{}
	h := NewHandlers(e.svc).WithPasswordReset(mail).WithPasswordLinkBase("https://crm.example.test/")
	sent := make(chan struct{})
	h.resetSendStarted = func() { close(sent) }

	// The member holds a live session that the reset must end.
	_, sessionToken, err := e.svc.Login(ctx, e.member.Email, memberPassword)
	if err != nil {
		t.Fatalf("pre-reset login: %v", err)
	}

	// Request: 202, and the mail carries the link with the raw token.
	rec := httptest.NewRecorder()
	h.RequestPasswordReset(rec, httptest.NewRequest(http.MethodPost, "/v1/auth/forgot-password",
		strings.NewReader(`{"email":"`+e.member.Email+`"}`)).WithContext(ctx))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("forgot-password status = %d, want 202: %s", rec.Code, rec.Body)
	}
	// The send is asynchronous by design (response timing must not
	// disclose account existence); the test seam signals completion.
	<-sent
	if mail.sent != 1 || mail.to != e.member.Email {
		t.Fatalf("mail = %+v, want one message to the member", mail)
	}
	if !strings.Contains(mail.body, "https://crm.example.test/#/reset-password?token=") {
		t.Fatalf("mail body carries no reset link: %q", mail.body)
	}
	// The token must be in the FRAGMENT, because a query string hands this live
	// single-use credential to every access log, Referer header and Cache Storage
	// key on the way in — the shape is a security assertion, not a formatting one.
	//
	// Split on '#' rather than searching the whole body: the correct link contains
	// "/reset-password?token=" too, immediately AFTER the fragment marker, so a
	// naive substring check fires on the good link and proves nothing.
	serverVisible, _, _ := strings.Cut(mail.body, "#")
	if strings.Contains(serverVisible, "token=") {
		t.Fatalf("reset link puts the token in the server-visible query: %q", mail.body)
	}
	match := resetLinkToken.FindStringSubmatch(mail.body)
	if match == nil {
		t.Fatalf("no token in the mail body: %q", mail.body)
	}
	rawToken := match[1]

	// Redeem: 204; the hash swapped, every session revoked, token spent.
	const newPassword = "an entirely new password"
	rec = httptest.NewRecorder()
	h.ResetPassword(rec, httptest.NewRequest(http.MethodPost, "/v1/auth/reset-password",
		strings.NewReader(`{"token":"`+rawToken+`","new_password":"`+newPassword+`"}`)).WithContext(ctx))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("reset-password status = %d, want 204: %s", rec.Code, rec.Body)
	}
	if _, err := e.svc.Authenticate(ctx, sessionToken); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("pre-reset session still authenticates (err=%v); a completed reset must end every session", err)
	}
	if _, _, err := e.svc.Login(ctx, e.member.Email, memberPassword); !errors.Is(err, ErrBadCredentials) {
		t.Fatalf("old password still logs in: %v", err)
	}
	if _, _, err := e.svc.Login(ctx, e.member.Email, newPassword); err != nil {
		t.Fatalf("new password refused: %v", err)
	}

	// Single-use: the same token answers the one neutral refusal.
	if err := e.svc.RedeemPasswordReset(ctx, rawToken, "yet another password!"); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("spent token redeemed again: %v", err)
	}
}

// TestPasswordResetRevokesPassportsToo pins the invariant: a completed
// reset must end every credential that could act as the account, not just
// the session cookie that prompted the recovery.
func TestPasswordResetRevokesPassportsToo(t *testing.T) {
	e := setupRevocationEnv(t, "reset-passport-cascade")
	ctx := e.wsOnlyCtx()

	issued, err := e.svc.IssuePassport(ctx, e.member, IssuePassportInput{Scopes: []string{"read"}})
	if err != nil {
		t.Fatalf("issue passport: %v", err)
	}
	if _, err := e.svc.AuthenticateAgent(ctx, issued.Token); err != nil {
		t.Fatalf("passport must authenticate before the reset: %v", err)
	}

	rawToken, err := e.svc.CreatePasswordReset(ctx, e.member.Email)
	if err != nil || rawToken == "" {
		t.Fatalf("CreatePasswordReset: token=%q err=%v", rawToken, err)
	}
	if err := e.svc.RedeemPasswordReset(ctx, rawToken, "a brand new recovery password"); err != nil {
		t.Fatalf("RedeemPasswordReset: %v", err)
	}

	if _, err := e.svc.AuthenticateAgent(ctx, issued.Token); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("passport minted before the reset still authenticates: err = %v, want not-found", err)
	}
	var livePassports int
	if err := e.owner.QueryRow(context.Background(),
		`SELECT count(*) FROM passport WHERE on_behalf_of = $1 AND revoked_at IS NULL`,
		e.member.UserID).Scan(&livePassports); err != nil {
		t.Fatal(err)
	}
	if livePassports != 0 {
		t.Fatalf("reset left %d live passports, want 0", livePassports)
	}
}

// TestOperatorResetPasswordRevokesPassportsToo is the same credential
// cascade proven over the operator-CLI recovery path (reset.go's
// OperatorResetPassword).
func TestOperatorResetPasswordRevokesPassportsToo(t *testing.T) {
	e := setupRevocationEnv(t, "operator-reset-passport-cascade")
	ctx := e.wsOnlyCtx()

	issued, err := e.svc.IssuePassport(ctx, e.member, IssuePassportInput{Scopes: []string{"read"}})
	if err != nil {
		t.Fatalf("issue passport: %v", err)
	}

	tx, err := e.owner.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	//craft:ignore swallowed-errors error-path safety net only — the Commit below is asserted, after which this rollback is a designed no-op
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(context.Background(), `SELECT set_config('app.workspace_id', $1, true)`, e.admin.WorkspaceID.String()); err != nil {
		t.Fatal(err)
	}
	if err := OperatorResetPassword(context.Background(), tx, e.admin.WorkspaceID, e.member.Email, "operator recovery password"); err != nil {
		t.Fatalf("OperatorResetPassword: %v", err)
	}
	if err := tx.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}

	if _, err := e.svc.AuthenticateAgent(ctx, issued.Token); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("passport minted before the operator reset still authenticates: err = %v, want not-found", err)
	}
}

// TestOperatorResetPasswordRevokesALiveOAuthGrantToo runs the operator
// recovery path against a user who holds a real OAuth connection, not just
// a locally minted passport. That distinction matters: the grant cascade's
// per-passport revocation events are staged through storekit.Emit, which
// refuses to write without a correlation id bound on the context — and
// cmd/migrate's bare command context supplies no operation scope of its
// own the way an HTTP request or a bus consumer would. A locally minted
// passport never reaches that branch (it carries no grant), so it cannot
// stand in for this case.
func TestOperatorResetPasswordRevokesALiveOAuthGrantToo(t *testing.T) {
	e := setupRevocationEnv(t, "operator-reset-oauth-cascade")
	ctx := context.Background()

	clientID := "client-" + ids.NewV7().String()
	if _, err := e.owner.Exec(ctx, `
		INSERT INTO oauth_client (workspace_id, client_id, client_name, redirect_uris)
		VALUES ($1, $2, 'operator-reset-cascade', ARRAY['https://client.example/cb'])`,
		e.admin.WorkspaceID, clientID); err != nil {
		t.Fatalf("registering the client: %v", err)
	}
	grantID := ids.NewV7()
	if _, err := e.owner.Exec(ctx, `
		INSERT INTO oauth_grant (id, workspace_id, client_id, user_id, scopes, refresh_allowed)
		VALUES ($1, $2, $3, $4, ARRAY['read']::text[], false)`,
		grantID, e.admin.WorkspaceID, clientID, e.member.UserID); err != nil {
		t.Fatalf("issuing the grant: %v", err)
	}
	if _, err := e.owner.Exec(ctx, `
		INSERT INTO passport (workspace_id, on_behalf_of, granted_by, label, scopes, token_hash, expires_at, oauth_grant_id)
		VALUES ($1, $2, $2, 'operator-reset-cascade', ARRAY['read']::text[], $3, now() + interval '30 days', $4)`,
		e.admin.WorkspaceID, e.member.UserID, "operator-reset-cascade-hash-"+grantID.String(), grantID); err != nil {
		t.Fatalf("minting the connection's credential: %v", err)
	}

	tx, err := e.owner.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	//craft:ignore swallowed-errors error-path safety net only — the Commit below is asserted, after which this rollback is a designed no-op
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(context.Background(), `SELECT set_config('app.workspace_id', $1, true)`, e.admin.WorkspaceID.String()); err != nil {
		t.Fatal(err)
	}
	if err := OperatorResetPassword(context.Background(), tx, e.admin.WorkspaceID, e.member.Email, "operator recovery over a live connection"); err != nil {
		t.Fatalf("OperatorResetPassword must not error for a user with a live OAuth connection: %v", err)
	}
	if err := tx.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}

	var grantRevoked bool
	if err := e.owner.QueryRow(context.Background(),
		`SELECT revoked_at IS NOT NULL FROM oauth_grant WHERE id = $1`, grantID).Scan(&grantRevoked); err != nil {
		t.Fatal(err)
	}
	if !grantRevoked {
		t.Error("the OAuth grant survived an operator reset")
	}
	var livePassports int
	if err := e.owner.QueryRow(context.Background(),
		`SELECT count(*) FROM passport WHERE oauth_grant_id = $1 AND revoked_at IS NULL`, grantID).Scan(&livePassports); err != nil {
		t.Fatal(err)
	}
	if livePassports != 0 {
		t.Errorf("%d passports issued under the grant are still live after an operator reset", livePassports)
	}
}

func TestPasswordResetRequestIsEnumerationResistant(t *testing.T) {
	e := setupRevocationEnv(t, "reset-enum")
	ctx := e.wsOnlyCtx()
	mail := &capturedMail{}
	h := NewHandlers(e.svc).WithPasswordReset(mail).WithPasswordLinkBase("https://crm.example.test")

	sent := make(chan struct{})
	h.resetSendStarted = func() { close(sent) }
	rec := httptest.NewRecorder()
	h.RequestPasswordReset(rec, httptest.NewRequest(http.MethodPost, "/v1/auth/forgot-password",
		strings.NewReader(`{"email":"nobody-`+e.member.Email+`"}`)).WithContext(ctx))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("unknown address status = %d, want the same 202 a known one gets", rec.Code)
	}
	// The whole account-dependent path is asynchronous; wait for it
	// before asserting nothing left the installation.
	<-sent
	if mail.sent != 0 {
		t.Fatalf("a mail left for an unknown address: %+v", mail)
	}
}

func TestPasswordResetSupersedesAndExpires(t *testing.T) {
	e := setupRevocationEnv(t, "reset-ttl")
	ctx := e.wsOnlyCtx()

	first, err := e.svc.CreatePasswordReset(ctx, e.member.Email)
	if err != nil || first == "" {
		t.Fatalf("first CreatePasswordReset: token=%q err=%v", first, err)
	}
	second, err := e.svc.CreatePasswordReset(ctx, e.member.Email)
	if err != nil || second == "" {
		t.Fatalf("second CreatePasswordReset: token=%q err=%v", second, err)
	}
	// A new request supersedes the outstanding token.
	if err := e.svc.RedeemPasswordReset(ctx, first, "a superseded password"); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("superseded token redeemed: %v", err)
	}

	// Expiry: shift the live token past its TTL through the owner
	// connection (the app role cannot touch clocks), workspace-bound and
	// row-count-asserted so the fixture can never miss silently.
	expireTx, err := e.owner.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	//craft:ignore swallowed-errors error-path safety net only — the Commit below is asserted, after which this rollback is a designed no-op
	defer func() { _ = expireTx.Rollback(context.Background()) }()
	if _, err := expireTx.Exec(context.Background(), `SELECT set_config('app.workspace_id', $1, true)`, e.admin.WorkspaceID.String()); err != nil {
		t.Fatal(err)
	}
	tag, err := expireTx.Exec(context.Background(),
		`UPDATE auth_token SET expires_at = now() - interval '1 minute'
		 WHERE user_id = $1 AND used_at IS NULL`, e.member.UserID)
	if err != nil {
		t.Fatal(err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("expiry fixture touched %d rows, want exactly the live token", tag.RowsAffected())
	}
	if err := expireTx.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.RedeemPasswordReset(ctx, second, "an expired password!"); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("expired token redeemed: %v", err)
	}
}

func TestOperatorResetPasswordRecoversTheAccount(t *testing.T) {
	e := setupRevocationEnv(t, "reset-operator")
	ctx := e.wsOnlyCtx()

	_, sessionToken, err := e.svc.Login(ctx, e.member.Email, memberPassword)
	if err != nil {
		t.Fatalf("pre-reset login: %v", err)
	}

	// The operator path runs on the owner connection with the workspace
	// GUC bound — exactly what `migrate reset-password` does.
	tx, err := e.owner.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	//craft:ignore swallowed-errors error-path safety net only — the Commit below is asserted, after which this rollback is a designed no-op
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(context.Background(), `SELECT set_config('app.workspace_id', $1, true)`, e.admin.WorkspaceID.String()); err != nil {
		t.Fatal(err)
	}
	const operatorPassword = "operator chosen password"
	if err := OperatorResetPassword(context.Background(), tx, e.admin.WorkspaceID, e.member.Email, operatorPassword); err != nil {
		t.Fatalf("OperatorResetPassword: %v", err)
	}
	if err := tx.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}

	if _, err := e.svc.Authenticate(ctx, sessionToken); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("session survived the operator reset: %v", err)
	}
	if _, _, err := e.svc.Login(ctx, e.member.Email, operatorPassword); err != nil {
		t.Fatalf("operator-set password refused: %v", err)
	}
	if err := OperatorResetPasswordSmoke(ctx, e, "missing@nobody.test"); err == nil {
		t.Fatal("operator reset for an unknown email must fail loudly")
	}
}

// OperatorResetPasswordSmoke drives the operator path for an address in
// its own transaction — the unknown-email refusal must not poison the
// test's main transaction.
func OperatorResetPasswordSmoke(ctx context.Context, e *revocationEnv, email string) error {
	tx, err := e.owner.Begin(context.Background())
	if err != nil {
		return err
	}
	//craft:ignore swallowed-errors error-path cleanup — the result under test is OperatorResetPassword's error
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(context.Background(), `SELECT set_config('app.workspace_id', $1, true)`, e.admin.WorkspaceID.String()); err != nil {
		return err
	}
	return OperatorResetPassword(context.Background(), tx, ids.From[ids.WorkspaceKind](e.admin.WorkspaceID.UUID), email, "irrelevant password!")
}
