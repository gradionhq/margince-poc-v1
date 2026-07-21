# The AI runtime — tasks, tiers, routing, and the one gate

How every AI call in Margince is *named*, *routed*, *metered*, *traced*, and
*certified*. This is the plumbing beneath the features: the cold-start read-back,
deep-read extraction, capture classification, the agent loop, briefs — they all
speak the same task vocabulary and pass through the same Router. For what an
*agent* does with a call, see [agent-surface.md](agent-surface.md); for how the
governance gate admits it, see [authorization.md](authorization.md). This page is
the model runtime itself.

## The shape at a glance

```
 WHAT (contract, a rebuild)            WHERE (config, runtime)          THE GATE (one path)
 ─────────────────────────            ─────────────────────           ───────────────────
 backend/api/ai-tasks.yaml            config/ai-routing.yaml           ai.Router
   task  → ladder of tiers              tier → provider + model          • meter (workspace budget)
   + execution_mode                     profile (egress posture)         • inject company context
   + on_budget_exhausted                BYOK key ← env var               • trace (ai_call rows)
        │                                     │                          • strip secrets
        │ make gen (drift-gated)              │                          • walk the ladder
        ▼                                     ▼                                 │
   tasks_gen.go  ───────────────────────────────────────────────────────────►  │
   (compiled task/tier/ladder)         (bound at boot, validated)               ▼
                                                                          provider adapter
   task cold_start                                                        (anthropic | openai |
     ladder [cheap_cloud, premium]   ──walk on error/schema-fail──►       gemini | ollama |
     on_budget_exhausted: degrade                                          vllm | openai_compatible
                                                                           | fake)
```

**Four principles hold this together:**

1. **Contract-first.** *What* a task is — its fallback ladder, its budget posture
   — lives in `ai-tasks.yaml` and compiles into the binary. Changing policy is a
   rebuild, drift-gated exactly like `crm.yaml`. *Which* model serves a tier is
   runtime config. Policy and deployment never blur.
2. **One gate.** Every AI call — real, fake, or embedding — goes through the
   `ai.Router`. There is no second path: `--ai-fake` rides the same metered,
   traced Router (fake provider only), and two arch fitness tests fail the build
   if a model client is constructed outside it.
3. **BYOK, egress-honest.** Margince runs no inference of its own (ADR-0020). The
   key, the endpoint, and the DPA are the customer's; the `profile` names where
   inference is allowed to happen.
4. **Honest tracing.** One `ai_call` row per *attempt* — retries, degrades, and
   escalations are all visible, and a served model's identity is read from the
   wire, never overclaimed.

## The task contract

A **task** is a named AI workload — `cold_start`, `site_extract`,
`capture_classify`, `agent_loop`, and 11 more (15 in all, including the
deep-read `site_fact_extract`, the Voice-DNA `voice_build`, and the
certification `cert_judge`). Code never picks a model; it names a task, and the
Router resolves the rest.

Each task declares a **ladder** — an ordered list of capability **tiers** — an
**execution mode**, and a **budget posture**:

```yaml
# backend/api/ai-tasks.yaml
tiers: [local_small, cheap_cloud, premium, local_large]

tasks:
  cold_start:    {ladder: [cheap_cloud, premium], execution_mode: interactive, on_budget_exhausted: degrade}
  site_extract:  {ladder: [premium],              execution_mode: background,  on_budget_exhausted: queue}
  capture_classify: {ladder: [local_small, cheap_cloud], execution_mode: background, on_budget_exhausted: queue}
```

- **Tiers** are *capability classes*, not models: `local_small` / `local_large`
  (on-box, zero-egress), `cheap_cloud` (fast/cheap hosted), `premium`
  (strongest). A task's ladder is its **fallback order** — the Router starts at
  the first tier and walks to the next on a provider error or a schema-validation
  failure, so a transient failure degrades instead of dropping the call.
- **`execution_mode`** names who is waiting: `interactive` (a human, mid-flow)
  or `background` (a worker job). It pairs with the budget posture — an
  interactive task always declares `degrade`, a background task `queue` — and
  the contract's own header states the invariant.
- **`on_budget_exhausted`** is what happens when the workspace's monthly model
  budget is spent: `degrade` (answer on a cheaper rung — at 100% an interactive
  task is pinned to `local_small` rather than blocked) or `queue` (defer rather
  than overspend). A queued deferral is a **typed refusal**: the Router returns
  `BudgetDeferralError` (unwraps to `ErrBudgetDeferred`) carrying
  `NextAttemptAt` — the next budget window — **before any provider attempt or
  `ai_call` row exists**, so a deferral costs nothing and traces nothing. A
  premium-only task like `site_extract` has no cheaper rung — it queues.

`make gen` compiles this into `tasks_gen.go` (and `config/ai-routing.schema.json`);
the drift gate fails the build if the generated files don't match, so the contract
can't silently rot.

## The routing config

`config/ai-routing.yaml` is the **runtime binding** — it says which real provider
and model serves each tier, and nothing about policy. It is seeded once by
`make install` / `make dev` and then left alone (edit-and-persist).

```yaml
profile: eu_hosted            # WHERE inference may run (the egress posture)
tiers:
  local_small: {provider: ollama,  model: gemma3}
  cheap_cloud: {provider: gemini,  model: gemini-2.5-flash}
  premium:     {provider: gemini,  model: gemini-2.5-pro}
embeddings:    {provider: gemini}
```

- **`profile`** is the §4 location ladder — the privacy choice of *where* the
  model runs: `eu_hosted` (partner-operated EU inference, the default),
  `sovereign` (zero egress by construction), and so on. It constrains, it never
  leaks.
- **No key ever lives in the file.** A provider names only itself; its BYOK key is
  read from the environment at boot (`GEMINI_API_KEY`, `ANTHROPIC_API_KEY`, …). A
  stray `api_key:` in the config is a *boot error*, not a convenience.
- **A tier may be left unbound.** A deployment legitimately runs only some
  workloads. An unbound ladder isn't a startup error — but it is **loud**: boot
  warns per task (`task cold_start: no bound tier on ladder [cheap_cloud premium];
  calls will be refused`), and `/readyz` names the AI state (`configured` |
  `fake` | `unconfigured`) so an operator reads the gap off the boot log, not off
  a refused call at 3am.

Binding a tier to a provider is an edit *here*; changing a task's ladder is an
edit to the *contract* (above). Swapping gemini for a local Ollama, or pinning a
premium Sonnet, never touches code — see
[connect-a-cloud-model-provider.md](../how-to/connect-a-cloud-model-provider.md)
and [enrich-with-a-local-llm.md](../how-to/enrich-with-a-local-llm.md).

## The one gate — `ai.Router`

Every call converges on the Router (`internal/modules/ai`). In one pass it:

- **meters** the workspace's monthly model budget — derived from seat count —
  (and applies `execution_mode` + `on_budget_exhausted` when spent);
- **injects company context** where the task's policy asks for it (below);
- **strips secrets** from the prompt before the request leaves the process, and
  again from anything it records;
- **walks the ladder** — one attempt per rung, escalating on provider error or a
  structured-output schema failure;
- **traces** every attempt (below).

**Company context** is the installation's own profile (offer, ICP, voice —
what the onboarding wizard confirms) injected into task prompts as governed
data, not ad-hoc prose: a request carries typed `ContextScopes`, a
`ContextFingerprint`, and byte/token estimates (`ports/model`), all of which
land in the `ai_call` trace, key the response cache (same prompt + different
context is a different call), and surface as per-task `/metrics` counters. The
whole lane sits behind the `company_context.rollout` kill switch in
`margince.yaml` — ordered `off < read < tasks < onboarding` (default
`onboarding` = fully on; `platform/deployconfig`).

The DB-less variant `ai.NewLocalRouter` serves the same seam for offline
fixtures and the certification lane; `--ai-fake` binds the offline fake *through
the Router*, so dev and test exercise the exact metering/tracing/budget path
production does. `TestNoModelClientOutsideTheGate` and `TestOneModelPathPerRole`
(in `backend/arch_test.go`) keep it that way — the gate is a property of the
build, not a habit.

## Honest tracing — the certification grain

Every attempt writes one `ai_call` row (migration `0100`), not one row per
final answer:

- `logical_call_id` groups the attempts of one logical call; `attempt` orders
  them; `is_terminal` marks the one the caller actually received. Retries,
  degrades, and ladder escalations are all visible; metrics count terminals only.
- **`served_identity_source`** labels how the served model's identity was learned
  — `response` (the provider reported it on the wire), `echo` (a generic
  OpenAI-compatible endpoint that merely echoed the requested id), or
  `configured` (a total-failure fallback to the binding). A model can never
  *overclaim* a higher-trust source than its adapter earned.
- **Config snapshots** are hash-keyed in `ai_call_config` (task-contract hash +
  routing-config hash) — the deterministic build/deploy facts, never a key or a
  prompt.
- **Company-context provenance** rides the same row (migration `0102`): the
  context scopes, fingerprint, and size that shaped the prompt — so "what did
  the model know about us" is answerable per attempt.
- Embeddings are traced too, and their rows age out at 90 days via the privacy
  retention evaluator.

The write path is the standard one — `ai_call` + `ai_call_payload` in one
`WithWorkspaceTx`, `workspace_id` stamped from the GUC, FORCE RLS on both — so a
trace is as tenant-isolated as any domain row. See
[write-backbone.md](write-backbone.md).

## Cost — the meter collects tokens, a rate table prices them

Inference is the customer's own provider bill (ADR-0020), so cost is **transparency,
never a gate** — it is a labeled number shown *about* their spend, and the budget
guardrail above stays token-denominated. The write path reflects that: the meter and
`ai_call` collect **tokens only** and know nothing about money. Price is a *read-side*
computation (ADR-0067), so a corrected rate heals every figure and nothing rides the
model-call hot path.

```
 WRITE (tokens only)                      RATES (fx_rate-style)            READ (priced on demand)
 ──────────────────                       ────────────────────            ───────────────────────
 ai_call: tokens_in / cached_tokens       ai_model_rate                   • /ai/usage  → actuals   (phase 1)
          / cache_write_tokens              per (provider, model, day)     • backfill preview → estimate (phase 2)
          / tokens_out  (per attempt)       4 micro-USD/MTok components          │
                                            input · cache_read ·                 └─ cost = uncached_in×in + cached×read
                                            cache_write · output                          + cache_write×write + out×out
```

- **The rate table (`ai_model_rate`).** Workspace-scoped, one row per
  `(provider, model, effective_date)` — keyed on the *concrete model that
  served*, not the tier, so rebinding a tier keeps its rates. Each row is four
  integer
  **micro-USD-per-MTok** prices: input, cache-read, cache-write, output. Lookup
  works like `fx_rate` — the latest row dated on or before the call's day wins,
  and a price change is a *new* row, never an edit. Local providers get explicit
  all-zero rows, so a local call prices as an honest `0`. **Unpriced ≠ free:** a
  call whose model has no rate row is *unpriced* — still counted and surfaced,
  just flagged as a different thing from a real `$0`. Changing a price is a single
  insert; no rebuild.
- **One formula, three consumers.** The four-bucket price arithmetic is written
  once as `PriceCall`. `/ai/usage` reports **actuals** through
  `RateStore.CostReport` — SQL that mirrors that same arithmetic row-for-row, to
  price a whole window in one round-trip — while the backfill preview and the
  certification record both call `PriceCall` directly, for a **pre-flight
  estimate** and a per-run cost stamp. One formula, so the numbers can't drift.
- **The pre-flight estimate (`compose/costestimate`).** Before a backfill runs,
  the preview estimates its cost as `Σ per-task (per-unit cost × expected units)`:
  - **Per-unit cost** comes from the last 7 days of `ai_call` history, grouped
    into `(task, tier, provider, model)` slices. Each slice is priced at whichever
    model *will* serve it now: the model that served it if that's still bound,
    else its own tier's current binding (so a rebind re-prices instantly), else
    the ladder head if that tier is now unbound.
  - **Expected units** come from the connection's completed backfill yields:
    messages to classify, people to enrich, entities to embed.
  - **When there's nothing to price from, the preview says so instead of
    guessing.** With no history it falls back to a priced **work-shape floor** and
    labels the estimate `heuristic` (vs `observed`). If the whole preview would be
    unpriced it *hides* the cost field rather than show a misleading `0`, and a
    cost-read failure degrades it to a plain message count — never a block on the
    consent flow.

  *(Known rough edge, tracked: the backfill loop doesn't populate the people/org
  unit counters yet, so anything keyed on them under-counts today — the enrich
  line floors as `heuristic`, and the embed estimate counts captured-message
  embeds only, under-counting person/org entities. The cold-start floor also
  counts message embeds by design: person/org embeds would over-quote at its
  full-email unit size.)*

## Certification — proving a binding is good enough

Because a task names a contract and the model behind it is swappable, you can
**certify a model against a task before you trust it**. The cert lane
(`compose/aicert`) runs a hand-authored scenario corpus through a candidate
model, scores each answer with a fixed rubric judge on its *own* `cert_judge`
binding (never the candidate's), and folds several cache-off runs into one
verdict — `certified` / `supported_degraded` / `not_supported` — saved as JSON.
That's how you compare, say, gemini-2.5-flash against a cheaper swap on the same
rubric *before* editing the routing file. To debug a verdict, the lane can dump
every candidate and judge call to a local JSONL trace — the *same* secret-stripped
`ai_call_payload` shape (on by default, gitignored). Full walkthrough:
[how-to/certify-an-ai-model.md](../how-to/certify-an-ai-model.md).

## Reference

| Concern | Where |
|---|---|
| Task contract (tasks, tiers, ladders, budget posture) | `backend/api/ai-tasks.yaml` → `tasks_gen.go` (via `tools/gen-aitasks`, `make gen`) |
| Runtime binding (tier → provider/model, profile) | `config/ai-routing.yaml` (schema: `config/ai-routing.schema.json`) |
| BYOK keys | environment only (`GEMINI_API_KEY`, `ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, `OPENAI_COMPATIBLE_API_KEY`) |
| The gate | `internal/modules/ai` — `ai.Router` / `ai.NewLocalRouter`; `--ai-fake` flag |
| Providers | `anthropic`, `openai`, `gemini` (native) · `ollama`, `vllm`, `openai_compatible` · `fake` |
| Tracing | `ai_call` / `ai_call_payload` / `ai_call_config` (migrations `0088`, `0089`, `0100`, `0102`) |
| Cost rates | `ai_model_rate` (per provider/model, effective-dated, micro-USD; ADR-0067) · seeded by `SeedModelRates` |
| Pricer (actuals) | `PriceCall` + `RateStore` (`internal/modules/ai`) → `/ai/usage` `cost_est_minor` |
| Pre-flight estimate | `internal/compose/costestimate` (backfill preview `estimated_cost_minor` + `estimate_quality`; ADR-0068) |
| Budget deferral | `BudgetDeferralError` / `ErrBudgetDeferred` (`internal/modules/ai/budget.go`) |
| Company context | `companycontextprompt.go` (compose) · rollout switch `company_context.rollout` (`margince.yaml`, `platform/deployconfig`, migration `0105`) |
| Boot/ops surface | `/readyz` AI state; per-task unbound-ladder boot warnings |
| Certification | `internal/compose/aicert` — `make e2e-ai`, `make e2e-ai-report` |

**Related:** [agent-surface.md](agent-surface.md) (what agents do with a call) ·
[authorization.md](authorization.md) (the admission gate) ·
[how-to/connect-a-cloud-model-provider.md](../how-to/connect-a-cloud-model-provider.md) ·
[how-to/enrich-with-a-local-llm.md](../how-to/enrich-with-a-local-llm.md) ·
[how-to/certify-an-ai-model.md](../how-to/certify-an-ai-model.md) ·
[reference/configuration.md](../reference/configuration.md).
