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

```
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

`fetch` runs the production fetcher and emits the exact bytes the extraction sites are
handed — post-`StripTags` for HTML, verbatim for markdown and JSON:

```bash
make ai-probe ARGS='fetch https://openrouter.ai/api/v1/models'
```

```
fetched  media=application/json  bytes=531321  passages=1  markdown=false
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

### `--expect` is not optional for every site

`--fixture` carries what production is given; `--expect` carries what you assert about the
reply. Several sites validate the expectation **before** calling the model —
`rate_extract/fx` refuses one that is not a currency→rate map, `agent_loop` refuses a step
name no declared tool could reach. Those sites need `--expect` or `--scenario`:

```
failed    rate_extract/pricing: the expected answer is not a map of model id to its prices
          (no expectation was supplied; this site validates one — use --expect or --scenario)
```

That is the site's own message. The probe never invents an expectation to get past it.

## 4. Read the report

```
site      rate_extract/pricing   kind=one_shot   scope=full_invocation
binding   routing config/ai-routing.openrouter.example.yaml   ladder [premium,cheap_cloud]
caveat    company context not declared for this site
fixture   589194 B

call 1
  request   system 1182 B  payload 529955 B  ~133k tok  max_tokens 8192  schema 588 B
  response  in 175453 tok  out 8192 tok (HIT CAP)  20287 B  served=mistralai/mistral-large-2512  3m2.873s

evaluate  invalid — parse extraction: unexpected end of JSON input
```

| line | what it tells you |
|---|---|
| `scope=` | how much of production this exercised — read it every time |
| `binding` | which routing answered, and the tier ladder behind it |
| `caveat` | company context this DB-less lane could not assemble |
| `request` | the system prompt and payload sized separately, plus the output ceiling |
| `response` | billed usage, the served model, latency |
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

### `invalid` vs `wrong_answer` vs `failed`

These are three different problems and the report keeps them apart:

- **`failed`** — the *harness* broke: a refused fixture, a dead model. Exits non-zero.
- **`invalid`** — the production validator refused the reply (malformed, ungrounded).
- **`wrong_answer`** — the validator ACCEPTED a well-formed reply that says something other
  than what you expected.

`wrong_answer` frequently means **your expectation is wrong**, not the model's answer. When
the OpenRouter fix was verified, the first run came back:

```
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
diff ../.tmp/aitask/before/*.request.json ../.tmp/aitask/after/*.request.json
```

Remember the corpus prompts are byte-pinned: changing a shipped prompt invalidates that
site's certification record, which `make e2e-ai-report` will then show as `stale`.

## Promoting a finding

A scenario you probed is yours and stays in `.tmp/`. If it turns out to be a case the build
should keep measuring, it becomes a committed corpus scenario —
[write a certification case](write-a-certification-case.md) covers the provenance fields
(`source`, `sanitized_by`) the corpus requires and the probe deliberately does not.

## Flags

| flag | |
|---|---|
| `--site <task>/<variant>` | which site to probe (needed with `--fixture`) |
| `--scenario <file.yaml>` | fixture + expectation in the corpus format |
| `--fixture <file.json>` / `--expect <file.json>` | the two halves separately |
| `--ai-routing <path>` / `--model provider:model` / `--ai-fake` | exactly one; `--ai-fake` is free |
| `--json <path\|->` | the whole result, machine-readable |
| `--dump-request <dir>` | each post-stripper request |
| `--out <path\|->` | where this verb's artifact goes |
| `--work-dir <dir>` | artifact sink (default gitignored `.tmp/aitask`) |
| `--corpus <dir>` | corpus for `list` / `scaffold` |

The BYOK key is loaded from repo-root `.env.local`, exactly as `make e2e-ai` does.
