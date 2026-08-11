// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// A forecast moves for two reasons that leave no stage transition behind: an
// amount revised in place, and a close date slipped. deal_stage_history is
// written on creation and on a move and nowhere else, so a forecast
// reconstructed from it reconciles over stage movement and silently omits the
// rest — while presenting itself as the whole answer.
//
// The last assertion here is the one that pins a design decision rather than a
// behaviour: these rows go to their own table BECAUSE five readers hold
// deal_stage_history to its stated meaning, counting rows as movements and
// taking max(changed_at) as "when did this deal last move". If a future change
// folds this write back into that table, stalled-deal detection and every
// automation preview start lying, and nothing else in the suite would notice.

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func TestAForecastMovedWithoutAStageChangeIsRecorded(t *testing.T) {
	e := Setup(t)
	pipeline, open, _ := DealFixture(t, e)
	admin := e.Admin()
	deal := ids.From[ids.DealKind](e.SeedDeal(t, "Forecast probe", pipeline, open, &e.Rep1))

	stageRows := func(t *testing.T) int {
		t.Helper()
		return countRows(admin, t, e, `SELECT count(*) FROM deal_stage_history WHERE deal_id = $1`, deal.UUID)
	}
	forecastRows := func(t *testing.T) int {
		t.Helper()
		return countRows(admin, t, e, `SELECT count(*) FROM deal_forecast_history WHERE deal_id = $1`, deal.UUID)
	}

	stageAtStart := stageRows(t)

	// A field a forecast never reads must leave no trace here, or the table
	// answers "the forecast moved" for every edit anyone makes to a deal.
	renamed := "Forecast probe, renamed"
	if _, err := e.Deals.UpdateDeal(admin, deal, deals.UpdateDealInput{Name: &renamed}); err != nil {
		t.Fatalf("renaming the deal: %v", err)
	}
	if got := forecastRows(t); got != 0 {
		t.Fatalf("a rename wrote %d forecast rows, want 0 — a name is not a forecast field", got)
	}

	// amount_minor and currency move together or not at all — a bare number is
	// not money, and the store refuses one without the other.
	amount, currency := int64(250_000), "EUR"
	if _, err := e.Deals.UpdateDeal(admin, deal,
		deals.UpdateDealInput{AmountMinor: &amount, Currency: &currency}); err != nil {
		t.Fatalf("revising the amount: %v", err)
	}
	if got := forecastRows(t); got != 1 {
		t.Fatalf("an amount revision wrote %d forecast rows, want 1", got)
	}

	// The value recorded is the state the deal is IN after the change, which is
	// what a reconstruction asking "what was the forecast as of date T" reads
	// off the latest row at or before T. Recorded the other way round, every
	// answer would be one edit stale.
	recorded := recordedForecastAmount(admin, t, e, deal.UUID)
	if recorded == nil || *recorded != amount {
		t.Fatalf("recorded amount = %v, want %d — the row must carry the amount the deal now has", recorded, amount)
	}

	// A close date set for the first time is a slip like any other: NULL and a
	// date are different forecasts, and the move from one to the other is
	// exactly what a reconstruction needs to see.
	slipped := time.Now().UTC().AddDate(0, 1, 0)
	if _, err := e.Deals.UpdateDeal(admin, deal, deals.UpdateDealInput{ExpectedClose: &slipped}); err != nil {
		t.Fatalf("slipping the close date: %v", err)
	}
	if got := forecastRows(t); got != 2 {
		t.Fatalf("a close-date change wrote %d forecast rows in total, want 2", got)
	}

	// And the reason this is a separate table at all.
	if got := stageRows(t); got != stageAtStart {
		t.Fatalf("deal_stage_history grew from %d to %d rows without a stage change — health.go reads "+
			"max(changed_at) there as 'when did this deal last move' and the automation previews count "+
			"those rows as movements, so both now answer wrongly", stageAtStart, got)
	}
}

func countRows(ctx context.Context, t *testing.T, e *Env, query string, deal ids.UUID) int {
	t.Helper()
	var n int
	if err := database.WithWorkspaceTx(ctx, e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, query, deal).Scan(&n)
	}); err != nil {
		t.Fatalf("counting rows for %q: %v", query, err)
	}
	return n
}

// recordedForecastAmount reads back the one column whose VALUE this suite
// asserts, so the read has the column's own type rather than an any the caller
// has to get right.
func recordedForecastAmount(ctx context.Context, t *testing.T, e *Env, deal ids.UUID) *int64 {
	t.Helper()
	var amount *int64
	if err := database.WithWorkspaceTx(ctx, e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT amount_minor_at_change FROM deal_forecast_history WHERE deal_id = $1`, deal).Scan(&amount)
	}); err != nil {
		t.Fatalf("reading the recorded forecast amount: %v", err)
	}
	return amount
}
