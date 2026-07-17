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
	h := NewHandlers(e.svc).WithPasswordReset(mail, "https://crm.example.test/")

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
	if mail.sent != 1 || mail.to != e.member.Email {
		t.Fatalf("mail = %+v, want one message to the member", mail)
	}
	if !strings.Contains(mail.body, "https://crm.example.test/reset-password?token=") {
		t.Fatalf("mail body carries no reset link: %q", mail.body)
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

func TestPasswordResetRequestIsEnumerationResistant(t *testing.T) {
	e := setupRevocationEnv(t, "reset-enum")
	ctx := e.wsOnlyCtx()
	mail := &capturedMail{}
	h := NewHandlers(e.svc).WithPasswordReset(mail, "https://crm.example.test")

	rec := httptest.NewRecorder()
	h.RequestPasswordReset(rec, httptest.NewRequest(http.MethodPost, "/v1/auth/forgot-password",
		strings.NewReader(`{"email":"nobody-`+e.member.Email+`"}`)).WithContext(ctx))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("unknown address status = %d, want the same 202 a known one gets", rec.Code)
	}
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
	// connection (the app role cannot touch clocks) and expect the same
	// neutral refusal.
	if _, err := e.owner.Exec(context.Background(),
		`UPDATE auth_token SET expires_at = now() - interval '1 minute'
		 WHERE user_id = $1 AND used_at IS NULL`, e.member.UserID); err != nil {
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

func TestCapabilitiesReflectTheWiredMailer(t *testing.T) {
	h := NewHandlers(&Service{})
	rec := httptest.NewRecorder()
	h.GetAuthCapabilities(rec, httptest.NewRequest(http.MethodGet, "/v1/auth/capabilities", nil))
	if !strings.Contains(rec.Body.String(), `"password_reset":false`) {
		t.Fatalf("unwired capabilities = %s, want password_reset:false", rec.Body)
	}

	h = h.WithPasswordReset(&capturedMail{}, "https://crm.example.test")
	rec = httptest.NewRecorder()
	h.GetAuthCapabilities(rec, httptest.NewRequest(http.MethodGet, "/v1/auth/capabilities", nil))
	if !strings.Contains(rec.Body.String(), `"password_reset":true`) {
		t.Fatalf("wired capabilities = %s, want password_reset:true", rec.Body)
	}
}
