// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package deals

import (
	"context"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// DealClaim is what claiming a deal answers: who owns it now, at which version.
type DealClaim struct {
	OwnerID ids.UUID
	Version int64
}

// ClaimDeal makes the calling human the owner of one deal — the deals half of
// the record claim (people owns the other three tables). Human-only, gated by
// storekit.ClaimOwnership: visible, and unowned or already the caller's to
// change. ifVersion, when given, is the If-Match compare.
func (s *Store) ClaimDeal(ctx context.Context, id ids.DealID, ifVersion *int64) (DealClaim, error) {
	if err := auth.RequireHuman(ctx); err != nil {
		return DealClaim{}, err
	}
	if err := auth.Require(ctx, "deal", principal.ActionUpdate); err != nil {
		return DealClaim{}, err
	}
	actor, err := storekit.Actor(ctx)
	if err != nil {
		return DealClaim{}, err
	}
	out := DealClaim{OwnerID: actor.UserID}
	err = s.tx(ctx, func(tx pgx.Tx) error {
		claim, auditID, err := storekit.ClaimOwnership(ctx, tx, "deal", id.UUID, actor.UserID, ifVersion)
		if err != nil {
			return err
		}
		out.Version = claim.Version
		if !claim.Changed {
			return nil
		}
		return storekit.EmitEvent(ctx, tx, auditID, id.UUID, crmcontracts.PublicEventDealUpdated{
			ChangedFields: map[string]any{"owner_id": actor.UserID},
		})
	})
	return out, err
}
