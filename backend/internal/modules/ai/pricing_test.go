// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import (
	"testing"
	"time"
)

func TestPriceCall(t *testing.T) {
	cases := []struct {
		name string
		u    Usage
		r    ModelRate
		want int64
	}{
		// Anthropic-shaped: 700 in total (100 uncached + 400 read + 200 write), 50 out
		// rates: in $5/MTok=5_000_000, out $25/MTok=25_000_000, read 500_000, write 6_250_000
		// = (100×5e6 + 400×5e5 + 200×6.25e6 + 50×25e6)/1e6 = (5e8+2e8+1.25e9+1.25e9)/1e6 = 3200
		{
			"anthropic cache-heavy",
			Usage{TokensIn: 700, CachedTokens: 400, CacheWriteTokens: 200, TokensOut: 50},
			ModelRate{InputPerMTokMicroUSD: 5_000_000, OutputPerMTokMicroUSD: 25_000_000, CacheReadPerMTokMicroUSD: 500_000, CacheWritePerMTokMicroUSD: 6_250_000},
			3200,
		},
		// plain call, zero-rate local → 0
		{
			"local zero rate",
			Usage{TokensIn: 1000, TokensOut: 1000},
			ModelRate{},
			0,
		},
		// floor: cached > tokens_in can't go negative
		{
			"defensive floor",
			Usage{TokensIn: 10, CachedTokens: 50},
			ModelRate{InputPerMTokMicroUSD: 5_000_000},
			0,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := PriceCall(c.u, c.r); got != c.want {
				t.Fatalf("PriceCall(%+v, %+v) = %d, want %d", c.u, c.r, got, c.want)
			}
		})
	}
}

// TestSeedModelRatesEveryEntryIsNonNegativeAndUnique proves the seed set is
// a valid price sheet on its own terms — the fitness the brief asks for
// instead of hand-listing every model: no entry ever pays a negative
// price, and (provider, model) never collides (a duplicate would make
// SeedModelRates' insertion order silently decide which price wins).
func TestSeedModelRatesEveryEntryIsNonNegativeAndUnique(t *testing.T) {
	rates := SeedModelRates(time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC))
	if len(rates) == 0 {
		t.Fatal("SeedModelRates returned no rows")
	}
	seen := make(map[string]bool, len(rates))
	for _, r := range rates {
		if r.InputPerMTokMicroUSD < 0 || r.OutputPerMTokMicroUSD < 0 ||
			r.CacheReadPerMTokMicroUSD < 0 || r.CacheWritePerMTokMicroUSD < 0 {
			t.Errorf("%s/%s: negative rate %+v", r.Provider, r.ModelID, r)
		}
		key := r.Provider + "\x00" + r.ModelID
		if seen[key] {
			t.Errorf("duplicate (provider, model) %s/%s", r.Provider, r.ModelID)
		}
		seen[key] = true
		if r.EffectiveDate.IsZero() {
			t.Errorf("%s/%s: zero EffectiveDate", r.Provider, r.ModelID)
		}
	}
}

// TestSeedModelRatesLocalsAreZero proves every local/offline provider's
// seed row prices as an honest 0 — a local deployment must never read as
// "unpriced" for lack of a rate row (global constraint: price-on-read,
// no silent 0 for a REAL call, but locals are a real 0 by construction).
func TestSeedModelRatesLocalsAreZero(t *testing.T) {
	rates := SeedModelRates(time.Now())
	locals := map[string]bool{ProviderFake: false, providerOllama: false, providerVLLM: false}
	for _, r := range rates {
		if _, ok := locals[r.Provider]; !ok {
			continue
		}
		locals[r.Provider] = true
		if r.InputPerMTokMicroUSD != 0 || r.OutputPerMTokMicroUSD != 0 ||
			r.CacheReadPerMTokMicroUSD != 0 || r.CacheWritePerMTokMicroUSD != 0 {
			t.Errorf("local provider %s/%s carries a non-zero rate: %+v", r.Provider, r.ModelID, r)
		}
	}
	for provider, present := range locals {
		if !present {
			t.Errorf("no seed row for local provider %q", provider)
		}
	}
}
