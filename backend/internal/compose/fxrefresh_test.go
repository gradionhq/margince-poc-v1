// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"io"
	"log/slog"
	"testing"
)

func discardLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// collect keeps the FIRST grounded value when the page prices one currency
// twice (e.g. both directions), rather than letting a later pair silently
// overwrite it — the conflict is logged, never applied blindly.
func TestCollectKeepsFirstOnDuplicateCurrency(t *testing.T) {
	f := fxRefresh{log: discardLog()}
	// Base EUR. Both pairs resolve to USD: "1 EUR = 1.08 USD" (inverted →
	// USD→EUR 0.9259…) comes first; "1 USD = 0.90 EUR" (direct) comes second
	// and conflicts. The first must survive.
	pairs := []extractedFxPair{
		{FromCurrency: "EUR", ToCurrency: "USD", Rate: "1.08", Evidence: "s0", Confidence: "0.9"},
		{FromCurrency: "USD", ToCurrency: "EUR", Rate: "0.90", Evidence: "s1", Confidence: "0.9"},
	}
	got := f.collect("EUR", pairs, map[string]bool{"USD": true})
	if len(got) != 1 || got["USD"] != "0.9259259259" {
		t.Fatalf("collect = %v, want USD=0.9259259259 (first grounded value kept)", got)
	}
}
