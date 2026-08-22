// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package deals

// The bulk owner handover: every live project one user owns moves to another
// user in one transaction. It is updateProject.owner_id applied N times, and
// it writes exactly what N single updates would write — one `update` audit
// row carrying the owner_id before/after images and one project.updated event
// per project — so each project's field history shows the move by itself.

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// TransferProjectOwnershipInput names the two ends of a handover.
type TransferProjectOwnershipInput struct {
	FromOwnerID ids.UserID
	ToOwnerID   ids.UserID
}

// OwnerNotActiveError maps to 422: the receiving user is not an active member
// of the workspace, so the projects would land on nobody's desk.
type OwnerNotActiveError struct{}

func (e *OwnerNotActiveError) Error() string {
	return "to_owner_id must name an active user of this workspace"
}

// FieldFault names the receiving owner as the field refused.
func (e *OwnerNotActiveError) FieldFault() (field, code, message string) {
	return "to_owner_id", "owner_not_active", e.Error()
}

// SameOwnerError maps to 422: a transfer from a user to themselves would move
// nothing and audit nothing, and saying so beats answering `transferred: 0`.
type SameOwnerError struct{}

func (e *SameOwnerError) Error() string {
	return "from_owner_id and to_owner_id name the same user"
}

// FieldFault names the receiving owner as the field refused.
func (e *SameOwnerError) FieldFault() (field, code, message string) {
	return "to_owner_id", "same_owner", e.Error()
}

// TransferProjectOwnership moves every live project FromOwnerID owns that the
// caller may write onto ToOwnerID, and answers how many moved. Archived
// projects and projects outside the caller's write authority stay put and are
// not counted; the caller is told a number, never which rows were withheld.
func (s *Store) TransferProjectOwnership(ctx context.Context, in TransferProjectOwnershipInput) (int, error) {
	if err := auth.Require(ctx, projectObject, principal.ActionUpdate); err != nil {
		return 0, err
	}
	if in.FromOwnerID == in.ToOwnerID {
		return 0, &SameOwnerError{}
	}
	moved := 0
	err := s.tx(ctx, func(tx pgx.Tx) error {
		if err := ensureActiveOwner(ctx, tx, in.ToOwnerID); err != nil {
			return err
		}
		candidates, err := transferableProjectIDs(ctx, tx, in.FromOwnerID)
		if err != nil {
			return err
		}
		for _, id := range candidates {
			ok, err := transferProjectOwner(ctx, tx, id, in)
			if err != nil {
				return err
			}
			if ok {
				moved++
			}
		}
		return nil
	})
	return moved, err
}

// ensureActiveOwner refuses a receiver who could not be handed a project by a
// single update either: app_user carries no row scope, so membership and an
// active status are the whole check.
func ensureActiveOwner(ctx context.Context, tx pgx.Tx, owner ids.UserID) error {
	var active bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM app_user WHERE id = $1 AND status = 'active' AND archived_at IS NULL)`,
		owner).Scan(&active); err != nil {
		return fmt.Errorf("check receiving owner: %w", err)
	}
	if !active {
		return &OwnerNotActiveError{}
	}
	return nil
}

// transferableProjectIDs enumerates the live projects the from-owner holds
// that the caller may both see and write, in id order so two concurrent
// handovers lock rows in the same sequence. The visibility clause is the one
// the project list renders (auth.ScopeClauseFor); the write arm is the one
// every single-row mutation asks (auth.WritableBy), so a `read` share of a
// colleague's project lets the caller see it in the list and still leaves it
// where it is here.
func transferableProjectIDs(ctx context.Context, tx pgx.Tx, from ids.UserID) ([]ids.UUID, error) {
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	ownerPos := arg(from)
	visible, err := auth.ScopeClauseFor(ctx, projectObject, "", arg)
	if err != nil {
		return nil, err
	}
	query := fmt.Sprintf(`SELECT id FROM project WHERE owner_id = $%d AND archived_at IS NULL`, ownerPos)
	if visible != "" {
		query += " AND " + visible
	}
	rows, err := tx.Query(ctx, query+" ORDER BY id", args...)
	if err != nil {
		return nil, fmt.Errorf("list projects to transfer: %w", err)
	}
	seen, err := pgx.CollectRows(rows, pgx.RowTo[ids.UUID])
	if err != nil {
		return nil, fmt.Errorf("list projects to transfer: %w", err)
	}
	writable := make([]ids.UUID, 0, len(seen))
	for _, id := range seen {
		ok, err := auth.WritableBy(ctx, tx, projectObject, id)
		if err != nil {
			return nil, err
		}
		if ok {
			writable = append(writable, id)
		}
	}
	return writable, nil
}

// transferProjectOwner is one project's share of the handover, spelled as
// UpdateProject spells an owner change: the row patch, the `update` audit row
// with the owner_id images, and project.updated — all under the row lock.
//
// false means the row was skipped, not moved: the candidate list was read
// before the lock, and a concurrent writer may have re-owned, archived or
// withdrawn the row in between. The lock is taken first and the row re-read
// under it, so the before-image audited below is the owner the row actually
// had, never the one the scan remembered.
func transferProjectOwner(ctx context.Context, tx pgx.Tx, id ids.UUID, in TransferProjectOwnershipInput) (bool, error) {
	lock, err := storekit.LockRow(ctx, tx, projectObject, id, storekit.LiveOnly)
	if errors.Is(err, apperrors.ErrNotFound) {
		return false, nil // archived since the scan
	}
	if err != nil {
		return false, fmt.Errorf("lock project %s for transfer: %w", id, err)
	}
	still, err := stillTransferable(ctx, tx, id, in.FromOwnerID)
	if err != nil || !still {
		return false, err
	}
	p := storekit.NewPatch()
	p.Set("owner_id", in.FromOwnerID, in.ToOwnerID)
	if err := p.ApplyLocked(ctx, tx, lock); err != nil {
		return false, fmt.Errorf("move project %s to its new owner: %w", id, err)
	}
	auditID, err := storekit.Audit(ctx, tx, "update", projectObject, id, p.Before(), p.After())
	if err != nil {
		return false, fmt.Errorf("audit project transfer: %w", err)
	}
	if err := storekit.EmitEvent(ctx, tx, auditID, id, crmcontracts.PublicEventProjectUpdated{
		ChangedFields: p.After(),
	}); err != nil {
		return false, fmt.Errorf("emit project.updated: %w", err)
	}
	return true, nil
}

// stillTransferable re-asks, under the row lock, the two questions the scan
// answered before it: is the row still the from-owner's, and is it still the
// caller's to write. A row that moved between the two is left alone — the
// other writer's change stands, and no audit row claims an owner it no
// longer had.
func stillTransferable(ctx context.Context, tx pgx.Tx, id ids.UUID, from ids.UserID) (bool, error) {
	var ownedByFrom bool
	if err := tx.QueryRow(ctx,
		`SELECT owner_id = $2 FROM project WHERE id = $1 AND archived_at IS NULL`,
		id, from).Scan(&ownedByFrom); err != nil {
		return false, fmt.Errorf("re-read project %s under lock: %w", id, err)
	}
	if !ownedByFrom {
		return false, nil
	}
	return auth.WritableBy(ctx, tx, projectObject, id)
}
