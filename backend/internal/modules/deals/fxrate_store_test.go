// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package deals

import (
	"errors"
	"testing"
)

// The clock-free shape gates only — currency and rate. The effective-day guard
// moved into writeFxRate (sampled at write time), so its past/future behaviour
// is proven in the integration lane (TestFxRateAppendForward /
// TestFxRateRejectsPastBaseAndNonPositive) where a fixed clock is injected.
func TestNormalizeFxCurrencyRate(t *testing.T) {
	t.Run("uppercases and accepts a valid currency + rate", func(t *testing.T) {
		from, err := normalizeFxCurrencyRate(SetFxRateInput{FromCurrency: "usd", Rate: "0.92"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if from != "USD" {
			t.Errorf("from = %q, want USD", from)
		}
	})

	cases := map[string]SetFxRateInput{
		"non-3-letter currency": {FromCurrency: "US", Rate: "0.9"},
		"non-letter currency":   {FromCurrency: "U5D", Rate: "0.9"},
		"empty currency":        {FromCurrency: "", Rate: "0.9"},
		"zero rate":             {FromCurrency: "USD", Rate: "0"},
		"negative rate":         {FromCurrency: "USD", Rate: "-0.5"},
		"non-numeric rate":      {FromCurrency: "USD", Rate: "abc"},
	}
	for name, in := range cases {
		t.Run("rejects "+name, func(t *testing.T) {
			_, err := normalizeFxCurrencyRate(in)
			var v *FxRateValidationError
			if !errors.As(err, &v) {
				t.Fatalf("expected FxRateValidationError, got %v", err)
			}
		})
	}
}
