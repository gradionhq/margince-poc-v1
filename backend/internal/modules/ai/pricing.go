// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import "time"

// ModelRate is one (provider, model, day) price line — the fx_rate-style
// as-of-date price sheet a call's usage is priced against (ADR-0067).
// Rates are micro-USD per million tokens (1 unit = 1e-6 USD / 1e6 tokens)
// so the whole pricer stays integer arithmetic end to end.
type ModelRate struct {
	Provider, ModelID                                   string
	InputPerMTokMicroUSD, OutputPerMTokMicroUSD         int64
	CacheReadPerMTokMicroUSD, CacheWritePerMTokMicroUSD int64
	EffectiveDate                                       time.Time
}

// PriceCall returns the micro-USD estimate for one call's normalized
// usage under rate r — THE money computation this package owns; nothing
// upstream of it (router, meter, adapters) knows a price exists
// (design decision 4, price-on-read).
//
// TokensIn is cache-inclusive (model.Response's pinned contract): it
// already counts both CachedTokens (a cache READ) and CacheWriteTokens
// (a cache CREATE). The plain "uncached" bucket is what's left after
// subtracting both, floored at 0 so a caller that reports CachedTokens
// (or CacheWriteTokens) larger than TokensIn — a defensive case, not a
// contract violation this package should trust blindly — never prices a
// negative number of tokens.
//
// The four buckets (uncached-in, cache-read, cache-write, out) each price
// at their own per-MTok rate, sum in micro-USD·tokens, then divide by
// 1e6 once at the end. Integer division truncates toward zero; at
// micro-USD grain (1e-6 USD) that is sub-cent noise per call and is
// intentional — CostReport performs the identical division so a
// row-by-row sum of PriceCall never drifts from the aggregate SQL.
func PriceCall(u Usage, r ModelRate) int64 {
	uncached := int64(u.TokensIn - u.CachedTokens - u.CacheWriteTokens)
	if uncached < 0 {
		uncached = 0
	}
	total := uncached*r.InputPerMTokMicroUSD +
		int64(u.CachedTokens)*r.CacheReadPerMTokMicroUSD +
		int64(u.CacheWriteTokens)*r.CacheWritePerMTokMicroUSD +
		int64(u.TokensOut)*r.OutputPerMTokMicroUSD
	return total / 1_000_000
}

// DayCost is one (calendar day, task, tier) computed cost line
// (CostReport's grouping grain — matching AIRT-WIRE-1's own day × task ×
// tier wire grain exactly, so the usage merge attaches each line to its
// one matching row instead of broadcasting a shared total across every
// tier a task ran on): the priced total for that day/task/tier's calls
// plus how many of them had no matching rate row and so contributed
// nothing to it — unpriced is always a visible count, never a silent 0
// (global constraint: cost is transparency, never a gate).
type DayCost struct {
	Day           time.Time
	Task          Task
	Tier          Tier
	CostMicroUSD  int64
	UnpricedCalls int64
}

// SeedModelRates is the source-constant seed price sheet: the cloud
// providers' published per-MTok sheet prices for the models this repo's
// example routing config (config/ai-routing.example.yaml) binds, plus
// explicit all-zero rows for the local/offline providers so a local
// deployment's cost reads as an honest 0, never "no data" (a call with
// no rate row is UNPRICED, which is a materially different signal from
// FREE — see CostReport).
//
// This is operator-editable seed data, not a live price feed: it is the
// starting price sheet a fresh workspace is seeded with (see
// SeedWorkspaceDefaultsTx), not something this package refreshes from a
// vendor API. An operator who changes provider pricing, adds a model, or
// disagrees with a starting number edits the ai_model_rate table (a new
// effective-dated row, fx_rate-style) — SeedModelRates itself only ever
// changes when a NEW deployment needs a NEW starting point.
//
// effective is the day every seeded row's effective_date carries — the
// caller's "as of" anchor (typically the day the seed runs).
func SeedModelRates(effective time.Time) []ModelRate {
	day := effective.UTC().Truncate(24 * time.Hour)
	rates := vendorSheetRates(day)
	rates = append(rates, brokerSheetRates(day)...)
	return append(rates, localZeroRates(day)...)
}

// rateOn is one price line effective on day, in micro-USD per million
// tokens for each of the four billed buckets.
func rateOn(day time.Time, provider, model string, in, out, cacheRead, cacheWrite int64) ModelRate {
	return ModelRate{
		Provider: provider, ModelID: model,
		InputPerMTokMicroUSD: in, OutputPerMTokMicroUSD: out,
		CacheReadPerMTokMicroUSD: cacheRead, CacheWritePerMTokMicroUSD: cacheWrite,
		EffectiveDate: day,
	}
}

// vendorSheetRates are the cloud vendors' own published per-MTok sheet
// prices, read from each vendor's native pricing page for the models
// config/ai-routing.example.yaml binds directly on that vendor's API.
func vendorSheetRates(day time.Time) []ModelRate {
	return []ModelRate{
		// Anthropic (native Messages API sheet prices, verified 2026-07-20):
		// cache read = 0.1x input, cache write = 1.25x input, matching
		// Anthropic's published prompt-caching multipliers across the family.
		rateOn(day, providerAnthropic, "claude-opus-4-8", 5_000_000, 25_000_000, 500_000, 6_250_000),
		rateOn(day, providerAnthropic, "claude-sonnet-4-6", 3_000_000, 15_000_000, 300_000, 3_750_000),
		// claude-haiku-4-5-20251001 is the exact dated snapshot id
		// config/ai-routing.example.yaml's commented cheap_cloud binding
		// uses — Anthropic prices per model family regardless of snapshot
		// date, so the undated family's sheet price applies verbatim.
		rateOn(day, providerAnthropic, "claude-haiku-4-5-20251001", 1_000_000, 5_000_000, 100_000, 1_250_000),

		// Gemini (verified 2026-07-20): cache read = 0.1x input; Gemini's
		// implicit context caching carries no separate write charge. Prices
		// are config/ai-routing.example.yaml's default bindings — premium
		// (gemini-3.5-flash) and cheap_cloud (gemini-3.1-flash-lite). Both
		// carry a >200k-token tier Gemini charges at a higher rate; the
		// sub-200k sheet price is seeded here as the common case.
		rateOn(day, providerGemini, "gemini-3.5-flash", 1_500_000, 9_000_000, 150_000, 0),
		rateOn(day, providerGemini, "gemini-3.1-flash-lite", 250_000, 1_500_000, 25_000, 0),

		// OpenAI: config/ai-routing.example.yaml's commented cheap_cloud
		// binding names "gpt-5-mini", which no longer appears on OpenAI's
		// current published price sheet (verified 2026-07-20) — priced here
		// at the closest current same-family sheet entry, gpt-5.4-mini
		// ($0.75 / $4.50 per MTok, cached input $0.075/MTok, no separate
		// cache-write charge). NEEDS OPERATOR CONFIRMATION: an operator who
		// actually binds gpt-5-mini (or a newer gpt-5.x-mini) should verify
		// against https://developers.openai.com/api/docs/pricing and correct
		// this row (or add the exact model id they bind) before relying on
		// the reported cost.
		rateOn(day, providerOpenAI, "gpt-5-mini", 750_000, 4_500_000, 75_000, 0),

		// Gemini embeddings: config/ai-routing.example.yaml's default
		// embeddings binding (`{ provider: gemini, model:
		// gemini-embedding-001 }`). Embeddings have no output and no cache
		// — only the input rate is nonzero. NEEDS OPERATOR CONFIRMATION:
		// gemini-embedding-001 is priced here at $0.15/MTok input (verified
		// 2026-07-20 against https://ai.google.dev/gemini-api/docs/pricing);
		// an operator relying on this cost should reconfirm against the
		// live sheet the same way the gpt-5-mini row above asks for.
		rateOn(day, providerGemini, "gemini-embedding-001", 150_000, 0, 0, 0),
	}
}

// brokerSheetRates price the models reached through the generic
// openai_compatible adapter (config/ai-routing.openrouter.example.yaml).
// Read from OpenRouter's own catalog — https://openrouter.ai/api/v1/models,
// per-token prices scaled by 1e12 — on 2026-07-31. Every model the example
// names is here, the commented jurisdiction alternates included: the file
// presents them as one-line swaps, and a swap must not silently turn the
// cost report UNPRICED. No entry publishes a cache-WRITE price, so that
// column is 0 throughout.
//
// These rows are keyed on the GENERIC provider name, because that is what a
// call on this adapter reports. An operator who points openai_compatible at
// a different broker serving the same model id inherits OpenRouter's price
// and should correct the row the same fx_rate-style way they'd correct any
// other.
func brokerSheetRates(day time.Time) []ModelRate {
	return []ModelRate{
		rateOn(day, providerOpenAICompatible, "mistralai/ministral-8b-2512", 150_000, 150_000, 15_000, 0),
		rateOn(day, providerOpenAICompatible, "mistralai/ministral-14b-2512", 200_000, 200_000, 20_000, 0),
		rateOn(day, providerOpenAICompatible, "mistralai/mistral-small-3.2-24b-instruct", 100_000, 300_000, 10_000, 0),
		rateOn(day, providerOpenAICompatible, "mistralai/mistral-large-2512", 500_000, 1_500_000, 50_000, 0),
		rateOn(day, providerOpenAICompatible, "deepseek/deepseek-v4-flash", 140_000, 280_000, 28_000, 0),
		rateOn(day, providerOpenAICompatible, "z-ai/glm-5.2", 966_000, 3_036_000, 179_400, 0),
		rateOn(day, providerOpenAICompatible, "nvidia/nemotron-3-ultra-550b-a55b", 600_000, 3_600_000, 200_000, 0),
		rateOn(day, providerOpenAICompatible, "openai/gpt-oss-20b", 30_000, 130_000, 30_000, 0),
		// gpt-oss-120b publishes no cached-input price, unlike its 20b
		// sibling; 0 here is "no cache discount", and the uncached input rate
		// is what a call actually pays.
		rateOn(day, providerOpenAICompatible, "openai/gpt-oss-120b", 37_000, 170_000, 0, 0),
		// Embedding lanes have no output and no cache — only input is nonzero.
		rateOn(day, providerOpenAICompatible, "mistralai/mistral-embed-2312", 100_000, 0, 0, 0),
		rateOn(day, providerOpenAICompatible, "baai/bge-m3", 10_000, 0, 0, 0),
		rateOn(day, providerOpenAICompatible, "openai/text-embedding-3-small", 20_000, 0, 0, 0),
	}
}

// localZeroRates are the local/offline providers' explicit zero rows, so a
// local deployment prices as an honest 0, never "unpriced". Keyed on each
// provider's own unbound-tier default model id (selectbrain.go) — an
// operator who binds a different local model adds their own zero row the
// same fx_rate-style way they'd correct any other price.
func localZeroRates(day time.Time) []ModelRate {
	return []ModelRate{
		rateOn(day, providerOllama, defaultOllamaModel, 0, 0, 0, 0),
		rateOn(day, providerVLLM, defaultVLLMModel, 0, 0, 0, 0),
		// bge-m3 is the local embedding model config/ai-routing.example.yaml
		// names for a fully-local embedder (`{ provider: ollama, model:
		// bge-m3 }`, ADR-0012's local-embed alternative) — a distinct model
		// id from defaultOllamaModel above (that one is the unbound CHAT
		// tier's default, gemma3, which is not an embedding model), so it
		// needs its own explicit zero row.
		rateOn(day, providerOllama, "bge-m3", 0, 0, 0, 0),
		// The offline fake provider carries no model id of its own — a
		// binding that omits `model:` (the common case: `{provider: fake}`)
		// resolves to model_id "" (routeMeta.model = cfg.Model, unmodified).
		rateOn(day, ProviderFake, "", 0, 0, 0, 0),
	}
}
