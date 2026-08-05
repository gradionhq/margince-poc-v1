# Certify an AI model

Prove a model is good enough for a Margince AI task **before** you bind it in
production — and benchmark a candidate swap against the one you run today. The
certification lane (`compose/aicert`) drives a hand-authored **fixture** corpus
through a real model — each site's own production request builder and production
validator, never a copy of either — scores each answer with a pinned rubric
judge, folds the runs into a `certified` / `supported_degraded` /
`not_supported` verdict, and commits the result as a JSON record.

This is the **paid, opt-in** lane: it makes real provider calls over the network,
billed to your own **BYOK** (bring-your-own-key) budget — real money on the API
key you supply, since Margince runs no inference of its own. It is a developer/CI
tool, never part of a request path.

> **Start free.** `make e2e-ai-report` ([§3](#3-read-the-readiness-report)) needs
> no key, no network and no database: it prints what every shipped site's record
> already says, including the ones nothing has ever certified. Read that before
> you spend anything — it tells you whether the run you are about to pay for is
> the one that is actually missing.

For how the model runtime itself works see
[explanation/ai-runtime.md](../explanation/ai-runtime.md); for binding a
provider see [connect-a-cloud-model-provider.md](connect-a-cloud-model-provider.md);
to add a task or a site rather than certify one that exists, see
[add-an-ai-task.md](add-an-ai-task.md).

## Prerequisites

1. A **routing config** binding the task's tier to a real provider/model.
   `make install` / `make dev` seed `config/ai-routing.yaml`; the shipped
   default binds **gemini** on `cheap_cloud` + `premium`. The lane defaults
   `MARGINCE_AI_ROUTING` to that file — override with `MARGINCE_AI_ROUTING=<path>`
   to certify a different binding without touching your dev config.

   For a second, non-Gemini binding to certify against, the tree also ships
   `config/ai-routing.openrouter.example.yaml` — one OpenRouter key reaching
   every open-weight model, with three candidates per tier ordered
   EU → China → USA:

   ```bash
   make e2e-ai TASK=cold_start \
     MARGINCE_AI_ROUTING=$PWD/config/ai-routing.openrouter.example.yaml
   ```
2. The provider's **BYOK key in the environment** — e.g. `GEMINI_API_KEY`,
   `ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, `OPENAI_COMPATIBLE_API_KEY` (the
   OpenRouter example reads that last one). Keys live in the env, never in the
   config file (a stray `api_key:` there is a boot error). Keep them in a
   gitignored `.env.local` and `source` it.
3. No database. The lane runs on the DB-less local router, so `make db-up` is
   not required.

## 1. Certify the task you run today

```bash
make e2e-ai TASK=cold_start
```

This certifies **the task's current binding** in your routing config. It runs
every scenario in the task's corpus `N` times (an odd number, with response
caching off so every run is a fresh model call), judges each answer, and prints
the verdict:

```text
cold_start: certified (reliability=1.00 score_p50=100 self_judged=false)
```

`self_judged` is `true` when the candidate and the judge resolved to the **same
served model** on every run — the model graded its own answers. It is not a
failure and does not change the verdict, but it weakens the *score*: read a
`self_judged=true` band as the deterministic pass (what the production validator
accepted) plus an opinion the candidate has an interest in. To remove the doubt,
certify against a candidate the `cert_judge` binding does not also serve.

A passing run writes/refreshes a record under
`backend/internal/compose/aicert/records/<task>/<provider>_<model>_<env>.json`.

The **task** names come from the contract (`backend/api/ai-tasks.yaml`), and only
a task the contract marks `status: shipped` can be certified: `agent_loop`,
`brief_ranking`, `capture_classify`, `capture_counterparty_verdict`,
`cert_judge` (the rubric judge is itself certified like any task), `cold_start`,
`draft_reply`, `enrich`, `offer_draft`, `rate_extract`, `signal_extract`,
`site_extract`, `site_fact_extract`, `site_triage`, `summarize`, `voice_build`.
Omit `TASK=` to run the whole corpus.

A `planned` task — one the contract declares but nothing implements
(`nl_search`, `transcript`, `deal_health`) — owns no scenarios, and
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

```bash
make e2e-ai TASK=cold_start MODEL=gemini:gemini-3.1-flash-lite
```

`MODEL=provider:model` overrides only the candidate; the **judge stays on its
own pinned `cert_judge` binding** (never the candidate's), so a cheaper
candidate can't grade itself lenient. Certify both the incumbent and the
candidate, then compare their records before you change the binding.

An override that names the tier's OWN provider keeps that tier's `base_url`, so
an `openai_compatible` candidate is a one-liner against a routing file already
pointed at the vendor:

```bash
make e2e-ai TASK=cold_start MODEL=openai_compatible:z-ai/glm-5.2 \
  MARGINCE_AI_ROUTING=$PWD/config/ai-routing.openrouter.example.yaml
```

An override that switches provider inherits no endpoint — one vendor's host
root addresses no other — so a cross-vendor candidate needs its own routing
file.

Other knobs: `RUNS=5` (odd repeat count), `MARGINCE_AI_ROUTING=<path>` (a scratch
routing file).

## 3. Read the readiness report

```bash
make e2e-ai-report
```

Free, no network: it reads the census, the corpus and the JSON under `records/`,
and prints one row per shipped invocation site — including the sites nothing has
ever certified, which is the whole reason it enumerates the census rather than
the records:

```text
AI certification readiness: 1 of 23 shipped sites carry a current record.

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
  describes the request this build actually sends. The stamp covers all three
  parts of that claim: the scenarios, the requests the sites' own code builds
  from them, and the request the grader those scores come from is sent.
- **`stale`** — a scenario changed after the run, or the code that turns it into
  a prompt did, or the grader's own prompt did. The band is a claim about
  requests that are no longer sent, or about scores a grader no longer produces;
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

```bash
make e2e-ai TASK=enrich          # trace is ON by default
```

Every candidate **and** judge call is dumped to a JSONL file under the
repo-root `.tmp/aicert/` (gitignored), and the path is printed to stdout:

> **Except a `no_payload` task**, whose content the contract forbids retaining
> whatever the capture posture says (`ai.NoPayload`; today that is
> `capture_counterparty_verdict`, which judges a counterparty's own message —
> other people's data). Its calls carry no payload, so the trace has no line
> for them and the run's `WARN … did not pass its validator/caps gate` detail
> is the only evidence of what went wrong. That is the prohibition working, not
> a gap to widen.

```text
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

## When certification passes but the field does not

A record measures a model against the CORPUS fixture. A site can be certified at
reliability 1.00 and still fail on the input production actually hands it — the
model-cost refresh did exactly that against a 530 KB provider catalog while
`rate_extract/pricing` was certified on a two-line fixture. To run a site against
real input through the same code, use
[debug an AI task](debug-an-ai-task.md) (`make ai-probe`).
