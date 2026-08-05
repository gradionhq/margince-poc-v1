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
- [add-a-job.md](how-to/add-a-job.md) — declare a background job kind in the job contract, then write and register its worker.
- [add-an-rbac-object.md](how-to/add-an-rbac-object.md) — add a new RBAC object across the policy, the contract enum, the backfill migration, and the published matrix.
- [create-a-workflow.md](how-to/create-a-workflow.md) — scaffold and wire a new automation starter workflow into the closed catalog.
- [apply-migrations.md](how-to/apply-migrations.md) — write and apply a database migration.
- [mint-a-passport.md](how-to/mint-a-passport.md) — issue an agent passport token.
- [connect-an-mcp-client.md](how-to/connect-an-mcp-client.md) — connect a client to the governed MCP tool surface.
- [run-the-frontend.md](how-to/run-the-frontend.md) — run the SPA in dev.
- [connect-a-mailbox.md](how-to/connect-a-mailbox.md) — connect a mailbox for capture: Gmail OAuth (standing sync + backfill), IMAP app-password, Microsoft Graph OAuth, or Google Calendar — all standing connections.
- [enrich-with-a-local-llm.md](how-to/enrich-with-a-local-llm.md) — point the AI lanes at a local Ollama and enrich a company with no cloud key.
- [connect-telegram.md](how-to/connect-telegram.md) — bind a workspace-level Telegram bot for pull ingress and governed replies.
- [import-your-linkedin-network.md](how-to/import-your-linkedin-network.md) — import your own `Connections.csv` as graph substrate, and read the reach it buys.
- [connect-a-hubspot-overlay.md](how-to/connect-a-hubspot-overlay.md) — connect a workspace to a HubSpot portal in overlay (read + continuous sync) mode.
- [flip-an-overlay-to-native.md](how-to/flip-an-overlay-to-native.md) — the one-way overlay→native cutover: preflight, seal, the typed confirmation, and what recovery actually means.
- [connect-a-cloud-model-provider.md](how-to/connect-a-cloud-model-provider.md) — bind the AI lanes to a BYOK cloud key (Anthropic / OpenAI / Gemini / any OpenAI-compatible vendor).
- [certify-an-ai-model.md](how-to/certify-an-ai-model.md) — certify a model against a task's fixture corpus and benchmark a candidate swap (`make e2e-ai`).
- [add-an-ai-task.md](how-to/add-an-ai-task.md) — add a new AI task or invocation site: declare it in the contract, wire the lane, register the site, certify it.
- [write-a-certification-case.md](how-to/write-a-certification-case.md) — bind a site to the production request builder and validator that certify it: the test-first loop, the case interface, the three site kinds, scenario and rubric authoring, scope.
- [register-a-webhook.md](how-to/register-a-webhook.md) — register an HTTPS endpoint for Standard-Webhooks-signed, retried outbound delivery of contract-generated event payloads (curl or Settings → Integrations), and verify/inspect/replay a delivery.
- [add-an-extension.md](how-to/add-an-extension.md) — ship a stable-tier extension unit (a jurisdiction pack) under `extensions/`, composed and verified.

### Reference — look it up
- [modules.md](reference/modules.md) — the modules: what each owns, its tables, its HTTP surface.
- [agent-tools.md](reference/agent-tools.md) — the governed tool catalog: every registered tool, its tier, the passport scope it spends, egress, and overlay-mode behaviour.
- [supply-chain.md](reference/supply-chain.md) — the source-tree SBOMs, the license gate, keyless signing, and the pinned toolchain.
- [rbac-matrix.md](reference/rbac-matrix.md) — what each seeded role may do to each kind of record. Generated from the seeded policy, never hand-edited.
- [platform-toolkit.md](reference/platform-toolkit.md) — the reusable `platform/*` + `shared/*` utilities.
- [configuration.md](reference/configuration.md) — every binary flag and environment variable.
- [make-targets.md](reference/make-targets.md) — every `make` target.
- [license-release-rule.md](reference/license-release-rule.md) — the BUSL Change-Date release-stamping rule. (The per-file SPDX license *header* rule is described in [backend-onboarding.md](explanation/backend-onboarding.md) and [AGENTS.md](../AGENTS.md).)

### Explanation — understand the why

**Start here — the shape of the system**

- [backend-onboarding.md](explanation/backend-onboarding.md) — **the contributor hub**: system overview, the map, what's generated vs hand-written, the store shape, the gates.
- [architecture.md](explanation/architecture.md) — the module DAG, the spine shapes, tenancy-as-structure.
- [contract-first.md](explanation/contract-first.md) — how code is generated from `crm.yaml`.
- [authorization.md](explanation/authorization.md) — why the auth check lives at the store entry point; the RLS backstop; what a passport is.
- [rbac-roles-and-teams.md](explanation/rbac-roles-and-teams.md) — the role matrix, row scope (own/team/all), teams, role assignment, and per-record sharing — the data model the auth gate reads.

**The platform spine — how a change is written**

- [write-backbone.md](explanation/write-backbone.md) — storekit, `audit_log`, the outbox, and who consumes the events.
- [composition-layer.md](explanation/composition-layer.md) — how `internal/compose/` boots and where every cross-module edge is wired.
- [job-fleet.md](explanation/job-fleet.md) — the job contract: declaration before code, dispatchers vs workspace workers, why args name rows, and the failure vocabulary.
- [custom-fields.md](explanation/custom-fields.md) — the one runtime `ALTER TABLE` chokepoint: the closed type/object sets, the privilege boundary, and the `fieldcatalog` seam.

**Capture, messaging and privacy**

- [capture-connectors.md](explanation/capture-connectors.md) — the governed **ingress** surface: the connector seam (Gmail / IMAP / Graph / Calendar / Telegram), the one Sink that owns every write, the grant-time scope gate, the ingestion modes (bounded backfill, continuous sync, Gmail push, Telegram long poll), the OAuth connect/callback flow, vault-sealed credentials, and the connect UI.
- [mail-history-import.md](explanation/mail-history-import.md) — the bounded backward scan a fresh mailbox is offered: the scope count that reads ids and no bodies, the consent estimate (measured units × measured per-unit cost, `observed` vs `heuristic`, and why an unpriceable estimate hides rather than shows `$0`), the resumable page loop and its failure ladder, the yields a run measures about itself, and the sweeps that spend after the progress bar fills.
- [outbound-messaging.md](explanation/outbound-messaging.md) — the egress twin of capture: the staging row, the transmit-time gates, receipt-before-bookkeeping, and the channel reply.
- [outbound-webhooks.md](explanation/outbound-webhooks.md) — the governed egress surface: subscription config vs. delivery engine, secret sealing, the contract-first payload pipeline (`api/public-events.yaml` + `gen-payloads` + the typed `EmitEvent` seam) and its additive-only versioning, the retry/dead-letter state machine, the owner-scope fan-out gate (incl. the ratified deferred-delivery exceptions), and the Settings → Integrations UI.
- [privacy-and-consent.md](explanation/privacy-and-consent.md) — the consent gate and the GDPR engines (erasure / SAR / retention).

**AI, retrieval and automation**

- [ai-runtime.md](explanation/ai-runtime.md) — the AI task contract, tiers/ladders, the routing config, the one Router gate, honest tracing, and certification.
- [agent-surface.md](explanation/agent-surface.md) — the Surface-B reasoning loop and the model runtime.
- [search-and-retrieval.md](explanation/search-and-retrieval.md) — the lexical and hybrid lanes, row scope inside the query, embedding identity, and the two kinds of staleness with their two different answers.
- [relationship-graph.md](explanation/relationship-graph.md) — who on our team knows this contact: participants, the interaction projection, warmth, deal coverage and its risk rules.
- [company-context.md](explanation/company-context.md) — the cold start, the governed company profile (profile fields, facts, site reads), and how bounded company context reaches AI tasks. This is the *installation's own* company; for the company **record** page see below.
- [automation.md](explanation/automation.md) — the closed 7×7 trigger/action catalog: the two vocabularies, the one firing path, the anchor occurrence key, and both permission gates.

**The product surface**

- [frontend-architecture.md](explanation/frontend-architecture.md) — the SPA's layers, the shell and its nav rules, the colour and theme contract, the evidence mark, and the gates that fail a frontend push.
- [company-record-page.md](explanation/company-record-page.md) — the company record page: one gated 360 read, the per-viewer account brief, Ask, record-derived suggestions, the visit baseline, and why view state carries no audit row.
- [margince-conversational-workspace-concept.md](explanation/margince-conversational-workspace-concept.md) — the implemented unified Company onboarding conversation—optional live website research, website-free collection, scoped interaction safety, and in-workspace confirmation—plus the planned reusable Margince interaction framework.

**Modes and extension**

- [overlay-augmentation.md](explanation/overlay-augmentation.md) — the two SoR modes, the frozen seam + inner incumbent seam, the mirror-as-cache, fail-closed visibility, and teardown for the HubSpot overlay (branch 1: read + continuous sync).
- [extensibility.md](explanation/extensibility.md) — the stable extension tier: the inert compile-time declaration, the marker-allowlisted surface, the composition build, the `GOWORK` binding that decides which composition module the compiler links, boot reconciliation, and the fitness functions that hold the boundary.
### Operate — run it in production
- [deployment.md](deployment.md) — self-hosting: the container materials, the two-role non-superuser database model FORCE RLS requires, env-only configuration, one-host routing for `/v1` + `/mcp` + the OAuth flow, health checks, and order of operations.

## Reading order for a new contributor

1. [tutorials/getting-started.md](tutorials/getting-started.md) — get it running.
2. [explanation/backend-onboarding.md](explanation/backend-onboarding.md) — the map + reading order hub.
3. [architecture.md](explanation/architecture.md) → [contract-first.md](explanation/contract-first.md) → [authorization.md](explanation/authorization.md).
4. Then read the one page nearest the change you came to make — pick from the
   grouped **Explanation** map above; *The platform spine* is the group that
   applies to almost every backend change. Working on the SPA instead? Start at
   [frontend-architecture.md](explanation/frontend-architecture.md).
5. [CONTRIBUTING.md](../CONTRIBUTING.md) + [AGENTS.md](../AGENTS.md) — the PR loop and the binding engineering rules.
