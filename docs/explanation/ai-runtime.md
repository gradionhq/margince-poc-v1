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
   task  → ladder of tiers              tier → provider + model          • meter (seat budget)
   + on_budget_exhausted                profile (egress posture)         • trace (ai_call rows)
        │                              BYOK key ← env var                • strip secrets
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
`capture_classify`, `agent_loop`, and 10 more (14 in all, including the
certification `cert_judge`). Code never picks a model; it names a task, and the
Router resolves the rest.

Each task declares a **ladder** — an ordered list of capability **tiers** — and a
**budget posture**:

```yaml
# backend/api/ai-tasks.yaml
tiers: [local_small, cheap_cloud, premium, local_large]

tasks:
  cold_start:    {ladder: [cheap_cloud, premium], on_budget_exhausted: degrade}
  site_extract:  {ladder: [premium],              on_budget_exhausted: queue}
  capture_classify: {ladder: [local_small, cheap_cloud], on_budget_exhausted: queue}
```

- **Tiers** are *capability classes*, not models: `local_small` / `local_large`
  (on-box, zero-egress), `cheap_cloud` (fast/cheap hosted), `premium`
  (strongest). A task's ladder is its **fallback order** — the Router starts at
  the first tier and walks to the next on a provider error or a schema-validation
  failure, so a transient failure degrades instead of dropping the call.
- **`on_budget_exhausted`** is what happens when the seat's model budget is spent:
  `degrade` (answer on a cheaper rung / smaller result) or `queue` (defer rather
  than overspend). A premium-only task like `site_extract` has no cheaper rung —
  it queues.

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

- **meters** the seat's model budget (and applies `on_budget_exhausted` when spent);
- **strips secrets** from the prompt before the request leaves the process, and
  again from anything it records;
- **walks the ladder** — one attempt per rung, escalating on provider error or a
  structured-output schema failure;
- **traces** every attempt (below).

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
- Embeddings are traced too, and their rows age out at 90 days via the privacy
  retention evaluator.

The write path is the standard one — `ai_call` + `ai_call_payload` in one
`WithWorkspaceTx`, `workspace_id` stamped from the GUC, FORCE RLS on both — so a
trace is as tenant-isolated as any domain row. See
[write-backbone.md](write-backbone.md).

## Certification — proving a binding is good enough

Because a task names a contract and a binding is swappable, you can **certify a
model against a task before you trust it**. The lane (`compose/aicert`) drives a
hand-authored scenario corpus through a candidate, scores each answer with a
pinned rubric judge on its *own* `cert_judge` binding (never the candidate's),
folds N odd cache-off runs into a `certified` / `supported_degraded` /
`not_supported` verdict, and commits the result as JSON. This is how you compare
gemini-2.5-flash against a cheaper swap on the same rubric before changing the
routing file. Full walkthrough:
[how-to/certify-an-ai-model.md](../how-to/certify-an-ai-model.md).

## Reference

| Concern | Where |
|---|---|
| Task contract (tasks, tiers, ladders, budget posture) | `backend/api/ai-tasks.yaml` → `tasks_gen.go` (via `tools/gen-aitasks`, `make gen`) |
| Runtime binding (tier → provider/model, profile) | `config/ai-routing.yaml` (schema: `config/ai-routing.schema.json`) |
| BYOK keys | environment only (`GEMINI_API_KEY`, `ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, `OPENAI_COMPATIBLE_API_KEY`) |
| The gate | `internal/modules/ai` — `ai.Router` / `ai.NewLocalRouter`; `--ai-fake` flag |
| Providers | `anthropic`, `openai`, `gemini` (native) · `ollama`, `vllm`, `openai_compatible` · `fake` |
| Tracing | `ai_call` / `ai_call_payload` / `ai_call_config` (migrations `0088`, `0089`, `0100`) |
| Boot/ops surface | `/readyz` AI state; per-task unbound-ladder boot warnings |
| Certification | `internal/compose/aicert` — `make e2e-ai`, `make e2e-ai-report` |

**Related:** [agent-surface.md](agent-surface.md) (what agents do with a call) ·
[authorization.md](authorization.md) (the admission gate) ·
[how-to/connect-a-cloud-model-provider.md](../how-to/connect-a-cloud-model-provider.md) ·
[how-to/enrich-with-a-local-llm.md](../how-to/enrich-with-a-local-llm.md) ·
[how-to/certify-an-ai-model.md](../how-to/certify-an-ai-model.md) ·
[reference/configuration.md](../reference/configuration.md).
