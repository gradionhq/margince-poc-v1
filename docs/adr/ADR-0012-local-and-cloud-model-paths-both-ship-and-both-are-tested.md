# ADR-0012 — Local and cloud model paths both ship, and both are tested

**Status:** Active

**Decided:** 2026-06-10

## The decision

Running every model on the customer's own hardware and running every model at a
hosted vendor are both shipping paths. Neither is a roadmap item and neither is
a fallback. Both are covered by unit and integration tests, and the AI quality
certification runs against both bindings.

The default local models are non-Chinese open weights. A Gemma-class model is
the default for a local binding. A Mistral model is the recommended swap where
an EU-origin model matters to the customer. Qwen stays selectable, because the
binding is config, but it is neither seeded nor recommended.

The routing code names capability tiers, never vendors. Which model backs a tier
is one line in the operator's routing file, so changing engines is a config edit
and not a code change.

## Why

The product sells "your data stays yours" into a market that distrusts a US
cloud dependency. A local path described as an option for regulated customers
does not answer that. Only a local path that ships and is tested does, so the
"is local actually supported?" objection has a concrete answer.

The default local models mattered separately. Seeding Chinese-origin open
weights as the sovereign default contradicts the sovereignty pitch for the
German mid-market and public-sector buyers this product targets, whatever the
technical merit of the models.

## What it binds in this repository

- `backend/internal/modules/ai/selectbrain.go` builds a client from one binding
  and is the only file naming vendors. `defaultOllamaModel` is `gemma3` and
  `defaultVLLMModel` is `google/gemma-3-12b-it`; no Qwen default exists.
- Local adapters are `ollama.go` and the vLLM path; cloud adapters are
  `anthropic.go`, `openai.go`, `gemini.go` and `openaicompat.go`. Each has its
  own test file beside it.
- `backend/internal/modules/ai/routing.go` holds `localProviders` and
  `ProviderIsLocal`, the one spelling of which providers may serve the sovereign
  zero-egress profile. `sovereignendpoint.go` enforces that a sovereign binding
  points at a same-host endpoint.
- `backend/internal/modules/ai/routing.go` refuses a cloud provider on any tier
  when the profile is `sovereign`, at config parse time rather than at the first
  model call.
- `config/ai-routing.example.yaml` and `config/ai-routing.schema.json` carry the
  operator surface: the provider enum, the per-tier bindings and the modality
  declarations.
- `backend/internal/compose/aicert/` runs the per-model quality certification
  that both bindings must pass.

## History

Adopted from the retired specification, decided 2026-06-10. Rewritten in plain
language 2026-08-19.

The record amended an earlier tier-to-model binding. The tier structure it sat
on — a small local tier, a cheap cloud tier, a premium tier, a large local tier,
and separate lanes for embeddings and speech — was left unchanged.
