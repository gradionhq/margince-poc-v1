// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

// The shared/ports/authz implementation: identity owns the role /
// role_assignment / app_user.seat_type tables, so it answers the gate's
// live authority questions (interfaces.md §2). platform/auth consumes
// only the interface; the composition root wires this Service in — the
// DAG never gains a platform→modules edge.

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/identity/internal/policy"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/authz"
)

var _ authz.Resolver = (*Service)(nil)

// RBACObjectGrantable reports whether an object name is one a role document may
// grant at all. Identity owns that vocabulary (internal/policy's coreObjects,
// which Parse rejects a document for stepping outside), so it is the only module
// that can answer.
//
// It is exported for the gates that derive an authority requirement somewhere
// else and must prove the requirement is SATISFIABLE before certifying whatever
// depends on it. An object outside this set is allowed by no principal that can
// exist, so a requirement naming one is not a strict rule — it is a permanent
// refusal, and a gate reading only the requirement's presence cannot tell the two
// apart. The confirm-first decidability gate is the caller today: an approval
// whose decision demands an ungrantable object can never be released or rejected
// by anyone.
func RBACObjectGrantable(object string) bool {
	return policy.IsGrantableObject(object)
}

// EffectiveRBAC reads the human's CURRENT role grants + teams. A user who
// is archived, suspended, or outside the context workspace resolves to
// ErrNotFound — absence of authority is denial, not empty permission.
func (s *Service) EffectiveRBAC(ctx context.Context, workspaceID, humanID ids.UUID) (authz.RBAC, error) {
	var out authz.RBAC
	err := s.liveUserTx(ctx, workspaceID, humanID, func(tx pgx.Tx, _ string) error {
		_, teams, perms, err := loadGrants(ctx, tx, ids.From[ids.UserKind](humanID))
		if err != nil {
			return err
		}
		out = authz.RBAC{Permissions: perms, TeamIDs: rawTeamIDs(teams)}
		return nil
	})
	return out, err
}

// EffectiveAuthority reads the human's grants AND their seat in ONE snapshot.
//
// The pair exists because reading them separately can compose an authority the
// member never held: a role change and a seat change that cross between two
// transactions leave a caller holding permissions from before with a seat from
// after. Both are ceilings on the same act, so they have to be read together —
// and liveUserTx already has both in hand, which is what makes this the cheap
// answer rather than a new query.
//
// A caller that legitimately needs only one may still use EffectiveRBAC or
// SeatType; what must not happen is one caller reading both, separately, and
// treating the pair as a fact about one instant.
func (s *Service) EffectiveAuthority(ctx context.Context, workspaceID, humanID ids.UUID) (authz.RBAC, principal.SeatType, error) {
	var (
		rbac authz.RBAC
		seat principal.SeatType
	)
	err := s.liveUserTx(ctx, workspaceID, humanID, func(tx pgx.Tx, seatType string) error {
		_, teams, perms, err := loadGrants(ctx, tx, ids.From[ids.UserKind](humanID))
		if err != nil {
			return err
		}
		rbac = authz.RBAC{Permissions: perms, TeamIDs: rawTeamIDs(teams)}
		seat = principal.SeatType(seatType)
		return nil
	})
	return rbac, seat, err
}

// SeatType reads the human's current seat — the A62/ADR-0047 licensing
// ceiling the gate checks before any tier reasoning.
func (s *Service) SeatType(ctx context.Context, workspaceID, humanID ids.UUID) (principal.SeatType, error) {
	var seat principal.SeatType
	err := s.liveUserTx(ctx, workspaceID, humanID, func(_ pgx.Tx, seatType string) error {
		seat = principal.SeatType(seatType)
		return nil
	})
	return seat, err
}

// liveUserTx runs fn inside the workspace transaction after proving the
// user is live in exactly the requested workspace. The GUC contract binds
// the tenant from the context; a caller asking about a different
// workspace is a programming error, refused rather than answered from
// the wrong tenant.
func (s *Service) liveUserTx(ctx context.Context, workspaceID, humanID ids.UUID, fn func(tx pgx.Tx, seatType string) error) error {
	ctxWs, ok := principal.WorkspaceID(ctx)
	if !ok || ctxWs != workspaceID {
		return fmt.Errorf("crmauth: authority resolution outside the bound workspace")
	}
	return s.db.Tx(ctx, func(tx pgx.Tx) error {
		var seatType string
		err := tx.QueryRow(ctx,
			`SELECT seat_type FROM app_user
			 WHERE id = $1 AND status = 'active' AND archived_at IS NULL`,
			humanID).Scan(&seatType)
		if errors.Is(err, pgx.ErrNoRows) {
			return apperrors.ErrNotFound
		}
		if err != nil {
			return err
		}
		return fn(tx, seatType)
	})
}
