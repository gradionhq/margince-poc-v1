// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package integrations

// Consumption over time (PI-FORM-3): what enrichment cost this installation,
// per calendar month and per credit pool.
//
// It answers a different question from the credit balance beside it on the
// card. That number is the PROVIDER's — how much is left to spend, and the
// customer may be spending the same credits through the provider's own app.
// This is OURS: what this installation consumed, read from the same
// reservation rows the monthly ceiling is enforced against.
//
// Those two must never disagree, which is why the charge expression below is
// the one poolUsedThisMonth uses, spelled once and shared. A spend figure
// whose arithmetic differed from the ceiling's would tell a customer they had
// budget left while the next run was refused for having none.

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// chargedCredits is what a reservation row counts as spent.
//
// An unreconciled hold counts at its full reserved amount: the provider has
// not said what it actually charged, and assuming a refund nobody promised
// would understate the bill in exactly the window where a customer is
// deciding whether to keep spending.
//
//nolint:gosec // G101 false positive: a SQL fragment naming credit columns, not a credential.
const chargedCredits = `COALESCE(r.actual_credits, r.reserved_credits)`

// spendExcludedStates are the runs that bought nothing. Nothing left the
// building for either, so neither is a charge — the same exclusion the
// ceiling makes.
const spendExcludedStates = `run.state <> 'skipped' AND run.state <> 'cancelled'`

// spendMonths is how far back the card looks: the current month plus five,
// so a reader sees a trend without the connection read growing unbounded.
const spendMonths = 6

// Both windows below convert the truncated month back to an instant with
// `AT TIME ZONE 'UTC'`. Truncating alone yields a timestamp WITHOUT a zone,
// which Postgres then reads in the session's own zone when it meets
// created_at — so a non-UTC session would draw the month boundary hours away
// from where the grouping puts it, and the spend total would disagree with the
// ceiling that refuses the next run.

// MonthlySpend is one calendar month's consumption for one pool.
type MonthlySpend struct {
	// Month is the first day of the UTC calendar month, matching the window
	// the ceiling enforces on — so "this month" means one thing across the
	// product.
	Month time.Time
	Pool  string
	// Charged is what was consumed, holds included.
	Charged int
	// Held is the part of Charged whose outcome was never learned
	// (submission_unknown, PI-AC-4). Reported SEPARATELY and never folded
	// away: the platform does not know whether those credits were spent, and
	// a total that quietly counted them either way would assert something it
	// cannot support. It is the figure a human reconciles against the
	// provider's invoice.
	Held int
	Runs int
}

// SpendByMonth reads this installation's own consumption, newest month first.
//
// Gated on the integrations READ grant, which is broad: a rep may see what
// enrichment costs. It is the installation's spend, not any subject's — the
// rows name no person, and an erasure detaches them from the one they used to
// name (PI-AC-8), which is what makes this series stable across an Art. 17
// request by construction.
func (s *Store) SpendByMonth(ctx context.Context, name string) ([]MonthlySpend, error) {
	if err := auth.Require(ctx, objectIntegrations, principal.ActionRead); err != nil {
		return nil, err
	}
	var out []MonthlySpend
	err := s.db.Tx(ctx, func(tx pgx.Tx) error {
		months, err := s.readSpendHistory(ctx, tx, name)
		out = months
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// readSpendHistory is the read itself, inside a transaction the caller already
// holds. List folds it into each connection, so the card's balance and its
// history describe one moment rather than two.
func (s *Store) readSpendHistory(ctx context.Context, tx pgx.Tx, name string) ([]MonthlySpend, error) {
	rows, err := tx.Query(ctx, `
		SELECT date_trunc('month', run.created_at AT TIME ZONE 'UTC') AS month,
		       r.pool,
		       COALESCE(SUM(`+chargedCredits+`), 0)::int AS charged,
		       COALESCE(SUM(`+chargedCredits+`) FILTER (
		         WHERE run.state = 'submission_unknown'), 0)::int AS held,
		       COUNT(DISTINCT run.id)::int AS runs
		  FROM provider_run_reservation r
		  JOIN provider_run run ON run.id = r.run_id
		 WHERE run.provider = $1
		   AND `+spendExcludedStates+`
		   AND run.created_at >= (date_trunc('month', now() AT TIME ZONE 'UTC')
		                            - make_interval(months => $2)) AT TIME ZONE 'UTC'
		 GROUP BY month, r.pool
		 ORDER BY month DESC, r.pool`, name, spendMonths-1)
	if err != nil {
		return nil, fmt.Errorf("integrations: reading the spend history: %w", err)
	}
	defer rows.Close()

	var out []MonthlySpend
	for rows.Next() {
		var m MonthlySpend
		if err := rows.Scan(&m.Month, &m.Pool, &m.Charged, &m.Held, &m.Runs); err != nil {
			return nil, fmt.Errorf("integrations: scanning a spend month: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("integrations: reading the spend history: %w", err)
	}
	return out, nil
}
