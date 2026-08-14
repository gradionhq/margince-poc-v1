// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

// How many seats the installation is using, for the entitlement surface. The
// LICENSE half of that surface is not here — what a license grants is resolved
// from the deployment file and the bundled validation module, which identity
// knows nothing about — so the composition root pairs this count with the
// posture (ADR-0054: a cross-layer edge is injected, never imported).

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// licenseObject is the RBAC object gating the entitlement read. Admin/ops only,
// read included: a seat meter is the installation's commercial standing, and a
// rep reads their own seat elsewhere (UC-ADMIN-03 F1).
const licenseObject = "license"

// SeatUsageStore counts the seats an entitlement is measured against.
type SeatUsageStore struct {
	db *database.DB
}

// NewSeatUsage builds the store on a handle already bound to the workspace it
// serves.
func NewSeatUsage(db *database.DB) *SeatUsageStore { return &SeatUsageStore{db: db} }

// FullSeatsInUse counts the full seats this installation is using: every
// non-deactivated one, agents included.
//
// Three decisions the count makes, each of which the meter would be wrong
// without:
//
// READ SEATS ARE NOT COUNTED. They are unlimited and never metered — that is the
// whole of A62/ADR-0047, and the reason a workspace can hand out viewers freely.
//
// A SUSPENDED SEAT IS NOT COUNTED. Suspension is how an admin stops somebody
// acting without erasing them, so counting one would bill for access the
// installation has already withdrawn.
//
// AN AGENT SEAT IS COUNTED. `app_user_agent_is_full` makes every agent a full
// seat, and a first-party runner acts on the estate exactly as a human does.
// Excluding them would let an installation act without limit through agents.
//
// This is deliberately NOT the spec's "active" definition (signed in within 30
// days, UC-ADMIN-03 precondition 3): the two meters therefore disagree, which is
// recorded on issue #1190 for reconciliation rather than resolved here.
func (s *SeatUsageStore) FullSeatsInUse(ctx context.Context) (int, error) {
	if err := auth.Require(ctx, licenseObject, principal.ActionRead); err != nil {
		return 0, err
	}
	var used int
	err := s.db.Tx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT count(*) FROM app_user
			  WHERE seat_type = 'full' AND status <> 'deactivated'`).Scan(&used)
	})
	if err != nil {
		return 0, fmt.Errorf("identity: counting full seats in use: %w", err)
	}
	return used, nil
}
