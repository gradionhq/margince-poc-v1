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
`nl_search`, `summarize`, `transcript`, `agent_loop`, `voice_build`, and
`cert_judge` (the rubric judge is itself certified like any task). Omit `TASK=` to run the
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

## 3. Read the readiness report

```
make e2e-ai-report
```

Free, no network: it reads the census, the corpus and the JSON under `records/`,
and prints one row per shipped invocation site — including the sites nothing has
ever certified, which is the whole reason it enumerates the census rather than
the records:

```
AI certification readiness: 1 of 19 shipped sites carry a current record.

SITE                  SCOPE            STATUS  BAND       PROVIDER  MODEL             ENV   RUNS  RELIABILITY  ACCEPTED  WRONG_ANSWER  INVALID  ABSTAINED
agent_loop/loop       single_turn      absent  -          -         -                 -     -     -            -         -             -        -
cold_start/acts       single_turn      current certified  gemini    gemini-2.5-flash  byok  3     1.00         3         0             0        0
rate_extract/pricing  full_invocation  stale   certified  gemini    gemini-2.5-flash  byok  3     1.00         3         0             0        0
```

Three states, and they never collapse into each other:

- **`current`** — the record's stamp is the one this corpus computes, so its band
  describes the request this build actually sends.
- **`stale`** — a scenario changed after the run. The band is a claim about
  prompts that no longer exist; re-certify that task.
- **`absent`** — nothing has ever been measured. The columns are dashes rather
  than zeroes, because a zero is a result and this is not one.

`SCOPE` is how much of the site a run covers: `single_turn` means the scenario
seeds the window and grades the one reply that follows — the surrounding
conversation or tool loop is supplied, not exercised.

**Every row is one (provider, model, env) binding.** A `certified` band
green-lights that deployment and says nothing about another one, which is why
the binding sits in the row rather than in the file name only. The report is a
view for a human release decision, not a gate: it always exits 0, because the
lane it reports on is paid, manual and BYOK-gated.

## 4. See the prompts — trace request/response for tuning

When a task lands `not_supported` or `supported_degraded`, the verdict alone
doesn't tell you *why*. Turn on the payload trace to read exactly what each
model saw and said:

```
make e2e-ai TASK=deal_health          # trace is ON by default
```

Every candidate **and** judge call is dumped to a JSONL file under the
repo-root `.tmp/aicert/` (gitignored), and the path is printed to stdout:

```
aicert: payload trace → /…/margince-next/.tmp/aicert/aicert-trace-20260719T054005Z.jsonl
```

One JSON object per call, in the **same shape as the `ai_call_payload`
table** — `request_payload` (system + messages) and `response_payload`, both
run through the *same* SecretStripper that guards egress, so a credential in
a prompt is scrubbed before it reaches disk. Each line also carries `role`
(`candidate`/`judge`), `task`, `scenario`, `run`, `served_model`, and the
token/latency numbers, so you can pinpoint the failing run:

```json
{"task":"deal_health","role":"candidate","scenario":"…","run":1,
 "served_model":"gemini-2.5-flash",
 "request_payload":{"system":"…","messages":[…]},
 "response_payload":"{\"signals\":[{\"confidence\":\"0.9\"…"}
```

That `"0.9"` (a string where the schema wants the number `0.9`) is a typical
find: a `not_supported` verdict driven by a reply the site's own validator
refuses, not a quality problem. Read the candidate's raw output, adjust,
re-run.

The trace is **on by default** because the corpus is a fixed, hand-authored
scenario set and the content is post-stripper and written local-only — there
is nothing to leak. `TRACE=<dir>` picks a directory; `TRACE=` (empty)
turns it off.

## How the verdict is decided

Each run either **HardPasses** — the site's own production validator accepted
the reply, the reply is the answer the scenario expects, and the run stayed
inside the scenario's token/latency caps — or fails. The judge scores the
answer 0–100 against the scenario's rubric. `N` runs of one scenario fold into a verdict
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
