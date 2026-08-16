// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package identity

// The obligation (ADR-0110): management is unbounded in row scope and holds
// NO governance authority. Every admin action in this module gates on the
// literal admin role key rather than on any grant or scope, so a management
// seat — whose document reads every row in the organization — is refused on
// each of them exactly like a team lead. This test proves it against the real
// seeded role, resolved through the real invite → set-password → login path,
// because a hand-built Identity carrying the key would prove only that the
// test author spelled it right.

import (
	"context"
	"errors"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

func TestManagementSeesEveryRowAndAdministersNothing(t *testing.T) {
	e := setupRevocationEnv(t, "management-role")
	ctx := context.Background()

	_, rawToken, err := e.svc.InviteUser(e.wsCtx(e.admin), e.admin, InviteUserInput{
		Email: "cso@acme.test", DisplayName: "Chief Sales Officer", Role: "management",
	})
	if err != nil {
		t.Fatalf("inviting a management seat: %v", err)
	}
	if err := e.svc.RedeemPasswordReset(principal.WithWorkspaceID(ctx, e.ws.UUID), rawToken, "a management password!"); err != nil {
		t.Fatalf("redeeming the invite token: %v", err)
	}
	mgmt, _, err := e.svc.Login(principal.WithWorkspaceID(ctx, e.ws.UUID), "cso@acme.test", "a management password!")
	if err != nil {
		t.Fatalf("management login: %v", err)
	}

	// The seeded document: manager's grid, every row.
	if got := mgmt.Permissions.RowScope; got != principal.RowScopeAll {
		t.Errorf("management row scope = %q, want %q", got, principal.RowScopeAll)
	}
	if !mgmt.Permissions.Objects["deal"].Delete || mgmt.Permissions.Objects["pipeline"].Update {
		t.Errorf("management grants = %+v; want manager's grid (deal CRUD, pipeline read-only)", mgmt.Permissions.Objects)
	}
	if mgmt.hasRole(roleAdmin) {
		t.Fatalf("management resolved with the admin role; the seat must not carry it")
	}

	mctx := e.wsCtx(mgmt)
	refusals := []struct {
		action string
		err    error
	}{
		{"invite a user", func() error {
			_, _, err := e.svc.InviteUser(mctx, mgmt, InviteUserInput{Email: "x@acme.test", DisplayName: "X", Role: "rep"})
			return err
		}()},
		{"change a user's role", e.svc.ChangeUserRole(mctx, mgmt, e.member.UserID, "manager")},
		{"edit a role's policy", func() error {
			_, err := e.svc.SetRoleObjectGrant(mctx, mgmt, "rep", "deal", storedGrant{Read: true}, nil)
			return err
		}()},
		{"issue a password link", func() error {
			_, _, err := e.svc.IssuePasswordLink(mctx, mgmt, e.member.UserID)
			return err
		}()},
		{"list every role", func() error {
			_, err := e.svc.ListRoles(mctx, mgmt)
			return err
		}()},
	}
	for _, r := range refusals {
		if !errors.Is(r.err, apperrors.ErrPermissionDenied) {
			t.Errorf("management may %s: err = %v, want permission denied", r.action, r.err)
		}
	}

	// Another user's passport reads as absent to management, as it does to
	// any non-admin — existence-hiding, not a 403.
	adminPassport, err := e.svc.IssuePassport(e.wsCtx(e.admin), e.admin, IssuePassportInput{Scopes: []string{"read"}})
	if err != nil {
		t.Fatalf("issuing the admin's passport: %v", err)
	}
	if err := e.svc.RevokePassport(mctx, mgmt, adminPassport.ID); !errors.Is(err, apperrors.ErrNotFound) {
		t.Errorf("management revoking another user's passport: err = %v, want not found", err)
	}
}
