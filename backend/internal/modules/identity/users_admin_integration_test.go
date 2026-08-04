// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package identity

// Admin user administration over a real migrated Postgres: an invite creates an
// active, passwordless member with the one target role and a single-use
// set-password token and emits user.invited; a reactivate returns a deactivated
// member to active and emits user.reactivated. Both are admin-only.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func TestInviteUserCreatesActiveMemberWithRoleTokenAndEvent(t *testing.T) {
	e := setupRevocationEnv(t, "invite-user")

	userID, rawToken, err := e.svc.InviteUser(e.wsCtx(e.admin), e.admin, InviteUserInput{
		Email: "Newbie@Acme.test", DisplayName: "New Bie", Role: "rep",
	})
	if err != nil {
		t.Fatalf("invite: %v", err)
	}
	if userID.IsZero() || rawToken == "" {
		t.Fatalf("invite returned userID=%v token-empty=%v; want both set", userID, rawToken == "")
	}

	member, err := e.svc.GetUser(e.wsCtx(e.admin), userID)
	if err != nil {
		t.Fatalf("get invited member: %v", err)
	}
	if member.Status != "active" || member.Email != "newbie@acme.test" {
		t.Fatalf("invited member = %+v; want active + lowercased email", member)
	}

	var role string
	if err := e.owner.QueryRow(context.Background(),
		`SELECT r.key FROM role_assignment ra JOIN role r ON r.id = ra.role_id WHERE ra.user_id = $1`,
		userID).Scan(&role); err != nil {
		t.Fatalf("role lookup: %v", err)
	}
	if role != "rep" {
		t.Errorf("invited member role = %q, want rep", role)
	}

	var liveTokens int
	if err := e.owner.QueryRow(context.Background(),
		`SELECT count(*) FROM auth_token WHERE user_id = $1 AND purpose = 'password_reset' AND used_at IS NULL`,
		userID).Scan(&liveTokens); err != nil {
		t.Fatal(err)
	}
	if liveTokens != 1 {
		t.Errorf("invite minted %d live set-password tokens, want exactly 1", liveTokens)
	}

	envs := e.identityEvents(t, "user.invited")
	if len(envs) != 1 {
		t.Fatalf("user.invited staged %d times, want once", len(envs))
	}
	var payload struct {
		UserID ids.UserID `json:"user_id"`
		Role   string     `json:"role"`
		By     ids.UserID `json:"by"`
	}
	if err := json.Unmarshal(envs[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.UserID != userID || payload.Role != "rep" || payload.By != e.admin.UserID {
		t.Errorf("user.invited payload = %+v, want {invited id, rep, admin}", payload)
	}
	if envs[0].Trace.AuditLogID.IsZero() {
		t.Error("user.invited carries no audit_log_id — the write shape demands the linked audit row")
	}

	// A duplicate email refuses; an unknown role is a 404; a non-admin cannot invite.
	if _, _, err := e.svc.InviteUser(e.wsCtx(e.admin), e.admin, InviteUserInput{
		Email: "newbie@acme.test", DisplayName: "Dupe", Role: "rep",
	}); !errors.Is(err, apperrors.ErrConflict) {
		t.Errorf("duplicate-email invite: err = %v, want conflict", err)
	}
	if _, _, err := e.svc.InviteUser(e.wsCtx(e.admin), e.admin, InviteUserInput{
		Email: "other@acme.test", DisplayName: "X", Role: "no-such-role",
	}); !errors.Is(err, apperrors.ErrNotFound) {
		t.Errorf("unknown-role invite: err = %v, want not found", err)
	}
	if _, _, err := e.svc.InviteUser(e.wsCtx(e.member), e.member, InviteUserInput{
		Email: "sneaky@acme.test", DisplayName: "X", Role: "rep",
	}); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Errorf("non-admin invite: err = %v, want permission denied", err)
	}
}

// TestInviteUserHandlerEmailsTheSetPasswordLink drives InviteUser through the
// HTTP handler rather than the service directly, because sendInvite — and the
// passwordLink call inside it — only runs on that path: the service layer
// mints the token but never sends the mail.
func TestInviteUserHandlerEmailsTheSetPasswordLink(t *testing.T) {
	e := setupRevocationEnv(t, "invite-mail")
	mail := &capturedMail{}
	h := NewHandlers(e.svc).WithPasswordReset(mail, "https://crm.example.test")

	ctx := withIdentity(e.wsCtx(e.admin), e.admin)
	body := `{"email":"invited@acme.test","display_name":"Invited Rep","role":"rep"}`
	rec := httptest.NewRecorder()
	h.InviteUser(rec, httptest.NewRequest(http.MethodPost, "/v1/users", strings.NewReader(body)).WithContext(ctx))
	if rec.Code != http.StatusCreated {
		t.Fatalf("invite status = %d, want 201: %s", rec.Code, rec.Body)
	}

	if mail.sent != 1 || mail.to != "invited@acme.test" {
		t.Fatalf("mail = %+v, want one message to the invited address", mail)
	}
	// Same shape as the reset link: the token rides the fragment, never the
	// server-visible query — passwordLink is the single builder for both.
	serverVisible, fragment, found := strings.Cut(mail.body, "#")
	if !found || strings.Contains(serverVisible, "token=") {
		t.Fatalf("invite link puts the token in the server-visible query: %q", mail.body)
	}
	if !strings.Contains(fragment, "/reset-password?token=") {
		t.Fatalf("invite mail carries no set-password link: %q", mail.body)
	}
}

func TestReactivateUserRestoresActiveAndEmits(t *testing.T) {
	e := setupRevocationEnv(t, "reactivate-user")

	if err := e.svc.DeactivateUser(e.wsCtx(e.admin), e.admin, DeactivateUserInput{UserID: e.member.UserID}); err != nil {
		t.Fatalf("deactivate: %v", err)
	}
	if err := e.svc.ReactivateUser(e.wsCtx(e.admin), e.admin, e.member.UserID); err != nil {
		t.Fatalf("reactivate: %v", err)
	}

	member, err := e.svc.GetUser(e.wsCtx(e.admin), e.member.UserID)
	if err != nil {
		t.Fatalf("get reactivated member: %v", err)
	}
	if member.Status != "active" {
		t.Errorf("reactivated member status = %q, want active", member.Status)
	}

	envs := e.identityEvents(t, "user.reactivated")
	if len(envs) != 1 {
		t.Fatalf("user.reactivated staged %d times, want once", len(envs))
	}

	// Idempotent on an already-active member: no error, no duplicate event.
	if err := e.svc.ReactivateUser(e.wsCtx(e.admin), e.admin, e.member.UserID); err != nil {
		t.Fatalf("repeat reactivate: %v", err)
	}
	if again := e.identityEvents(t, "user.reactivated"); len(again) != 1 {
		t.Errorf("repeat reactivation staged a duplicate event (%d total)", len(again))
	}

	// An unknown member is a 404; a non-admin cannot reactivate.
	if err := e.svc.ReactivateUser(e.wsCtx(e.admin), e.admin, ids.UserID{UUID: ids.NewV7()}); !errors.Is(err, apperrors.ErrNotFound) {
		t.Errorf("reactivate unknown: err = %v, want not found", err)
	}
	if err := e.svc.ReactivateUser(e.wsCtx(e.member), e.member, e.admin.UserID); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Errorf("non-admin reactivate: err = %v, want permission denied", err)
	}
}

// TestConcurrentLastAdminDeactivationsKeepOneAdmin proves the admin-guard is
// race-safe: with two admins, two transactions each deactivating a DIFFERENT
// one must not both succeed (that would zero out admins). The per-workspace
// advisory lock serializes them, so exactly one is refused.
func TestConcurrentLastAdminDeactivationsKeepOneAdmin(t *testing.T) {
	e := setupRevocationEnv(t, "admin-race")

	// Promote the member to admin — now the workspace has exactly two admins.
	if err := e.svc.ChangeUserRole(e.wsCtx(e.admin), e.admin, e.member.UserID, "admin"); err != nil {
		t.Fatalf("promote member to admin: %v", err)
	}

	targets := []ids.UserID{e.admin.UserID, e.member.UserID}
	errs := make([]error, len(targets))
	var wg sync.WaitGroup
	for i, target := range targets {
		wg.Add(1)
		go func(i int, target ids.UserID) {
			defer wg.Done()
			errs[i] = e.svc.DeactivateUser(e.wsCtx(e.admin), e.admin, DeactivateUserInput{UserID: target})
		}(i, target)
	}
	wg.Wait()

	conflicts := 0
	for _, err := range errs {
		switch {
		case err == nil:
		case errors.Is(err, apperrors.ErrConflict):
			conflicts++
		default:
			t.Fatalf("unexpected deactivation error: %v", err)
		}
	}
	if conflicts != 1 {
		t.Fatalf("concurrent double-admin deactivation refused %d times, want exactly 1 — the race would zero out admins", conflicts)
	}
}
