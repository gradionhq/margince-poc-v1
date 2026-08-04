# Debug an AI task against real input

`make ai-probe` runs ONE production invocation site against input you supply, through
the same code production runs, and reports every boundary between that input and the
verdict as numbers.

## When to reach for it

| you want to know | use |
|---|---|
| Is this model good enough for this prompt? | `make e2e-ai` — scores a fixed corpus, writes a record |
| Does this site survive **this** input? | **`make ai-probe`** — one site, your input, no score, no record |
| Which sites carry a certification record? | `make e2e-ai-report` |

The two are not interchangeable, and the gap between them is real. `rate_extract/pricing`
was `certified` at reliability 1.00 on `openai_compatible mistralai/mistral-large-2512`
while the model-cost refresh failed every single time against OpenRouter's live catalog.
Certification was honest — it measured the corpus fixture, which is two lines. Production
hands that site 530 KB. **A green record says nothing about an input the corpus never had.**

The probe is cheap: `list`, `scaffold` and `fetch` cost nothing, and `run --ai-fake` costs
nothing. Only `run` against a real binding calls a model, and it makes one call with no
judge and no record.

## 1. Find the site

```bash
make ai-probe ARGS='list'
```

```text
SITE                                  KIND        SCOPE            LADDER                   CORPUS
rate_extract/pricing                  one_shot    full_invocation  premium,cheap_cloud      yes
agent_loop/loop                       agent_loop  single_turn      cheap_cloud,premium      yes
capture_classify/classify             one_shot    full_invocation  local_small,cheap_cloud  yes
```

The list comes from the census (`compose.NewTaskCensus()`, built from `tasks_gen.go`), so
it cannot drift from the contract. **SCOPE is the column to read first** — see
[What a probe does not cover](#what-a-probe-does-not-cover).

## 2. Get a starting fixture

Every site takes a differently shaped fixture (`page_text` here, `pages[].text` there,
nothing web-shaped at all for `capture_classify`). Rather than read the Go types, copy the
site's corpus scenario:

```bash
make ai-probe ARGS='scaffold rate_extract/pricing'
# → .tmp/aitask/rate_extract_pricing.yaml
```

Edit the `fixture:` block, keep the shape, then run it:

```bash
make ai-probe ARGS='run --scenario ../.tmp/aitask/rate_extract_pricing.yaml --ai-fake'
```

Artifacts land in the gitignored `.tmp/aitask/` **by design**: a fetched page or a real
fixture carries whatever the source carried, and a probe must not be able to leave customer
content somewhere a commit would pick it up. `--out -` writes to stdout instead; `--out
<path>` puts it where you ask.

## 3. Feed it real input

`fetch` runs the production fetcher and emits what crosses the FETCH boundary:
HTML reduced by `StripTags`, markdown and JSON verbatim.

That is not always what a site is finally handed. A route may reduce further before
building its request — `rate_extract/pricing` narrows a JSON catalog to one passage
per bound model (see below). `fetch` shows you the input to that step, not its output.

```bash
make ai-probe ARGS='fetch https://openrouter.ai/api/v1/models'
```

```text
fetched  media=application/json  bytes=531321  passages=1  markdown=false  json=true
```

**`passages=` is the number that earns its place here.** Passages are what
`numberPassages` emits, one per non-empty line, and they are what an extracted row cites as
evidence. A body served as one long line numbers to a *single* passage however many bytes
it carries — so every row cites `[s0]` and the evidence gate has nothing to disagree with.
A byte count hides that completely.

Then assemble a fixture and probe. `--fixture` takes JSON, so a large body never has to
survive a YAML paste:

```bash
jq -n --rawfile t .tmp/aitask/fetch-openrouter.ai_api_v1_models.txt \
  '{provider:"openai_compatible",page_text:$t}' > .tmp/aitask/fx.json

make ai-probe ARGS='run --site rate_extract/pricing \
  --fixture ../.tmp/aitask/fx.json \
  --expect  ../.tmp/aitask/expect.json \
  --ai-routing ../config/ai-routing.openrouter.example.yaml'
```

### A JSON catalog needs reducing first, or you are probing the wrong shape

Read this before probing `rate_extract/pricing` against a broker catalog.

Production does **not** hand that site a raw catalog. `modelCostRefresh.extract`
reduces a JSON body to one passage per model and narrows it to the models this
deployment's routing binds on that provider — and only then builds the request. That
reduction lives in the crawl path, **outside** the certification seam the probe
drives, so pasting the raw catalog in as `page_text` reproduces the shape production
sent *before* the fix: one passage, hundreds of models, a truncated reply.

That is useful exactly once — to see the old failure — and misleading afterwards.
To probe what production now sends, reduce the body the same way first:

```bash
jq -c --argjson bound '["mistralai/mistral-large-2512","mistralai/ministral-14b-2512"]' \
  '.data[] | select(.id as $i | $bound | index($i))' \
  .tmp/aitask/fetch-openrouter.ai_api_v1_models.txt > .tmp/aitask/reduced.txt

jq -n --rawfile t .tmp/aitask/reduced.txt \
  '{provider:"openai_compatible",page_text:$t}' > .tmp/aitask/fx.json
```

The `passages` count on the request line is the quickest signal, but read it as a
heuristic rather than a proof: a reduced catalog that matched exactly **one** bound
model also yields one passage. Confirm by looking at the payload — the reduced form
carries only the model ids you bound, the raw one carries every id the vendor lists.

The reduction itself is covered by unit tests
(`internal/compose/modelratecatalog_test.go`), not by the probe — so a probe of the
raw catalog going red does **not** mean production is red.

### `--expect` is not optional for every site

`--fixture` carries what production is given; `--expect` carries what you assert about the
reply. Several sites validate the expectation **before** calling the model —
`rate_extract/fx` refuses one that is not a currency→rate map, `agent_loop` refuses a step
name no declared tool could reach. Those sites need `--expect` or `--scenario`:

```text
failed    rate_extract/pricing: the expected answer is not a map of model id to its prices: unexpected end of JSON input
          (no expectation was supplied; this site validates one — use --expect or --scenario)
```

That is the site's own message. The probe never invents an expectation to get past it.

## 4. Read the report

```text
site      rate_extract/pricing   kind=one_shot   scope=full_invocation
binding   routing config/ai-routing.openrouter.example.yaml   ladder [premium,cheap_cloud]
caveat    company context not declared for this site
fixture   589194 B

call 1
  request   system 1182 B  payload 529955 B  passages 1  ~133k tok  max_tokens 8192  schema 588 B
  response  in 175453 tok  out 8192 tok (HIT CAP)  20287 B  served=mistralai/mistral-large-2512  tier=premium  3m2.873s

evaluate  invalid — parse extraction: unexpected end of JSON input
```

| line | what it tells you |
|---|---|
| `scope=` | how much of production this exercised — read it every time |
| `binding` | which routing answered, and the tier ladder behind it |
| `caveat` | company context this DB-less lane could not assemble |
| `request` | the system prompt and payload sized separately, the **passage count**, and the output ceiling |
| `response` | billed usage, the served model, the tier that answered, latency |
| `evaluate` | what the **production validator** made of the reply |

Three things worth knowing:

- **`HIT CAP` is an inference, not a fact.** `model.Response` carries no finish reason, so
  it is derived from `OutputTokens >= MaxTokens`. A model that legitimately stopped exactly
  at the ceiling looks identical to one that was cut off. It is printed as a flag beside the
  raw numbers, never as a claim about why the provider stopped — but a site whose answer
  scales with its input hits it long before anything else goes wrong.
- **`~N tok` is `bytes/4`.** It under-reads by roughly a quarter on dense JSON (`~133k` above
  against 175,453 billed). It exists to compare orders of magnitude against a context window
  and an output cap, not to bill anyone.
- **`served=` prefers what the provider said answered** over what the routing bound. A vendor
  that silently substitutes a model is exactly what a surprising result is explained by.
- **`CACHED` would mean the call never happened.** The probe disables the result cache for
  the same reason the certification lane does, so you should never see it — if you do, the
  report is telling you a repeat was served from memory rather than measured.

### `invalid` vs `wrong_answer` vs `failed`

These are three different problems and the report keeps them apart:

- **`failed`** — the *harness* broke: a refused fixture, a dead model. Exits non-zero.
- **`invalid`** — the production validator refused the reply (malformed, ungrounded).
- **`wrong_answer`** — the validator ACCEPTED a well-formed reply that says something other
  than what you expected.

`wrong_answer` frequently means **your expectation is wrong**, not the model's answer. When
the OpenRouter fix was verified, the first run came back:

```text
evaluate  wrong_answer — cache-read 0.05 where the scenario expects cache-read 0
```

The catalog said `"input_cache_read":"0.00000005"` — 0.05 per MTok. The model had converted
correctly and the hand-written expectation was wrong. Check the source before you blame the
model.

## What a probe does not cover

Two limits come from the certification seam itself, not from this tool. It prints both on
every run so a green probe is never read as more coverage than it bought.

**Scope** (`aitasks.ScopeOf`, also in `make e2e-ai-report`):

- `full_invocation` — the whole production invocation (`rate_extract/*`, `site_extract/profile`,
  `draft_reply/reply`, `enrich/signature`, `offer_draft/draft`, `voice_build/*`, …)
- `single_turn` — the fixture seeds the window and one reply is graded (`agent_loop/loop`,
  the `cold_start` multi-turn sites)
- `single_call` — one of several calls the site makes (`capture_classify/classify`,
  `capture_counterparty_verdict/verdict`)

**Company context is never assembled**, because the lane is DB-less. It is declared for
`agent_loop`, `draft_reply`, `offer_draft` and `summarize`; for those sites you are probing
without part of the real prompt, and the caveat line says so.

## Tuning a prompt

1. `--dump-request <dir>` writes each post-`SecretStripper` request as JSON — the artifact a
   prompt edit is diffed against.
2. Edit the site's request builder in `internal/compose/certcase_*.go` or the production
   code it calls.
3. Re-run and diff. `--json <path>` gives the whole result machine-readably.

```bash
make ai-probe ARGS='run --scenario ../.tmp/aitask/s.yaml --ai-fake --dump-request ../.tmp/aitask/before'
# …edit the prompt…
make ai-probe ARGS='run --scenario ../.tmp/aitask/s.yaml --ai-fake --dump-request ../.tmp/aitask/after'

# Paths inside ARGS='…' are relative to backend/ (the root Makefile delegates
# with -C backend); your own shell is in the repo root, so diff has no ../.
nonce='s/untrusted-[0-9a-f-]{36}/untrusted-NONCE/g'
diff <(sed -E "$nonce" .tmp/aitask/before/*.request.json) \
     <(sed -E "$nonce" .tmp/aitask/after/*.request.json)
```

**The nonce substitution is required, not optional.** Every call mints a fresh
`untrusted-<uuid>` boundary marker and names it in both the system prompt and the
payload — that marker is what makes a forged delimiter inside a fetched page inert,
so it MUST differ per call. Two runs of an unchanged prompt therefore always differ
in two places. The dumps stay faithful to what was actually sent; the normalisation
happens in the diff, where it belongs.

Remember the corpus prompts are byte-pinned: changing a shipped prompt invalidates that
site's certification record, which `make e2e-ai-report` will then show as `stale`.

## Promoting a finding

A scenario you probed is yours and stays in `.tmp/`. If it turns out to be a case the build
should keep measuring, it becomes a committed corpus scenario —
[write a certification case](write-a-certification-case.md) covers the provenance fields
(`source`, `sanitized_by`) the corpus requires and the probe deliberately does not.

## Flags

One flagset serves all four verbs, so a flag a verb has no use for is accepted and
ignored rather than refused — `list --site x` prints the whole table. The verb
column is what each flag actually affects.

| flag | verbs | |
|---|---|---|
| `--site <task>/<variant>` | run, scaffold | which site to probe (needed with `--fixture`) |
| `--scenario <file.yaml>` | run | fixture + expectation in the corpus format |
| `--fixture <file.json>` / `--expect <file.json>` | run | the two halves separately |
| `--ai-routing <path>` / `--model provider:model` / `--ai-fake` | run | exactly one; `--ai-fake` is free |
| `--json <path\|->` | run | the whole result, machine-readable |
| `--dump-request <dir>` | run | each stripped request |
| `--out <path\|->` | scaffold, fetch | where this verb's artifact goes |
| `--work-dir <dir>` | scaffold, fetch, run | artifact sink (default gitignored `.tmp/aitask`) |
| `--corpus <dir>` | list, scaffold | corpus to read |

The BYOK key is loaded from repo-root `.env.local`, exactly as `make e2e-ai` does.
