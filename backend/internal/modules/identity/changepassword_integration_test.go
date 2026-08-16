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
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
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
	if err := e.svc.ChangePassword(ctx, memberPassword, "🔑🔑🔑🔑"); err == nil {
		t.Fatal("a four-character password was accepted")
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
