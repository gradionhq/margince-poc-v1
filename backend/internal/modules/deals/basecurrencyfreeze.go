// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package deals

// The base-currency freeze predicate (ADR-0085 §7). It lives here rather than
// beside the setting it guards because what makes a conversion rate "frozen"
// is this module's business, and identity — which owns the installation
// setting — may not read these tables.
//
// Compose injects it onto identity's entry, the way every cross-module edge is
// wired.

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// frozenRateTables are every table holding an `fx_rate_to_base` — the column
// that expresses one record's worth against the base in force when it froze.
// A deal freezes on close (0006's deal_closed_fx CHECK); an OFFER freezes on
// send, and that figure has already reached a customer in a PDF. Counting only
// deals would let a workspace that has sent offers and closed nothing change
// its base and silently restate every one of them.
//
// A mirrored INVOICE freezes on its issue date (FIN-PARAM-7/DM-FX-4), and that
// figure was on a document the customer has already been sent and may have
// already paid. It is the least restatable of the three: the other two are our
// own records of our own intent, while an issued invoice is a fact about money
// that moved.
//
// TestTheBaseCurrencyGuardCountsEveryFrozenRate derives this list from the
// migrations, so a future table carrying the column fails that test rather than
// quietly widening the hole.
var frozenRateTables = []string{"deal", "offer", "finance_invoice"}

// BaseCurrencyFrozen reports whether the installation's base currency has
// stopped being changeable, and says why.
//
// TWO THINGS STOP IT, and they differ in whether anyone can undo them.
//
// A frozen conversion rate is PERMANENT. It COUNTS rather than testing
// existence on purpose: "3 records have already frozen a conversion rate
// against it" tells an operator what changing the base would re-mean, where
// "immutable" tells them only that they may not. ADR-0085 §7 requires the
// refusal to name that count.
//
// A priced rate sheet is REPAIRABLE. Each `fx_rate` row records the base it
// converts INTO, and changing the base does not rewrite them — a USD→EUR rate
// cannot be restated as a USD→CHF one, so the sheet would go on being served
// beside a base it does not convert to. Clearing the sheet lifts this one, and
// the reason says so.
//
// Runs inside the caller's write transaction, so the answer cannot go stale
// between the check and the write it guards.
func BaseCurrencyFrozen(ctx context.Context, tx pgx.Tx) (bool, string, error) {
	converted, err := frozenRateCount(ctx, tx)
	if err != nil {
		return false, "", err
	}
	if converted > 0 {
		return true, fmt.Sprintf(
			"%d record(s) — closed deals and sent offers — have already frozen a conversion rate "+
				"against it, so changing the base would re-mean every roll-up built on them",
			converted), nil
	}
	priced, base, err := ratesPricedAgainstBase(ctx, tx)
	if err != nil {
		return false, "", err
	}
	if priced > 0 {
		return true, fmt.Sprintf(
			"%d rate(s) in the sheet convert into %s and cannot be restated against another base; "+
				"clear the rate sheet first", priced, base), nil
	}
	return false, "", nil
}

// frozenRateCount counts the records whose worth is already expressed against
// the current base. One is enough to freeze it; the count exists for the reason.
func frozenRateCount(ctx context.Context, tx pgx.Tx) (int, error) {
	total := 0
	for _, table := range frozenRateTables {
		var n int
		// The table name comes from the package-level list above, never from a
		// caller, so the interpolation carries nothing a request chose.
		if err := tx.QueryRow(ctx,
			`SELECT count(*) FROM `+table+` WHERE fx_rate_to_base IS NOT NULL`).Scan(&n); err != nil {
			return 0, fmt.Errorf("counting %s rows with a frozen conversion rate: %w", table, err)
		}
		total += n
	}
	return total, nil
}

// ratesPricedAgainstBase counts the sheet rows converting into the base the
// workspace currently holds, and returns that base so the reason can name it.
//
// It reads the setting row directly rather than through platform/settings,
// and NO ROW is a real answer here rather than a failure. This runs inside the
// settings write's own transaction, and on the FIRST write there is nothing
// stored yet — the refusing read would fail the probe where the honest answer
// is "no base has been set, so nothing can be priced against it". An absent
// row therefore yields zero and an empty base, which is what lets that first
// write through.
func ratesPricedAgainstBase(ctx context.Context, tx pgx.Tx) (int, string, error) {
	var base string
	var priced int
	err := tx.QueryRow(ctx, `
		SELECT s.value #>> '{}',
		       (SELECT count(*) FROM fx_rate WHERE to_currency = s.value #>> '{}')
		  FROM setting s WHERE s.key = 'installation.base_currency'`).Scan(&base, &priced)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, "", nil
	}
	if err != nil {
		return 0, "", fmt.Errorf("counting rates priced against the current base: %w", err)
	}
	return priced, base, nil
}
