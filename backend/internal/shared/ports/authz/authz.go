// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package authz is the RBAC/seat resolver seam (interfaces.md §2). The
// admission gate lives in platform/auth, but the authoritative role /
// role_assignment / app_user.seat_type rows are owned by modules/identity
// — which the DAG places ABOVE platform (ADR-0054 §5). So the gate
// re-derives an agent's authority through this dependency-free interface:
// identity implements it, the composition root injects it, and platform
// imports only this package. Reading live at every admission is what makes
// a role or seat revocation bind mid-session (data-model §11, RT-AR-M11)
// instead of surviving as a cached value on the token.
package authz

import (
	"context"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// RBAC is a human's current effective authority: the merged permission
// policy of their roles plus their team memberships (the row-scope key).
type RBAC struct {
	Permissions principal.Permissions
	TeamIDs     []ids.UUID
}

// Resolver returns a principal's CURRENT effective authority, read live
// at admission. Implemented by modules/identity; consumed by
// platform/auth's gate. No caching in the seam — the event-bus cascade
// may cache above it as an optimization, never inside it.
type Resolver interface {
	// EffectiveRBAC resolves the human's current role grants + teams for
	// (workspace, human). A missing/archived/suspended user resolves to
	// apperrors.ErrNotFound — never to an empty-but-valid authority.
	EffectiveRBAC(ctx context.Context, workspaceID, humanID ids.UUID) (RBAC, error)
	// SeatType returns the human's current seat (read | full) — the seat
	// ceiling (A62/ADR-0047).
	SeatType(ctx context.Context, workspaceID, humanID ids.UUID) (principal.SeatType, error)
}
