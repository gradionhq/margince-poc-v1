// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

// The conversion rates a multi-currency pipeline cannot close without.
//
// Winning a deal FREEZES its rate to the base currency, so a deal in USD or
// VND is refused outright until a rate exists:
//
//	no fx_rate from USD to EUR to freeze at close — an admin must load the
//	rate for this currency pair before this close can succeed
//
// That refusal is correct — a won number nobody can convert is a forecast
// nobody can add up — and it is what made this phase necessary the moment the
// dataset stopped being all-euro. Korean exhibitors bill in USD and the
// Vietnamese ones in dong.
//
// The rates are fixed rather than fetched. A demo that reached out to a rate
// provider would need a key, a network and a story about what happens when it
// is down; a demo that states its rates is reproducible and cannot drift.

import (
	"encoding/json"
	"fmt"
)

// demoFxRates are the rates the seeded currencies convert at, to the euro
// base. Approximately mid-2026 and deliberately round: these exist so a deal
// can close and a total can be added up, not so anybody can price a trade.
var demoFxRates = map[string]string{
	"USD": "0.92",
	"VND": "0.000035",
	"GBP": "1.17",
	"CHF": "1.05",
}

// seedFxRates loads a rate for every currency the plan will actually use.
//
// It must run BEFORE any deal closes. The rate is frozen at the close, so a
// missing one is not a warning the seeder can carry past — the advance is
// refused and the run stops.
func seedFxRates(c *client, refs pipelineRefs, plan map[string]profile, mode runMode) (int, error) {
	wanted := map[string]bool{}
	for _, domain := range sortedDomains(plan) {
		if currency := currencyFor(localeFor(domain)); currency != "EUR" {
			wanted[currency] = true
		}
	}
	if len(wanted) == 0 {
		return 0, nil
	}
	if mode == modeDryRun {
		return len(wanted), nil
	}

	have, err := fxCurrencies(c)
	if err != nil {
		return 0, err
	}
	created := 0
	for currency := range wanted {
		if have[currency] {
			continue
		}
		rate, ok := demoFxRates[currency]
		if !ok {
			return created, fmt.Errorf(
				"a company bills in %s and no demo rate is defined for it — add one to demoFxRates", currency)
		}
		// effective_date defaults to today and may not be in the past, so it
		// is left off: the rate is live from now, which is when the deals
		// this run closes are being closed.
		body := jsonBody{"from_currency": currency, "rate": rate}
		if err := c.post("/v1/fx-rates", body, nil); err != nil {
			if _, conflict := conflictingID(err); conflict {
				continue
			}
			return created, fmt.Errorf("setting the %s rate: %w", currency, err)
		}
		created++
	}
	return created, nil
}

// fxCurrencies is every currency the installation already holds a rate for.
func fxCurrencies(c *client) (map[string]bool, error) {
	out := map[string]bool{}
	err := c.getAll("/v1/fx-rates", nil, func(raw json.RawMessage) error {
		var rows []struct {
			FromCurrency string `json:"from_currency"`
		}
		if err := json.Unmarshal(raw, &rows); err != nil {
			return err
		}
		for _, row := range rows {
			out[row.FromCurrency] = true
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("listing fx rates: %w", err)
	}
	return out, nil
}
