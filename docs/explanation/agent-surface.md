# The agent surface & the model runtime

How AI agents *act* inside Margince, and what *runs the model* behind them. This is the read/react
counterpart to the write path: how a proposal becomes a governed action. The **governance** — the
autonomy tiers, passports, and the one admission gate — is explained in
[authorization.md](authorization.md); this page is what the agent *does* and how the model runtime is
wired.

## Two surfaces, one gate

There are two ways an agent reaches the tool surface, and **both go through the same governed
registry and the same admission gate** — there is no privileged back door:

- **Surface A** — an *external* agent over MCP (stdio or hosted HTTP), acting under a passport.
- **Surface B** — *our own* runner: the proactive reason-act-observe loop (e.g. the overnight passes).

Both call every action through `agents.Registry.Invoke`, which admits each call through `platform/auth`
(**scope ∧ seat ∧ tier**) before any handler runs. A 🟢 call executes and is audited; a 🟡 call stages a
confirm-first approval. "Two surfaces, one gate" is a property of the construction, not a convention.

## The reason-act-observe loop (Surface B)

The runner (`internal/modules/agents/runner/`) is where **the model proposes and the governed tool
surface decides**. Each iteration:

1. **Guarantee checks first** — three hard per-run ceilings: wall-clock, a **step budget**
   (`MaxSteps`, default 40), and an **output-token budget** (`MaxOutputTokens`, default 50 000).
   Hitting one degrades the run honestly, so one unattended run can never claim the whole workspace's
   model budget.
2. **Reason** — one model call (`brain.Complete`).
3. **Parse** the proposed step — the protocol requires *exactly one* of `tool` or `final`; malformed
   output retries with feedback, and after 3 consecutive invalid steps the run **degrades honestly**
   rather than fabricate a partial result.
4. **Terminal** — a `final` step completes the run.
5. **Act** — `registry.Invoke(tool, args)` (the runner's *only* path to an action).
6. **Observe** — on the tool's result:
   - a **🟡 refusal** *suspends* the run on the staged approval (`awaiting-approval`) — it never blocks;
   - a **scope/budget refusal** is fed back as an *observation*, so the model re-plans within its
     authority;
   - **success** is observed and the loop continues.

**Resume:** when a human approves, the run re-submits the *identical* staged call carrying the approval
id; when rejected, it observes "re-plan without it." **Grounding** content seeded into the run is
spotlighted as *data, not instructions* (a prompt-injection guard). The runner reaches records **only
through the registry** — read-vs-write is governed by the gate, never by the loop itself.

## The model runtime

Behind the `ports/model` seam (`Client { Complete / Stream / Embed / Caps }`), **model choice is config,
not architecture**. `internal/modules/ai/` owns it:

- **`SelectBrain(cfg)`** turns one binding (from `ai-routing.yaml`) into a `Client` — "offline fake ↔
  API key ↔ local, one line." Providers:
  - **`anthropic`** — cloud-frontier **BYOK**: you supply the key, the product runs no inference of its own.
  - **`ollama`** and **`vllm`** — local / self-host adapters (`LocalOnly`, eligible for the zero-egress
    sovereign profile).
  - **`fake`** — a fully deterministic offline client that every test drives (records each outbound
    payload *after* stripping, so tests assert what would have left the process).
- **The Router** — tasks name *tiers*; tiers resolve to bound clients; the budget guardrail bends the
  route *before* the call; every call is metered. **Callers never pick a model.**
- **The `SecretStripper`** runs over *every* outbound payload and irreversibly removes secrets — API
  keys, tokens, private keys, password assignments (→ `[SECRET-REMOVED:<kind>]`). It is **hygiene, not
  a PII filter**: names, emails, and phone numbers pass through (privacy is handled by the location
  ladder and the erasure engines, not by stripping). The sovereign profile blocks egress entirely.
- **Metering & budget** — `ai_usage` accumulates per-(workspace, day, task, tier) counters against a
  monthly token budget. At ≥80% utilization the router soft-degrades a tier; at ≥100% it queues
  non-interactive work (`ErrBudgetExhausted`). **Core CRM is never behind this error — only model
  calls are.**

## Automations & MCP transports (in brief)

- **Automations** (`/v1/automations`) parameterize the workflow engine's closed catalog per workspace;
  mutations are human-only, re-gated at the store on the `automation` RBAC object. (The workflow engine
  itself is covered in [write-backbone.md → who consumes the events](write-backbone.md#5-the-consumer-side--groups--dedupe).)
- **MCP** serves the *same* tool surface over **A1 stdio** and **A2 hosted HTTP** — one registry, one
  admission gate, one audit stream. Running it: [how-to/run-the-mcp-server.md](../how-to/run-the-mcp-server.md);
  minting the passport: [how-to/mint-a-passport.md](../how-to/mint-a-passport.md).

## Honest gaps

- **Per-agent quota is specified but not yet enforced.** The admission gate binds scope ∧ seat ∧ tier
  today; a per-agent budget ceiling is designed but not wired.

## Where to go next

- The gate, autonomy tiers, and what a passport is: [authorization.md](authorization.md).
- Where the runner's resume trigger comes from (the `approval.decided` event):
  [write-backbone.md](write-backbone.md).
- What each module owns (`agents`, `ai`): [reference/modules.md](../reference/modules.md).
