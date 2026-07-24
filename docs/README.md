# Margince documentation

Documentation for building and operating **Margince** — a governed, multi-tenant CRM (a Go `/v1` API
backend; the Vite/React web UI ships separately). The docs follow the [Diátaxis](https://diataxis.fr/) split: **tutorials** to learn,
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
- [create-a-workflow.md](how-to/create-a-workflow.md) — scaffold and wire a new automation starter workflow into the closed catalog.
- [apply-migrations.md](how-to/apply-migrations.md) — write and apply a database migration.
- [mint-a-passport.md](how-to/mint-a-passport.md) — issue an agent passport token.
- [run-the-mcp-server.md](how-to/run-the-mcp-server.md) — serve the governed MCP tool surface.
- [run-the-frontend.md](how-to/run-the-frontend.md) — run the SPA in dev.
- [connect-a-mailbox.md](how-to/connect-a-mailbox.md) — connect a mailbox for capture: Gmail over OAuth (standing sync + backfill) and IMAP one-shot pull for a Gmail/Outlook mailbox with an app-password.
- [enrich-with-a-local-llm.md](how-to/enrich-with-a-local-llm.md) — point the AI lanes at a local Ollama and enrich a company with no cloud key.
- [connect-a-hubspot-overlay.md](how-to/connect-a-hubspot-overlay.md) — connect a workspace to a HubSpot portal in overlay (read + continuous sync) mode.
- [connect-a-cloud-model-provider.md](how-to/connect-a-cloud-model-provider.md) — bind the AI lanes to a BYOK cloud key (Anthropic / OpenAI / Gemini / any OpenAI-compatible vendor).
- [certify-an-ai-model.md](how-to/certify-an-ai-model.md) — certify a model against a task's scenario corpus and benchmark a candidate swap (`make e2e-ai`).
- [register-a-webhook.md](how-to/register-a-webhook.md) — register an HTTPS endpoint for Standard-Webhooks-signed, retried outbound delivery of contract-generated event payloads (curl or Settings → Integrations), and verify/inspect/replay a delivery.
- [add-an-extension.md](how-to/add-an-extension.md) — ship a stable-tier extension unit (a jurisdiction pack) under `extensions/`, composed and verified.

### Reference — look it up
- [modules.md](reference/modules.md) — the modules: what each owns, its tables, its HTTP surface.
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
- [rbac-roles-and-teams.md](explanation/rbac-roles-and-teams.md) — the role matrix, row scope (own/team/all), teams, role assignment, and per-record sharing — the data model the auth gate reads.
- [write-backbone.md](explanation/write-backbone.md) — storekit, `audit_log`, the outbox, and who consumes the events.
- [agent-surface.md](explanation/agent-surface.md) — the Surface-B reasoning loop and the model runtime.
- [ai-runtime.md](explanation/ai-runtime.md) — the AI task contract, tiers/ladders, the routing config, the one Router gate, honest tracing, and certification.
- [company-context.md](explanation/company-context.md) — the five-step onboarding wizard, the governed company profile (profile fields, facts, site reads), and how bounded company context reaches AI tasks.
- [margince-conversational-workspace-concept.md](explanation/margince-conversational-workspace-concept.md) — the implemented unified Company onboarding conversation—optional live website research, website-free collection, scoped interaction safety, and in-workspace confirmation—plus the planned reusable Margince interaction framework.
- [privacy-and-consent.md](explanation/privacy-and-consent.md) — the consent gate and the GDPR engines (erasure / SAR / retention).
- [custom-fields.md](explanation/custom-fields.md) — the one runtime `ALTER TABLE` chokepoint: the closed type/object sets, the privilege boundary, and the `fieldcatalog` seam.
- [overlay-augmentation.md](explanation/overlay-augmentation.md) — the two SoR modes, the frozen seam + inner incumbent seam, the mirror-as-cache, fail-closed visibility, and teardown for the HubSpot overlay (branch 1: read + continuous sync).
- [automation.md](explanation/automation.md) — the closed 7×7 trigger/action catalog: the two vocabularies, the one firing path, the anchor occurrence key, and both permission gates.
- [capture-connectors.md](explanation/capture-connectors.md) — the governed **ingress** surface: the connector seam (Gmail / IMAP / Graph / Calendar), the one Sink that owns every write, the grant-time scope gate, the three ingestion modes (bounded backfill, continuous sync, Gmail push), the OAuth connect/callback flow, vault-sealed credentials, and the connect UI.
- [outbound-webhooks.md](explanation/outbound-webhooks.md) — the governed egress surface: subscription config vs. delivery engine, secret sealing, the contract-first payload pipeline (`api/public-events.yaml` + `gen-payloads` + the typed `EmitEvent` seam) and its additive-only versioning, the retry/dead-letter state machine, the owner-scope fan-out gate (incl. the ratified deferred-delivery exceptions), and the Settings → Integrations UI.
- [extensibility.md](explanation/extensibility.md) — the stable extension tier: the inert compile-time declaration, the marker-allowlisted surface, the composition build, boot reconciliation, and the fitness functions that hold the boundary.

## Reading order for a new contributor

1. [tutorials/getting-started.md](tutorials/getting-started.md) — get it running.
2. [explanation/backend-onboarding.md](explanation/backend-onboarding.md) — the map + reading order hub.
3. [architecture.md](explanation/architecture.md) → [contract-first.md](explanation/contract-first.md) → [authorization.md](explanation/authorization.md).
4. Deep dives on demand: [write-backbone.md](explanation/write-backbone.md), [composition-layer.md](explanation/composition-layer.md), [agent-surface.md](explanation/agent-surface.md), [ai-runtime.md](explanation/ai-runtime.md), [company-context.md](explanation/company-context.md), [privacy-and-consent.md](explanation/privacy-and-consent.md), [custom-fields.md](explanation/custom-fields.md), [automation.md](explanation/automation.md), [capture-connectors.md](explanation/capture-connectors.md), [outbound-webhooks.md](explanation/outbound-webhooks.md), [reference/modules.md](reference/modules.md), [reference/platform-toolkit.md](reference/platform-toolkit.md).
5. [CONTRIBUTING.md](../CONTRIBUTING.md) + [AGENTS.md](../AGENTS.md) — the PR loop and the binding engineering rules.
