// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package overlay

// The webhook-as-signal tenant binding's real-Postgres proof (OVA-DDL-3 /
// OVA-WIRE-10 / AC-OV-13 a–b): Connect records the incumbent portal id, and
// WorkspaceForPortal resolves ONLY the workspace whose active connection
// carries that portal — an unbound portal is ErrNotFound (fail-closed, no
// cross-tenant), which is what makes the receiver refuse it.

import (
	"errors"
	"testing"

	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
)

// TestWorkspaceForPortalBindsAndFailsClosed connects a workspace with a portal
// id, then asserts the binding resolves it — and that a foreign/unknown portal
// resolves to nothing (fail-closed).
func TestWorkspaceForPortalBindsAndFailsClosed(t *testing.T) {
	ctx, pool, ws := testWorkspaceCtx(t)
	store := NewMirrorStore(pool, noOwnerEmails{})
	svc := NewService(pool, keyvault.NewMemory(), store).
		WithIncumbentFactory(func(_, _ string) Incumbent {
			return seedIncumbent{portalID: "portal-A"}
		})

	if _, err := svc.Connect(ctx, ConnectInput{Incumbent: "hubspot", Region: "eu1", Token: "tok"}); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	got, err := WorkspaceForPortal(ctx, pool, "hubspot", "portal-A")
	if err != nil {
		t.Fatalf("WorkspaceForPortal(portal-A): %v", err)
	}
	if got.UUID != ws {
		t.Errorf("WorkspaceForPortal(portal-A) = %s, want the connected workspace %s", got.UUID, ws)
	}

	// A portal bound to no active connection resolves fail-closed — the
	// receiver rejects it, never ingesting cross-tenant.
	if _, err := WorkspaceForPortal(ctx, pool, "hubspot", "portal-UNKNOWN"); !errors.Is(err, apperrors.ErrNotFound) {
		t.Errorf("WorkspaceForPortal(unknown portal): err = %v, want ErrNotFound", err)
	}
	// A blank portal is likewise unbindable.
	if _, err := WorkspaceForPortal(ctx, pool, "hubspot", ""); !errors.Is(err, apperrors.ErrNotFound) {
		t.Errorf("WorkspaceForPortal(\"\"): err = %v, want ErrNotFound", err)
	}
}
