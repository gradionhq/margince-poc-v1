// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package integrations

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/ports/provider"
)

// reserve holds this run's worst-case cost, per pool, before anything is
// submitted (PI-FORM-1). It returns the skip reason when the run cannot be
// afforded, or "" when every touched pool is reserved.
//
// Three properties matter, and each is protecting a charge the customer did
// not authorize:
//
//   - The WHOLE worst case is held, including every cascade the frozen policy
//     permits. Reserving only the primary pass would let a run submit and then
//     discover it cannot afford its own fallback.
//   - Pools are locked in a fixed alphabetical order, so two runs touching the
//     same two pools cannot deadlock by taking them in opposite orders.
//   - It is all-or-nothing. A partial reservation rolls back with the
//     transaction and the run is skipped, never half-submitted.
func (s *Store) reserve(ctx context.Context, tx pgx.Tx, desc provider.Descriptor,
	conn admittedConnection, runID string, cats []provider.Category) (provider.SkipReason, error) {

	cost, err := desc.WorstCase(cats)
	if err != nil {
		return "", fmt.Errorf("integrations: pricing the run: %w", err)
	}
	if len(cost) == 0 {
		// An unmetered provider, or a selection that costs nothing. There is
		// nothing to hold, and the daily ceiling has already been applied.
		return "", nil
	}

	// Every pool is CHECKED before any pool is written. Locking and deciding
	// in one pass, then inserting in a second, is what makes the reservation
	// genuinely all-or-nothing: a run refused on its second pool would
	// otherwise leave the first pool's credits held against a skipped run that
	// will never spend them, quietly shrinking the customer's ceiling for the
	// rest of the month. The locks taken in the first pass are held until the
	// transaction ends, so nothing can move underneath the second.
	pools := provider.PoolsInLockOrder(cost)
	for _, pool := range pools {
		budget, err := s.lockPool(ctx, tx, conn.id, string(pool))
		if err != nil {
			return "", err
		}
		want := cost[pool]

		if budget.ceiling != nil {
			used, err := s.poolUsedThisMonth(ctx, tx, conn.id, string(pool))
			if err != nil {
				return "", err
			}
			if used+want > *budget.ceiling {
				return provider.SkipBudgetExhausted, nil
			}
		}
		// The balance is the one the provider last told us, never a live read:
		// reading one is an outbound call and this runs inside the transaction
		// holding these locks. An unknown balance does not block work —
		// refusing to spend because we have not looked lately would fail
		// closed on our own ignorance.
		if budget.pauseBelow != nil && budget.balance != nil {
			if *budget.balance-want < *budget.pauseBelow {
				return provider.SkipLowBalance, nil
			}
		}
	}

	for _, pool := range pools {
		if _, err := tx.Exec(ctx, `
			INSERT INTO provider_run_reservation (run_id, pool, reserved_credits)
			VALUES ($1, $2, $3)`, runID, string(pool), cost[pool]); err != nil {
			return "", fmt.Errorf("integrations: reserving %s credits: %w", pool, err)
		}
	}
	return "", nil
}

type poolBudgetRow struct {
	ceiling    *int
	pauseBelow *int
	balance    *int
}

// lockPool takes the row lock that makes concurrent reservations correct: two
// workers at the same ceiling serialize here, so the second sees the first's
// hold rather than both admitting a run the budget can only afford once.
func (s *Store) lockPool(ctx context.Context, tx pgx.Tx, connID, pool string) (poolBudgetRow, error) {
	var b poolBudgetRow
	err := tx.QueryRow(ctx, `
		SELECT monthly_ceiling, pause_below_balance, last_known_balance
		  FROM provider_connection_budget
		 WHERE connection_id = $1 AND pool = $2
		 FOR UPDATE`, connID, pool).Scan(&b.ceiling, &b.pauseBelow, &b.balance)
	if err == pgx.ErrNoRows {
		// No budget row means no ceiling was ever set for this pool, which is
		// "spend what the provider allows" rather than "spend nothing".
		return poolBudgetRow{}, nil
	}
	if err != nil {
		return poolBudgetRow{}, fmt.Errorf("integrations: locking the %s budget: %w", pool, err)
	}
	return b, nil
}

// poolUsedThisMonth is what the ceiling is measured against: credits already
// spent plus credits currently held by runs still in flight.
//
// Held reservations count. Leaving them out would let a burst of concurrent
// runs each see an empty ledger and collectively blow through the ceiling —
// the exact failure the ceiling exists to prevent.
func (s *Store) poolUsedThisMonth(ctx context.Context, tx pgx.Tx, connID, pool string) (int, error) {
	// The charge expression and the exclusions are shared with the spend
	// history (spend.go), not copied: the number a customer READS and the
	// number that REFUSES their next run come from one definition, so a page
	// can never say there is budget left while the ceiling says otherwise.
	var used int
	err := tx.QueryRow(ctx, `
		SELECT COALESCE(SUM(`+chargedCredits+`), 0)
		  FROM provider_run_reservation r
		  JOIN provider_run run ON run.id = r.run_id
		 WHERE r.pool = $2
		   AND run.provider = (SELECT provider FROM provider_connection WHERE id = $1)
		   AND run.created_at >= (date_trunc('month', now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC')
		   AND `+spendExcludedStates,
		connID, pool).Scan(&used)
	if err != nil {
		return 0, fmt.Errorf("integrations: reading %s spend: %w", pool, err)
	}
	return used, nil
}

// reconcile settles a run's holds against what the provider actually charged.
// The billing basis decides what a no-match releases: a provider that charges
// per successful result refunds it, one that charges per request does not,
// because there was no refund to pass on.
func (s *Store) reconcile(ctx context.Context, tx pgx.Tx, desc provider.Descriptor,
	runID string, spend map[provider.Pool]int, matched bool) error {

	if desc.Billing == provider.BillingPerSuccessfulResult && !matched {
		// Nothing was bought, so nothing is owed: release the whole hold.
		if _, err := tx.Exec(ctx, `
			UPDATE provider_run_reservation
			   SET actual_credits = 0, reconciled_at = now()
			 WHERE run_id = $1`, runID); err != nil {
			return fmt.Errorf("integrations: releasing the reservation: %w", err)
		}
		return nil
	}

	rows, err := tx.Query(ctx, `SELECT pool, reserved_credits FROM provider_run_reservation WHERE run_id = $1`, runID)
	if err != nil {
		return fmt.Errorf("integrations: reading the reservations to reconcile: %w", err)
	}
	held := map[string]int{}
	for rows.Next() {
		var pool string
		var reserved int
		if err := rows.Scan(&pool, &reserved); err != nil {
			rows.Close()
			return fmt.Errorf("integrations: scanning a reservation: %w", err)
		}
		held[pool] = reserved
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("integrations: reading the reservations to reconcile: %w", err)
	}

	for pool, reserved := range held {
		actual := reserved
		// The provider's own number wins where it gave one. Where it said
		// nothing, the hold stands as spent — assuming a refund nobody
		// promised would understate what the customer was charged.
		if v, ok := spend[provider.Pool(pool)]; ok {
			actual = v
		}
		// But never ABOVE the hold. The reservation is what this run was
		// authorized to spend, and actual_credits feeds poolUsedThisMonth,
		// which enforces the customer's monthly ceiling — so an adapter
		// reporting more than was held would silently consume budget later
		// runs were counting on. A vendor cannot charge this installation
		// more than it agreed to hold; if one claims to have, that is a
		// discrepancy for a human to take up with them, not a number this
		// ledger accepts.
		if actual > reserved {
			actual = reserved
		}
		if _, err := tx.Exec(ctx, `
			UPDATE provider_run_reservation
			   SET actual_credits = $3, reconciled_at = now()
			 WHERE run_id = $1 AND pool = $2`, runID, pool, actual); err != nil {
			return fmt.Errorf("integrations: reconciling %s: %w", pool, err)
		}
	}
	return nil
}
