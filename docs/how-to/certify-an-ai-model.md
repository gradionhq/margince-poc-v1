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
`backend/internal/compose/aicert/records/<task>/<provider>_<model>_<env>.json`.

The **task** names come from the contract (`backend/api/ai-tasks.yaml`), and only
a task the contract marks `status: shipped` can be certified: `agent_loop`,
`brief_ranking`, `capture_classify`, `capture_counterparty_verdict`,
`cert_judge` (the rubric judge is itself certified like any task), `cold_start`,
`draft_reply`, `enrich`, `offer_draft`, `rate_extract`, `site_extract`,
`site_fact_extract`, `voice_build`. Omit `TASK=` to run the whole corpus.

A `planned` task — one the contract declares but nothing implements
(`summarize`, `nl_search`, `transcript`, `deal_health`) — owns no scenarios, and
naming it fails the run with `task "…" has no scenarios under corpus`. That is
the point: a scenario for a prompt nobody ships would score a hand-written copy
and report the task covered, so the corpus refuses to carry one and a fitness
test (`aicert/corpus_test.go`) holds it to that in both directions.

A task is not one prompt. `cold_start` ships four invocation **sites** and
`voice_build` three, each with its own scenarios; `TASK=` selects the task, so
certifying one runs every site the task ships. The report in §3 is what breaks
a task's result back down per site.

## 2. Benchmark a candidate swap

Certify a *different* model against the same corpus, without editing your
routing config:

```
make e2e-ai TASK=cold_start MODEL=gemini:gemini-3.1-flash-lite
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

SITE                  SCOPE            STATUS  BAND       PROVIDER  MODEL             ENV   RUNS  PASSED  RELIABILITY  ACCEPTED  WRONG_ANSWER  INVALID  ABSTAINED
agent_loop/loop       single_turn      absent  -          -         -                 -     -     -       -            -         -             -        -
cold_start/acts       single_turn      current certified  gemini    gemini-3.5-flash  byok  3     3       1.00         3         0             0        0
rate_extract/pricing  full_invocation  stale   certified  gemini    gemini-3.5-flash  byok  3     3       1.00         3         0             0        0
```

**Every row's numbers are that SITE's own.** A record is written per task and a
task can ship several sites — `cold_start` ships four — so the record carries
each scenario's own counts and the row folds the ones that ran on its site. A
site the record never ran a scenario on reads `absent`, not as its sibling's
numbers.

`RUNS`/`PASSED` is how often the site did what its scenarios asked. The four
columns after `RELIABILITY` are what the site's own validator **reported**, and
they are not a pass/fail column: a run can be `ACCEPTED` and still fail, when
the scenario asked for an abstention.

Three states, and they never collapse into each other:

- **`current`** — the record's stamp is the one this build computes, so its band
  describes the request this build actually sends. The stamp covers both halves
  of that claim: the scenarios, and the requests the sites' own code builds from
  them.
- **`stale`** — a scenario changed after the run, or the code that turns it into
  a prompt did. The band is a claim about requests that are no longer sent;
  re-certify that task.
- **`absent`** — nothing has ever been measured. The columns are dashes rather
  than zeroes, because a zero is a result and this is not one.

`SCOPE` is how much of the site a run covers, from the most to the least:

- **`full_invocation`** — the run drives the whole production invocation, so
  certifying it certifies the site.
- **`single_turn`** — the scenario seeds the window and grades the one reply
  that follows; the surrounding conversation or tool loop is supplied, not
  exercised. The turns it leaves out are their own answers.
- **`single_call`** — the run makes ONE of the calls the site makes for one
  invocation. Where the site re-asks a below-floor item, asks again after an
  unreadable answer, or fans out over pages, the answer the product serves is
  assembled from calls the run never made — and the fold that assembles them is
  unmeasured too.

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
make e2e-ai TASK=enrich          # trace is ON by default
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
(`candidate`/`judge`), `task`, `scenario`, `run`, `call`, `served_model`, and the
token/latency numbers, so you can pinpoint the failing run — and the failing
call inside it, since a site may answer in several (the reply drafter sends up
to three, and the judge retries once on an unparseable score):

```json
{"task":"enrich","role":"candidate","scenario":"…","run":1,"call":1,
 "served_model":"gemini-3.5-flash",
 "request_payload":{"system":"…","messages":[…]},
 "response_payload":"{\"fields\":[{\"field\":\"title\",\"value\":\"Head of Quality\",\"evidence_snippet\":\"heads up quality assurance\"…"}
```

That `evidence_snippet` is a paraphrase, not a span the signature states
character-for-character — so the site's own evidence gate drops the field and
the run fails on a reply that is perfectly well-formed. That is the typical
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
identity is not uniform (a fallback to another model, between runs or between
the calls of one run) **voids** the record: you cannot certify a moving target.

A run is not always one model call — a site may retry, fall back, or turn a
tool loop — and everything the run is judged and charged for is pooled across
all of them: any degraded call degrades the run, and the caps, tokens, latency
and cost are the run's totals.

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
