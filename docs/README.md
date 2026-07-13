# Margince documentation

Documentation for building and operating **Margince** — a governed, multi-tenant CRM (Go backend +
embedded SPA). The docs follow the [Diátaxis](https://diataxis.fr/) split: **tutorials** to learn,
**how-to** guides for tasks, **reference** for lookup, **explanation** for the *why*.

**New to the backend? Start with [tutorials/getting-started.md](tutorials/getting-started.md), then
[explanation/backend-onboarding.md](explanation/backend-onboarding.md)** — the orientation hub that
maps the codebase and links everything below.

## Map

### Tutorials — learn by doing
- [getting-started.md](tutorials/getting-started.md) — clone → running instance with a bootstrapped workspace.

### How-to — accomplish a task
- [add-an-endpoint.md](how-to/add-an-endpoint.md) — add or change an API operation (contract → gen → handler).
- [add-a-module.md](how-to/add-a-module.md) — add a new capability (module) or a cross-module edge, wired into compose.
- [apply-migrations.md](how-to/apply-migrations.md) — write and apply a database migration.
- [mint-a-passport.md](how-to/mint-a-passport.md) — issue an agent passport token.
- [run-the-mcp-server.md](how-to/run-the-mcp-server.md) — serve the governed MCP tool surface.
- [run-the-frontend.md](how-to/run-the-frontend.md) — run the SPA in dev.
- [enrich-with-a-local-llm.md](how-to/enrich-with-a-local-llm.md) — point the AI lanes at a local Ollama and enrich a company with no cloud key.

### Reference — look it up
- [modules.md](reference/modules.md) — the 16 modules: what each owns, its tables, its HTTP surface.
- [platform-toolkit.md](reference/platform-toolkit.md) — the reusable `platform/*` + `shared/*` utilities.
- [configuration.md](reference/configuration.md) — every binary flag and environment variable.
- [make-targets.md](reference/make-targets.md) — every `make` target.
- [license-release-rule.md](reference/license-release-rule.md) — the BUSL Change-Date release-stamping rule. (The per-file SPDX license *header* rule is described in [backend-onboarding.md](explanation/backend-onboarding.md) and [AGENTS.md](../AGENTS.md).)

### Explanation — understand the why
- [backend-onboarding.md](explanation/backend-onboarding.md) — **the contributor hub**: system overview, the map, what's generated vs hand-written, the store shape, the gates.
- [architecture.md](explanation/architecture.md) — the module DAG, the spine shapes, tenancy-as-structure.
- [composition-layer.md](explanation/composition-layer.md) — how `internal/compose/` boots and where every cross-module edge is wired.
- [contract-first.md](explanation/contract-first.md) — how code is generated from `crm.yaml`.
- [authorization.md](explanation/authorization.md) — why the auth check lives at the store entry point; the RLS backstop; what a passport is.
- [write-backbone.md](explanation/write-backbone.md) — storekit, `audit_log`, the outbox, and who consumes the events.
- [agent-surface.md](explanation/agent-surface.md) — the Surface-B reasoning loop and the model runtime.
- [privacy-and-consent.md](explanation/privacy-and-consent.md) — the consent gate and the GDPR engines (erasure / SAR / retention).

## Reading order for a new contributor

1. [tutorials/getting-started.md](tutorials/getting-started.md) — get it running.
2. [explanation/backend-onboarding.md](explanation/backend-onboarding.md) — the map + reading order hub.
3. [architecture.md](explanation/architecture.md) → [contract-first.md](explanation/contract-first.md) → [authorization.md](explanation/authorization.md).
4. Deep dives on demand: [write-backbone.md](explanation/write-backbone.md), [composition-layer.md](explanation/composition-layer.md), [agent-surface.md](explanation/agent-surface.md), [privacy-and-consent.md](explanation/privacy-and-consent.md), [reference/modules.md](reference/modules.md), [reference/platform-toolkit.md](reference/platform-toolkit.md).
5. [CONTRIBUTING.md](../CONTRIBUTING.md) + [AGENTS.md](../AGENTS.md) — the PR loop and the binding engineering rules.

---

*`worklists/` holds transient implementation plans and dated session notes, not product/operator
documentation — skip it as a reader.*
