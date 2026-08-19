# ADR-0020 — The product ships no inference; the customer supplies the model

**Status:** Active — the environment-variable key path and the self-hosted path
are built. Entering a model-provider key through an admin screen is **not
built**; see below.

**Decided:** 2026-06-16

## The decision

Margince runs, resells, bundles, meters and marks up no model inference. The
licence buys software: the CRM, the governed surface, the AI features and the
governance around them. It never buys tokens.

Every AI feature runs on one of two things the customer chooses. Either the
customer's own provider key, billed to them by that provider directly, or a
model the customer runs on their own hardware. In both cases the customer is the
provider's billing counterparty.

Every AI feature ships to everyone. There is no premium AI tier and no split
between bundled features and bring-your-own features. Inference is the only
line that differs.

The routing config names providers and models and never carries a credential. A
key reaches the process through the environment. A cloud binding whose key is
missing fails at startup and names the variable to set, rather than starting
with a degraded or faked client.

The non-AI product works with no model configured at all. AI features switch on
once a key or a local endpoint exists.

## Why

Bundling inference into a seat price makes the margin rest on an estimate of
tokens per seat that nobody can make confidently. If that estimate is a few
times wrong, the cost of goods moves from a rounding error to a fifth of the
seat price.

It is also the thing buyers have started refusing. The market around this
product resells intelligence by the unit, and customers keep getting bills they
did not predict. Charging only for software removes both the margin risk and the
trust problem in one move, and it matches an architecture that already had a
one-line switch between providers.

Keeping the key out of the routing file is a separate rule with its own reason.
The routing file is meant to be shared and reviewed. A secret in it stops being
reviewable the moment it is real.

## What it binds in this repository

- `backend/internal/modules/ai/selectbrain.go` reads each cloud provider's key
  from its conventional environment variable — `ANTHROPIC_API_KEY`,
  `OPENAI_API_KEY`, `GEMINI_API_KEY`, `OPENAI_COMPATIBLE_API_KEY` — through the
  `cloudKeyEnv` map. `byokKeyRequired` is the fail-closed error, and it names
  the variable.
- The routing parser in `backend/internal/modules/ai/routing.go` calls
  `dec.KnownFields(true)`, so an `api_key` field in the routing file is a parse
  error at boot rather than a silently ignored key.
- `config/ai-routing.schema.json` has no credential field on either binding
  shape; `config/ai-routing.example.yaml` says so at the top of the file.
- `ConfigItems` in `selectbrain.go` declares the four keys as secret config
  items, none of them required, because which keys an installation needs is a
  property of its routing file.
- Self-hosting is `ollama.go` and the vLLM path in the same module.
- Spend the customer can see is `backend/internal/modules/ai/usage.go`,
  `meter.go`, `pricing.go` and `budget.go`. The budget machinery reports the
  customer's own provider spend; it protects no margin of ours.

## What is owed

There is no in-product screen for entering a model-provider key. The original
decision named the sealed connector secret store — the one ADR-0048 describes,
encrypted in the application database with no read-back — as the eventual place
an administrator would type a provider key, with the process still receiving it
at boot rather than the key going back into the routing file. That surface does
not exist. Today a deployment sets the environment variables directly through
its deployment tooling. The design is not final; the open question is how a key
typed into a running installation reaches a process that reads its bindings at
startup.

## History

Adopted from the retired specification, decided 2026-06-16. Rewritten in plain
language 2026-08-19.

This record retired the funding clause of ADR-0003, which had the built-in AI
tier paid for out of the seat licence. The features survived; only the funding
changed. Amended 2026-07-17 to pin where the key lives, after downstream
readings had drifted toward putting it in the routing file.
