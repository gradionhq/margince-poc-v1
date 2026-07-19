# Certify an AI model

Prove a model is good enough for a Margince AI task **before** you bind it in
production — and benchmark a candidate swap against the one you run today. The
certification lane (`compose/aicert`) drives a hand-authored scenario corpus
through a real model, scores each answer with a pinned rubric judge, folds the
runs into a `certified` / `supported_degraded` / `not_supported` verdict, and
commits the result as a JSON record.

This is the **paid, opt-in** lane: it makes real provider calls over the
network and spends your BYOK budget. It is a developer/CI tool, never part of a
request path. For how the model runtime itself works see
[explanation/agent-surface.md](../explanation/agent-surface.md); for binding a
provider see [connect-a-cloud-model-provider.md](connect-a-cloud-model-provider.md).

## Prerequisites

1. A **routing config** binding the task's tier to a real provider/model.
   `make install` / `make dev` seed `config/ai-routing.yaml`; the shipped
   default binds **gemini** on `cheap_cloud` + `premium`. The lane defaults
   `MARGINCE_AI_ROUTING` to that file — override with `MARGINCE_AI_ROUTING=<path>`
   to certify a different binding without touching your dev config.
2. The provider's **BYOK key in the environment** — e.g. `GEMINI_API_KEY`,
   `ANTHROPIC_API_KEY`, `OPENAI_API_KEY`. Keys live in the env, never in the
   config file (a stray `api_key:` there is a boot error). Keep them in a
   gitignored `.env.local` and `source` it.
3. No database. The lane runs on the DB-less local router, so `make db-up` is
   not required.

## 1. Certify the task you run today

```
make e2e-ai TASK=cold_start
```

This certifies **the task's current binding** in your routing config. It runs
every scenario in the task's corpus `N` times (odd, cache off), judges each
answer, and prints the verdict:

```
cold_start: certified (reliability=1.00 score_p50=100 self_judged=false)
```

A passing run writes/refreshes a record under
`backend/internal/compose/aicert/records/<task>/<provider>_<model>_<profile>.json`.

The **task** names come from the contract (`backend/api/ai-tasks.yaml`):
`cold_start`, `site_extract`, `site_fact_extract`, `brief_ranking`,
`offer_draft`, `capture_classify`, `enrich`, `deal_health`, `draft_reply`,
`nl_search`, `summarize`, `transcript`, `agent_loop`. Omit `TASK=` to run the
whole corpus. Seven tasks have no production call site yet — their scenarios are
documented starters, not full corpora (see [STATUS.md](../../STATUS.md)).

## 2. Benchmark a candidate swap

Certify a *different* model against the same corpus, without editing your
routing config:

```
make e2e-ai TASK=cold_start MODEL=gemini:gemini-2.5-flash-lite
```

`MODEL=provider:model` overrides only the candidate; the **judge stays on its
own pinned `cert_judge` binding** (never the candidate's), so a cheaper
candidate can't grade itself lenient. Certify both the incumbent and the
candidate, then compare their records before you change the binding.

Other knobs: `RUNS=5` (odd repeat count), `MARGINCE_AI_ROUTING=<path>` (a scratch
routing file).

## 3. Read the matrix

```
make e2e-ai-report
```

Prints every committed record as a task × provider × model table — free, no
network, reads the JSON under `records/`:

```
TASK        PROVIDER  MODEL                  VERDICT    RELIABILITY  SCORE_P50  LATENCY_P50_MS  RUNS
cold_start  gemini    gemini-2.5-flash       certified  1.00         100        5329            3
cold_start  gemini    gemini-2.5-flash-lite  certified  1.00         100        2020            3
```

## How the verdict is decided

Each run either **HardPasses** (all structural checks pass — JSON schema,
required substrings, token caps) or fails. The judge scores the answer 0–100
against the scenario's rubric. `N` runs of one scenario fold into a verdict
against the scenario's score bands (spec §5):

| Verdict | Rule |
|---|---|
| `certified` | **every** run HardPasses ∧ median score ≥ `certified_min` ∧ min score ≥ `floor` |
| `supported_degraded` | ≥ ⌈2N/3⌉ runs HardPass ∧ median score ≥ `degraded_min` |
| `not_supported` | otherwise |

**reliability** is the fraction of runs that HardPassed (0–1), reported for
every verdict — the number to trend over time. A run whose served-model
identity is not uniform across the set (a mid-set fallback to another model)
**voids** the record: you cannot certify a moving target.

## Notes

- **Reasoning models think before they answer.** Gemini 2.5 / o-series spend
  output tokens on internal thinking that counts against `maxOutputTokens`; the
  lane gives both the candidate and the judge headroom so a thinking burst
  doesn't starve the answer into a `MAX_TOKENS` stop. If you author a scenario
  with a tight `caps.max_tokens`, leave room for it.
- **Markdown-fenced JSON** is tolerated: the lane unfences ` ```json ` blocks
  the same way production parsers do.
- Records are committed artifacts — the certification proof travels with the
  code. Re-running refreshes latency/token numbers (network noise); the verdict
  is the durable signal.
