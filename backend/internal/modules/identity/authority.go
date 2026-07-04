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

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/authz"
)

var _ authz.Resolver = (*Service)(nil)

// EffectiveRBAC reads the human's CURRENT role grants + teams. A user who
// is archived, suspended, or outside the context workspace resolves to
// ErrNotFound — absence of authority is denial, not empty permission.
func (s *Service) EffectiveRBAC(ctx context.Context, workspaceID, humanID ids.UUID) (authz.RBAC, error) {
	var out authz.RBAC
	err := s.liveUserTx(ctx, workspaceID, humanID, func(tx pgx.Tx, _ string) error {
		_, teams, perms, err := loadGrants(ctx, tx, humanID)
		if err != nil {
			return err
		}
		out = authz.RBAC{Permissions: perms, TeamIDs: teams}
		return nil
	})
	return out, err
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
	return database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		var seatType string
		err := tx.QueryRow(ctx,
			`SELECT seat_type FROM app_user
			 WHERE id = $1 AND workspace_id = $2 AND status = 'active' AND archived_at IS NULL`,
			humanID, workspaceID).Scan(&seatType)
		if errors.Is(err, pgx.ErrNoRows) {
			return apperrors.ErrNotFound
		}
		if err != nil {
			return err
		}
		return fn(tx, seatType)
	})
}
