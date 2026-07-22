# Module catalog

The nineteen bounded capabilities under `backend/internal/modules/`. This is the "what owns what" map
— use it to find the module a change belongs to, or to place a new one. For the *why* of the module
boundary (the DAG, the two spine shapes), see [explanation/architecture.md](../explanation/architecture.md);
for the store/write mechanics every module shares, see
[explanation/write-backbone.md](../explanation/write-backbone.md).

## The rules every module follows

- **Flat by default** — store + contract mapping + transport handlers + provider slice in one package.
  A module earns a subpackage only under a real need (a protocol adapter, an independent engine, a
  hidden ruleset).
- **A module never imports a sibling.** If capability A needs B, the edge is injected at the
  composition root (`internal/compose/server.go`) via an adapter — never `import ".../modules/b"`.
  Three gates enforce this ([backend-onboarding.md → gates](../explanation/backend-onboarding.md#the-gates-that-judge-your-pr-fitness-functions)).
- **A module writes only the tables it owns.** The "Owns tables" column below is the ownership
  declared in each module's `doc.go` and enforced by `backend/tableownership_test.go`. The few
  sanctioned cross-store writes (merge relinks, GDPR erasure) are ratified in that test.
- **Two spine shapes, and only two.** *Handlers→Store* (CRUD modules: the store owns the write shape
  and the RBAC gate at its entry points) or *Handlers→Service* (engine modules: a service owns
  multi-step logic and drives the SQL). A handful of modules are seam-shaped (a registry, a factory,
  a jurisdiction pack) rather than an HTTP capability — noted below.

## The catalog

The **HTTP surface** column is the *contract* surface (from `crm.yaml`) each module owns — a route may
still answer a generated `501` until its handler lands; it is not an implementation-status list.

| Module | Owns (purpose) | Spine | Owns tables | HTTP surface (`/v1/…`) |
|---|---|---|---|---|
| **identity** | Workspaces, users, opaque server-side sessions, RBAC roles, Agent Seat Passports, the A2 OAuth server, and the resumable onboarding-wizard state. Auth is in-app — no separate identity service. The singleton organization bootstraps from `margince.yaml`; no request creates a workspace. | Handlers→Service (`NewService`) | `workspace, app_user, team, team_membership, session, passport, auth_token, role, role_assignment, record_grant, oauth_client, oauth_authorization_code, onboarding_wizard_state` | `/me`, `/users`, `/teams`, `/auth/*` (login, logout, capabilities, forgot/reset-password), `/passports`, `/record-grants`, `/onboarding/state` |
| **people** | The person, organization, and lead aggregates — create, dedupe, list, optimistic update, archive, the two-record merge, lead promotion — plus the company profile/fact substrate and site reads (the typed `CompanyContext` read model, the deep-read and onboarding dossiers; see [company-context.md](../explanation/company-context.md)). | Handlers→Store (`NewStore`) | `person, person_email, person_phone, person_social, organization, organization_domain, organization_profile_field, person_profile_field, organization_fact, site_read, dedupe_candidate, relationship, partner, lead` | `/people` (+`/merge`,`/strength`), `/organizations` (+`/merge`,`/partner`,`/enrich`,`/deep-read`,`/site-reads`,`/hierarchy-rollup`), `/relationships`, `/leads` (+`/promote`), `/dedupe/candidates`, `/company` (+`/context`,`/site-reads`), `/coldstart` |
| **deals** | The deal aggregate + pipeline/stage scaffolding, stage advancement (won/lost + FX freeze), the per-workspace default-pipeline seed, and the offer engine (rate-card products + versioned deal-bound offers with server-computed totals). | Handlers→Store (`NewStore`) | `deal, deal_stage_history, pipeline, stage, fx_rate, product, offer, offer_line_item, offer_template` | `/deals` (+`/advance`,`/stakeholders`), `/pipelines`, `/stages`, `/products`, `/offer-templates`, `/offers` (+`/line-items`,`/send`,`/accept`,`/reject`,`/regenerate`,`/render`,`/pdf`) |
| **activities** | The activity timeline — idempotent capture-keyed logging, polymorphic links to person/org/deal — plus attachments and scheduling/booking. | Handlers→Store (`NewStore`) | `activity, activity_link, attachment, booking_page` | `/activities` (+`/relink`,`/draft-email`,`/send-email`), `/attachments` (+`/extraction`,`/extraction:accept`,`/request-access`), `/availability`, `/bookings`, `/public/booking`, `/public/preferences` |
| **approvals** | The 🟡 confirm-first engine — agents stage an action they may not perform, humans decide it in the inbox, the agent redeems the decision. The staged row is the authority object. | Handlers→Service (`NewService`) | `approval, workspace_signing_key` | `/approvals` (+`/{id}/approve`,`/{id}/reject`) |
| **agents** | The governed agent surface — the MCP tool registry, the admission gate (scope ∧ tier ∧ seat), the approval flow, and the Surface-B reasoning loop. Reaches records only through the datasource seam. | Engine (MCP registry + `runner/`) | `agent_run, runner_job` (the loop's own run state; records belong to domain modules, staged actions to approvals) | the MCP tool surface (`cmd/mcp`), `/agent-tools` |
| **automation** | The closed automation catalog (ADR-0035) — a bounded 7×7 trigger/action registry a workspace instantiates and parameterizes, never a builder. Owns the per-workspace standing automation rows and the deterministic trigger runtime: an event matcher (`cg:workflows`) and a River-driven clock time-scan converge on one shared firing path, each firing permission-gated twice — an author-time ceiling and a match-time re-check of the automation owner's live RBAC. | Handlers→Store for `/automations` CRUD; the engine + time-scan are worker-driven, not an HTTP spine (precedented by `privacy.RetentionService`) | `automation, workflow_run` | `/automations` (+`/catalog`,`/{id}/preview`,`/{id}/runs`) |
| **ai** | The model runtime behind `ports/model` — provider adapters (Anthropic / OpenAI / Gemini / OpenAI-compatible BYOK, Ollama, local vLLM, the offline fake), the `SelectBrain` factory, the tiered `Router` (routing + budget + tracing), the outbound secret stripper — plus the Voice DNA HTTP slice. See [ai-runtime.md](../explanation/ai-runtime.md). | Runtime factory (`SelectBrain`) + `Router`; voice HTTP via Store | `ai_usage, ai_call, ai_call_payload, ai_call_config, voice_profile, voice_corpus_source` | `/voice-profiles` (+`/sources`), `/ai/usage` |
| **search** | Cross-object retrieval — ranked full-text over the generated `search_tsv` columns, with the pgvector/RRF hybrid and context graph. Every query carries the caller's row-scope predicate (a hit IS a read). | Handlers→Store (`NewStore`) | `embedding` (reads domain tables through their indexes) | `/search` |
| **capture** | The one `connector.Sink` — normalized inbound capture, one transaction per record, idempotent on `(source_system, source_id)` — plus the connector registry (grant-time scope intersection). | Sink + Registry (`NewSink`/`NewRegistry`; connector HTTP wired in compose) | `raw_capture, capture_connection, capture_exclusion_rule, capture_sync_state, capture_backfill, workspace_email_domain, capture_digest` | `/connectors` (+`/{provider}/connect`,`/callback`,`/disconnect`,`/backfill`, `/imap/connect`), `/capture/exclusions`, `/digest` (via compose adapters) |
| **consent** | Per-purpose consent — the purpose catalog, each person's current state, the append-only proof log, and the default-deny outbound suppression gate. Hosts the DSR case queue. | Handlers→Store (`NewStore`) + `NewGate` | `consent_purpose, person_consent, consent_event, consent_doi_token, preference_token, data_subject_request, retention_policy` (the retention *catalog*; the engine that executes it is privacy's) | `/consent-purposes`, `/people/{id}/consent` (+`/double-opt-in`), `/data-subject-requests` |
| **privacy** | The GDPR engines — Art. 17 erasure, Art. 15 SAR assembly, the nightly retention evaluator — the ratified cross-store writer. Serves the field-history + audit-log reads. | Engines (`NewEraser`, SAR, retention) + thin HTTP over the pool | `erasure_suppression` (deliberately writes, but does NOT own, person/lead/activity/deal/embedding/raw_capture rows during a purge — each ratified in `tableownership_test.go`) | `/field-history`, `/audit-log` |
| **collections** | Lists (static sets and dynamic segments) and tags over the four core record types, each membership visibility-probed so a list can't become a side channel. Plus saved views + export sources. | Handlers→Store (`NewStore`) | `list, list_member, tag, taggable, saved_view` | `/lists` (+`/members`), `/tags` (+`/apply`), `/views`, `/exports` |
| **signals** | The company-level, consent-gated warm-room signal substrate, the inspectable signal→organization resolver, and the warm/cold join over our own contact graph. | Handlers→Store (`NewStore`, strength source injected) | `signal, signal_resolution` | `/signals` (+`/{id}/resolve`,`/{id}/warmth`,`/{id}/intro-path`) |
| **customfields** | The governed add-field engine — the single chokepoint allowed to run a runtime `ALTER TABLE`. Validates a field definition against the closed type/object sets, derives its namespaced physical `cf_*` column, and runs the DDL + `custom_field` catalog INSERT + audit atomically. Record stores read these columns through the `fieldcatalog` seam, never by importing this module. | Handlers→Service (`NewService`) | `custom_field` | `/custom-fields` (+`/{id}`,`/{id}/retire`,`/{id}/options`) |
| **quotas** | The quota aggregate (RD-T06) — a per-owner XOR per-team revenue target over an explicit period, with a human-set `target_minor` (never AI-guessed or server-computed). Workspace-shared config posture: governed by the `quota` object grant alone, never row-scoped. Audit-only writes (events.md defines no `quota.*` type). | Handlers→Store (`NewStore`) | `quota` | `/quotas` (+`/{id}`,`/{id}/attainment`) |
| **de** | The German jurisdiction pack — GoBD statutory retention classes, registered in `init()` and pulled into an edge binary by a blank import. Core code never contains a jurisdiction string. | Jurisdiction pack (`ports/jurisdiction`, no Handlers/Store) | none | none (consumed by privacy's retention evaluator through the seam) |
| **overlay** | The HubSpot-as-system-of-record adapter (branch 1: read + continuous sync) — binds the frozen `datasource` seam via an inner `incumbent.Incumbent` seam, mirrors a connected portal into a governed, T2-tagged, per-user-visibility-filtered read model, and meters/degrades force-fresh reads under the shared HubSpot rate budget. Connection lifecycle is admin/ops-only; every role reads status. Write verbs return `unsupported_by_sor` (write-back is a later branch). | Handlers→Service (`NewService`) + a seam-shaped substrate (mirror/backfill/reconcile/teardown, no HTTP of their own) | `incumbent_connection, overlay_mirror, overlay_association, mirror_user_map, mirror_visibility, overlay_write_ledger, overlay_tombstone, overlay_backfill_cursor, overlay_reconcile_watermark` | `/overlay/connection`, `/overlay/sync-status`, `/overlay/reconcile`, `/overlay/budget`, `/overlay/flip*` (stub) |
| **webhooks** | The governed outbound egress surface (E10/S-E10.6) — tenant-registered HTTPS subscriptions to a subset of the published event catalog, and a delivery engine that fans matching domain events off `cg:webhooks` as HMAC-SHA256-signed POSTs, retried with exponential backoff, dead-lettered, and human-replayable. The per-subscription signing secret is AES-256-GCM-sealed at rest (shown once at create/rotate); the fan-out is owner-scoped (BYO-EVT-4 — a webhook never delivers an event its owner may not see); the dialer is SSRF-guarded. Managing subscriptions is admin/ops config; agent create/update is 🟡. Needs a deployment signing key — unconfigured, reads still list but secret paths answer 503. See [outbound-webhooks.md](../explanation/outbound-webhooks.md). | Handlers→Store (`NewStore`) for CRUD; a bus-consumer + retry-sweep `Deliverer` (worker-driven, no HTTP spine) | `webhook_subscription, webhook_delivery` | `/webhook-subscriptions` (+`/{id}`,`/{id}/rotate-secret`,`/{id}/deliveries`,`/{id}/deliveries/{deliveryId}/replay`) |

Two tables are owned by the composition layer, not a module: `idempotency_key`
(HTTP replay protection — transport plumbing) and `brief_run` (the morning-brief
ranker's own snapshot, `compose/briefs`). Compose-level *features* — company
context, onboarding wizard transport, reply drafting, deep-read orchestration,
the AI certification lane — are likewise not modules: they wire module stores
together ([explanation/composition-layer.md](../explanation/composition-layer.md)).

## Notable subpackages

- `identity/internal/policy` — the role permission-policy documents (kept hidden from the rest of the module).
- `identity/internal/password` — Argon2id hashing/verification.
- `agents/runner` — the Surface-B reason-act-observe loop (its own `Store`, catalog, window).
- `capture/{imap,gmail,graph}` — the per-provider mail-capture connectors, with
  `capture/oauthflow` (the shared OAuth connect/callback flow), `capture/exclusion`
  (the exclusion-rule engine), and `capture/mailmap` (address/domain mapping).

## Where cross-module edges are wired

A module never reaches a sibling; the composition root injects the edge (how that works:
[explanation/composition-layer.md](../explanation/composition-layer.md)). The current edges (wired in
`internal/compose/server.go`):

- **identity's workspace seed** ← deals (default pipeline) + consent (default purposes/retention) +
  automation (starter automations) + activities (booking page) — one bootstrap transaction.
- **agents' staging/redemption** ← approvals (adapter).
- **automation's action seams** ← collections (add-to-list), activities + consent (draft-email
  compose + the outbound suppression gate), approvals (🟡 staging), activities (the no-activity/
  check-in candidate scan), identity (the match-time owner-permission re-check via `authz.Resolver`).
- **consent's DSR erasure** ← privacy's `Eraser`.
- **signals' relationship strength** ← people's store (adapter).
- **activities' outbound gate** ← consent (the suppression gate) + people/consent public seams.
- **webhooks' owner-scope fan-out** ← identity (the enqueue-time owner-visibility re-check via `authz.Resolver`).
- **imap connect** ← capture's connector registry (adapter).
- **filtered export** ← collections' saved-view/list source (adapter).

To place a new capability: add `internal/modules/<name>/` (flat), give it a `doc.go` with a "Tables
owned" list, follow one spine shape, and wire any cross-module need as a compose adapter — never a
sibling import.
