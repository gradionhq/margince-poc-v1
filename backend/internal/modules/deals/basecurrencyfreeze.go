// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package deals

// The base-currency freeze predicate (ADR-0085 §7). It lives here rather than
// beside the setting it guards because what makes a conversion rate "frozen"
// is this module's business: `deal.fx_rate_to_base` is stamped when a deal
// closes (0006's deal_closed_fx CHECK), and identity — which owns the
// installation setting — may not read this table.
//
// Compose injects it onto identity's entry, the way every cross-module edge is
// wired.

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// BaseCurrencyFrozen reports whether the installation's base currency has
// stopped being changeable, and says how much history is at stake.
//
// It COUNTS rather than testing existence on purpose: "3 deals have already
// frozen a conversion rate against it" tells an operator what changing the
// base would re-mean, where "immutable" tells them only that they may not.
// ADR-0085 §7 requires the refusal to name that count.
//
// Runs inside the caller's write transaction, so the answer cannot go stale
// between the check and the write it guards.
func BaseCurrencyFrozen(ctx context.Context, tx pgx.Tx) (bool, string, error) {
	var converted int
	if err := tx.QueryRow(ctx,
		`SELECT count(*) FROM deal WHERE fx_rate_to_base IS NOT NULL`).Scan(&converted); err != nil {
		return false, "", fmt.Errorf("counting deals with a frozen conversion rate: %w", err)
	}
	if converted == 0 {
		return false, "", nil
	}
	return true, fmt.Sprintf(
		"%d deal(s) have already frozen a conversion rate against it, so changing the base "+
			"would re-mean every roll-up built on them", converted), nil
}
