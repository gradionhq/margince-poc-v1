// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package contracts

// Contract write authority, inherited from the same anchor visibility.go
// derives sight from — and inherited in BOTH directions, which is the whole
// point of this file.
//
// A contract carries no owner_id, so every gate it has is its anchor's. The
// visibility clause answers "may this caller SEE the agreement", and until this
// file existed it was also the only thing standing in front of a patch, an
// archive, a status change, a cancellation and a renewal. A manual grant widens
// visibility at either access level (that is what makes a `read` share useful),
// so a colleague handed read on a deal could rewrite, cancel and archive every
// agreement hanging off it (#1373).
//
// The anchor rule is visibility.go's, restated once here rather than re-derived:
// a contract WITH a deal is judged by that deal, and one without is judged by
// its organization. Widening a deal-anchored contract to its company would hand
// a caller agreements attached to deals they cannot see; narrowing it to both
// would refuse a legitimate editor who holds only the deal.

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// writableContract is readContract for a path that CHANGES the agreement: the
// caller must be able to see it, and their authority over its anchor must be
// write-level.
//
// The read comes first and keeps its 404, so a caller who cannot see the
// contract still cannot learn it exists by trying to change it; only a caller
// who has been shown the row is answered ErrPermissionDenied.
func writableContract(ctx context.Context, tx pgx.Tx, id ids.ContractID, asOf time.Time) (crmcontracts.Contract, error) {
	existing, err := readContract(ctx, tx, id, asOf)
	if err != nil {
		return crmcontracts.Contract{}, err
	}
	if err := ensureAnchorWritable(ctx, tx, existing); err != nil {
		return crmcontracts.Contract{}, err
	}
	return existing, nil
}

// ensureAnchorWritable applies the write-authority probe to whichever record
// the contract inherits from.
func ensureAnchorWritable(ctx context.Context, tx pgx.Tx, contract crmcontracts.Contract) error {
	if contract.DealId != nil {
		return auth.EnsureWritable(ctx, tx, "deal", ids.UUID(*contract.DealId))
	}
	return auth.EnsureWritable(ctx, tx, "organization", ids.UUID(contract.OrganizationId))
}
