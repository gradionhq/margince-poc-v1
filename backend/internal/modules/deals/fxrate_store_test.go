// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package deals

import (
	"errors"
	"testing"
	"time"
)

func TestNormalizeFxInput(t *testing.T) {
	today := time.Date(2026, 7, 23, 0, 0, 0, 0, time.UTC)

	t.Run("uppercases and accepts a valid forward-dated rate", func(t *testing.T) {
		from, err := normalizeFxInput(SetFxRateInput{FromCurrency: "usd", Rate: "0.92", EffectiveDate: today}, today)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if from != "USD" {
			t.Errorf("from = %q, want USD", from)
		}
	})

	t.Run("accepts a future effective date", func(t *testing.T) {
		if _, err := normalizeFxInput(SetFxRateInput{FromCurrency: "USD", Rate: "1", EffectiveDate: today.AddDate(0, 0, 1)}, today); err != nil {
			t.Fatalf("future date should be allowed: %v", err)
		}
	})

	cases := map[string]SetFxRateInput{
		"non-3-letter currency": {FromCurrency: "US", Rate: "0.9", EffectiveDate: today},
		"non-letter currency":   {FromCurrency: "U5D", Rate: "0.9", EffectiveDate: today},
		"empty currency":        {FromCurrency: "", Rate: "0.9", EffectiveDate: today},
		"zero rate":             {FromCurrency: "USD", Rate: "0", EffectiveDate: today},
		"negative rate":         {FromCurrency: "USD", Rate: "-0.5", EffectiveDate: today},
		"non-numeric rate":      {FromCurrency: "USD", Rate: "abc", EffectiveDate: today},
		"past effective date":   {FromCurrency: "USD", Rate: "0.9", EffectiveDate: today.AddDate(0, 0, -1)},
	}
	for name, in := range cases {
		t.Run("rejects "+name, func(t *testing.T) {
			_, err := normalizeFxInput(in, today)
			var v *FxRateValidationError
			if !errors.As(err, &v) {
				t.Fatalf("expected FxRateValidationError, got %v", err)
			}
		})
	}
}
