// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

// The licensed ceiling on full seats, enforced where a seat comes into use.
//
// The LICENSE half of the question is not here — what a license grants is
// resolved from the deployment file and the bundled validation module, which
// identity knows nothing about — so the composition root injects the answer and
// this module counts its own rows against it.

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
)

// SeatCeiling answers how many full seats this installation's license admits.
//
// capped is false when nothing caps them: a license that grants no seat count,
// or an unlicensed installation — which, since a production installation refuses
// to boot without a license, means a development or test one. The two are
// deliberately one answer here, because "uncapped" is the only thing identity
// can do about either.
type SeatCeiling func() (limit int, capped bool)

// WithSeatCeiling binds the ceiling seat creation is held to, wired once at
// composition from the same posture the entitlement screen and /metrics report
// — so the number an admin is refused against is the number they were shown. A
// role that wired no license posture caps nothing: it has no ceiling to
// enforce, and inventing one would refuse seats on the strength of a license
// nobody read.
//
// It binds on the SERVICE rather than on the returned handlers, because the
// refusal has to happen inside the writer's transaction and the handlers are
// not in it. That the service is shared by pointer is the point: one
// installation has one licensed ceiling, whichever surface reaches the writer.
func (h Handlers) WithSeatCeiling(ceiling SeatCeiling) Handlers {
	if h.svc == nil {
		// A handler set over no service reaches no writer — every route on it
		// fails before one — so there is no seat creation here for a ceiling to
		// hold. Answered rather than dereferenced, because a zero value staying
		// zero is not the same thing as a wiring mistake.
		return h
	}
	h.svc.seatCeiling = ceiling
	return h
}

// refuseWhenNoSeatIsLeft stops a full seat from coming into use once the
// licensed ceiling is reached. It runs INSIDE the caller's write transaction,
// before the write it guards.
//
// The lock is what makes the count mean anything. Two admins inviting at the
// same moment would otherwise both read one seat left and both take it, and the
// installation would sit one seat over a ceiling nothing will bring it back
// under. Every seat creation in this installation serializes on ONE key,
// because the ceiling is a property of the SET of seats and not of any row in
// it — so there is no finer key that would still be correct.
func (s *Service) refuseWhenNoSeatIsLeft(ctx context.Context, tx pgx.Tx) error {
	if s.seatCeiling == nil {
		return nil
	}
	limit, capped := s.seatCeiling()
	if !capped {
		return nil
	}
	if err := storekit.LockWriteIdentity(ctx, tx, seatCeilingLockEntity, seatCeilingLockIdentity); err != nil {
		return err
	}
	var used int
	if err := tx.QueryRow(ctx, fullSeatsInUseQuery).Scan(&used); err != nil {
		return fmt.Errorf("identity: counting full seats against the licensed ceiling: %w", err)
	}
	if used < limit {
		return nil
	}
	// The numbers reach the admin: "no seats left" without them is a refusal
	// nobody can act on, and both ways out of it — free one, or license more —
	// are decisions only a human makes.
	return fmt.Errorf("%w: this installation's license grants %d full seats and %d are in use; "+
		"deactivate a member, or raise the licensed seat count", apperrors.ErrSeatLimitReached, limit, used)
}

// The one lock key every seat creation takes. Named here rather than spelled at
// the call sites, because two spellings would be two locks and neither would
// serialize anything.
const (
	seatCeilingLockEntity   = "app_user_seat"
	seatCeilingLockIdentity = "licensed_ceiling"
)
