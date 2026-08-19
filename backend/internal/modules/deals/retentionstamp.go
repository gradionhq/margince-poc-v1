// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package deals

// The commercial-correspondence stamp seam (A165/ADR-0114, A167/ADR-0114).
//
// When a deal reaches a commercial conclusion, the correspondence already
// filed against it becomes a Handelsbrief and must be shielded from Art. 17
// erasure for the statutory period. The stamp records that the moment it is
// EARNED, and never re-derives it — because qualification is reversible in the
// product (a won deal can be reopened, a relink drops the link a derivation
// would read) and of the two ways to be wrong only one destroys data.
//
// It runs INSIDE the transaction that concludes the deal, not on the event
// bus. A subscriber would be eventually consistent, and the gap between the
// deal closing and the stamp landing is a window in which an erasure sees
// unclassified correspondence and destroys it. There is no recovering from
// that, so the stamp commits with the thing that earned it or not at all.
//
// deals may not import activities (ADR-0054), so compose injects it.

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// StampCorrespondence marks every activity linked to this deal as commercial
// correspondence, inside the caller's transaction, and records what qualified
// it. Implementations are idempotent: the stamp is write-once at the database
// level, so re-running over an already-stamped activity leaves it alone rather
// than failing the transaction that concluded the deal.
//
// basis names why the deal qualifies — the vocabulary the evidence table
// admits for a derived qualification.
type StampCorrespondence func(ctx context.Context, tx pgx.Tx, dealID ids.DealID, basis string) error

// The two derived bases. A deal qualifies by being won, or by carrying an
// offer that has left draft — a sent Angebot documents the preparation of a
// Handelsgeschäft whether or not it closed, which is why DEPACK-PARAM-5 prices
// sent offers alongside accepted ones.
const (
	BasisDealWon          = "deal_won"
	BasisOfferBeyondDraft = "offer_beyond_draft"
)

// refusingStamp is what an un-injected seam becomes. It fails CLOSED at the
// first conclusion that needs it rather than silently leaving correspondence
// unclassified — an unstamped Handelsbrief is one the erasure destroys, and a
// seam that quietly did nothing would look exactly like one that worked.
func refusingStamp() StampCorrespondence {
	return func(context.Context, pgx.Tx, ids.DealID, string) error {
		return errors.New("deals: the StampCorrespondence seam was not injected; " +
			"construct this store with installseam.Deals(), which binds activities' " +
			"StampCorrespondenceForDeal")
	}
}
