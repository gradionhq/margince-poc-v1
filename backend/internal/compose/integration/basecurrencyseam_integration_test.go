// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The base currency a deal freezes against comes from the SETTING row, not
// from workspace.base_currency (ADR-0091 phase 4, issue #521).
//
// While both copies exist, every other test in this tree seeds them to the
// same value — so reverting the readers to the column leaves those suites
// green and the migration only LOOKS done. These tests set the two copies to
// disagree, which is the one fixture that can tell the readers apart.

import (
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// baseCurrencyOnUSD points the setting at USD while the workspace column
// stays EUR, and puts a EUR→USD rate on the sheet. The disagreement is the
// test: a reader still on the column sees base EUR.
func baseCurrencyOnUSD(t *testing.T, e *Env) {
	t.Helper()
	e.WsExec(t, `UPDATE setting SET value = '"USD"'::jsonb WHERE key = 'installation.base_currency'`)
	if n := e.WsCount(t, `SELECT count(*) FROM workspace WHERE id = $1 AND base_currency = 'EUR'`, e.WS); n != 1 {
		t.Fatal("the fixture needs the two copies to DISAGREE; workspace.base_currency is not EUR")
	}
	e.WsExec(t, `INSERT INTO fx_rate (workspace_id, from_currency, to_currency, rate, rate_date)
		VALUES ($1, 'EUR', 'USD', '1.1000000000', CURRENT_DATE - 1)`, e.WS)
}

// Closing a EUR deal freezes the EUR→USD rate. This is the assertion that
// separates the two readers: against the column the deal's own currency WOULD
// equal the base, and freezeFx short-circuits to the identity rate 1 without
// consulting the sheet at all.
func TestClosingADealFreezesAgainstTheSettingsBaseCurrency(t *testing.T) {
	e := Setup(t)
	pipeline, open, won := DealFixture(t, e)
	admin := e.Admin()
	baseCurrencyOnUSD(t, e)

	amount, currency := int64(100000), "EUR"
	d, err := e.Deals.CreateDeal(admin, deals.CreateDealInput{
		Name: "Priced in EUR, reported in USD", PipelineID: pipeline, StageID: open,
		Source: "manual", AmountMinor: &amount, Currency: &currency,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Deals.AdvanceDeal(admin,
		ids.From[ids.DealKind](ids.UUID(d.Id)), deals.AdvanceDealInput{ToStageID: won}); err != nil {
		t.Fatalf("closing as won: %v", err)
	}

	if n := e.WsCount(t, `SELECT count(*) FROM deal WHERE id = $1 AND fx_rate_to_base = 1`,
		ids.UUID(d.Id)); n != 0 {
		t.Fatal("the deal froze at the identity rate 1 — the close read the base currency " +
			"from workspace.base_currency, where EUR still is the base")
	}
	if n := e.WsCount(t, `SELECT count(*) FROM deal WHERE id = $1 AND fx_rate_to_base = 1.1`,
		ids.UUID(d.Id)); n != 1 {
		t.Error("fx_rate_to_base is not the 1.1 EUR→USD rate the sheet carries")
	}
}

// The fx sheet is listed against the base the SETTING names, so a rate priced
// into the retiring column's currency is not on it.
func TestTheFxSheetListsRatesIntoTheSettingsBaseCurrency(t *testing.T) {
	e := Setup(t)
	admin := e.Admin()
	baseCurrencyOnUSD(t, e)

	base, err := e.Deals.BaseCurrency(admin)
	if err != nil {
		t.Fatal(err)
	}
	if base != "USD" {
		t.Errorf("BaseCurrency() = %q, want USD — the fx screen is still reading the column", base)
	}
}
