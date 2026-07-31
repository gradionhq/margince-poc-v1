// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package ai

// ADR-0067's storage-shape half, as a schema fitness function: price-on-read
// means the AI telemetry tables hold tokens and never money. The as-of-date
// resolution and the pricing arithmetic those tables feed are proven in
// ratestore_integration_test.go; this file guards the shape they read from.

import (
	"context"
	"testing"
)

// Price-on-read means the AI telemetry tables store no money at all, and
// that absence is an invariant worth gating rather than a tidiness
// preference. A stored per-call cost freezes whichever rate was on the
// sheet the day the call ran, and the sheet is explicitly expected to be
// corrected afterwards — SeedModelRates ships two rows marked NEEDS
// OPERATOR CONFIRMATION. Under price-on-read a corrected rate re-prices
// history; a stored number cannot follow the correction, and it would
// silently diverge from the two live pricers (PriceCall and the CostReport
// SQL) the moment either changes.
//
// 0100 shipped exactly such a column (estimated_cost_microusd) that no code
// ever wrote, and an all-NULL cost column beside a populated rate sheet was
// duly read as a broken pricing pipeline. 0147 dropped it; this test is why
// it cannot return by accident.
//
// The offending column list is derived from the live schema, not maintained
// here, so a future migration that adds money to a telemetry table fails
// here instead of shipping a second, staler source of cost truth. The rate
// sheet is deliberately out of scope: ai_model_rate is where prices
// legitimately live, and its per-MTok columns are rates, not stored spend.
func TestAiTelemetryTablesStoreNoMoney(t *testing.T) {
	e := setupRateStore(t)

	rows, err := e.owner.Query(context.Background(), `
		SELECT table_name, column_name
		FROM information_schema.columns
		WHERE table_schema = 'public'
		  AND table_name LIKE 'ai_call%'
		  AND (column_name LIKE '%cost%' OR column_name LIKE '%price%'
		       OR column_name LIKE '%usd%' OR column_name LIKE '%minor%'
		       OR column_name LIKE '%amount%')
		ORDER BY table_name, column_name`)
	if err != nil {
		t.Fatalf("reading telemetry columns: %v", err)
	}
	defer rows.Close()

	offenders := []string{}
	for rows.Next() {
		var table, column string
		if err := rows.Scan(&table, &column); err != nil {
			t.Fatalf("scanning column row: %v", err)
		}
		offenders = append(offenders, table+"."+column)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterating columns: %v", err)
	}

	if len(offenders) > 0 {
		t.Fatalf("AI telemetry stores money in %v — cost is computed on read from "+
			"ai_model_rate (ADR-0067) so a corrected rate re-prices history; a stored "+
			"cost freezes the rate that was current when the call ran. Price it in "+
			"PriceCall/CostReport instead of persisting it.", offenders)
	}
}
