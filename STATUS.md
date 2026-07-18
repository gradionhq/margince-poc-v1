# Status — where this stands and where to pick up

> The pickup record for this implementation. Whoever works here next
> (human or agent): read this first, then [AGENTS.md](AGENTS.md) for the
> binding engineering rules. Update this file at the end of every working
> session. The durable, detailed record is git history — this file stays
> a concise snapshot of current state and open work, not a session log.

## Where this is

Margince's **WP0 foundation + WP1 core spine** are built and green:
schema, contract pipeline, auth, core CRUD, the event bus, RBAC, the
governed MCP/agent surface, the transport-agnostic autonomy gate, the
approval engine, two-record merge, and the Vite/React web UI. The full,
current inventory of built surface is
[README.md → *What works today*](README.md#what-works-today); what is
deliberately still stubbed (answering explicit 501) is
[*Deliberately not here yet*](README.md#deliberately-not-here-yet).

The merge gate (`make check`), the real-Postgres integration lane
(`make test-integration`), and the live-boot job are all green.

## Recently landed

**Deep read v3 — reference evidence + page-parallel lanes (founder
target ≤15 s, 2026-07-18)** — v2's one corpus call hit the output-token
wall (~9k quoted-evidence tokens ≈ 150 s). v3 makes the model *read*
everything and *write* almost nothing: pages are segmented into
numbered passages, the model cites `"e":"s12"` (schema-enum'd — an
uncitable id can't be generated) and Go resolves + verifies the
reference, storing the page's own text as evidence. Extraction is one
compact call per fact-bearing page (fast tier, `site_fact_extract`) +
ONE premium profile call over the top excerpts (`site_extract`), all
OVERLAPPED with the frontier-wave crawl — page calls launch as pages
commit, the profile fires once the identity-dense prefix is in. Live on
gradion.com: **~25 s end-to-end** (360→150→42→25 across the arc; the
remaining floor is gradion's own server throttling the crawl burst —
snappier origins land ~12–15 s), with MORE extracted than ever: 8/8
profile fields, ~200 facts (69 services, 69 technologies, 25 locations),
**11 people** (first roster), 5-entity census → correct abstention.
E2E floor gains duration ceilings + a paraphrase-warning watchdog.

**Deep read v2 — ONE corpus call (founder decision 2026-07-18)** — the
per-page extraction (1–2 model calls per page, ~6 min for gradion.com,
plus a synthesis pass and three cross-page merges) is replaced by ONE
streamed model call over the whole labeled site corpus (~78k tokens for
gradion.com; chunked fallback ≤4 for outsized sites). The no-guess gate
survives intact — every fact re-verified against its NAMED page, and a
new `legal_entities[]` census makes the multi-entity abstention explicit
(gradion.com's five-entity imprint → no legal identity proposed +
warning). Extraction taxonomy v2 adds `company/location` and
`signal/technology` (migration 0088). The crawl bursts (12-wide waves,
committed in order — byte-identical to serial by test; ~10 s, <5 s needs
the pipelined-fetch follow-up), and the dossier now reports live
`phase`/`pages_read` (migration 0089) so the SPA poll shows movement.
Anthropic Complete rides SSE above 8k max_tokens (the API drops silent
non-streaming connections). Extraction routes premium-first
(`site_extract`); for Anthropic the premium tier must be SONNET-class+
(Haiku paraphrases evidence away) — judged by the pinned E2E floor
`make -C backend e2e-siteread` with taxonomy floors (locations ≥ 4,
technologies ≥ 5, offerings ≥ 10, ≤4 calls). Live: gradion.com in
~2.5 min end-to-end, 60+ facts, 3 people, correct abstention.

**Deep-read quality loop — debug CLI + ingestion quality** — the answer
to "12 pages, missing facts, wrong company": crawl caps are now
operator-tunable with raised defaults (40 pages / 32 MiB / 240 s;
`--deepread-*` worker flags), and `worker siteread <url>` runs the whole
crawl→extract→merge pipeline **without the stack** (no DB/Redis/staging)
printing every intermediate — pages, skips, every extracted field with
evidence, every finding the gate DROPPED with its reason, merge
decisions, per-call model telemetry, diffable `--json`. Quality fixes:
the evidence gate now falls back to presentation-normalized matching
(quotes/dashes/whitespace/case — words never forgiven) and reports every
drop instead of silently discarding; the crawl queue is kind-ranked
(impressum/about/team before blog archives; tracking params stripped);
extraction has its own routing dial (`site_extract` task) so its tier is
an `ai-routing.yaml` edit; a site-level synthesis pass reconciles
contradictions across pages (still evidence-gated per named page,
degrades to the merge on failure); and the legal-page override is
hardened (path-depth ≤ 2 authority rule; disagreeing legal pages cancel
the override entirely). Model comparison per site:
`worker siteread <url> --model anthropic:<model>`. Spec reconciliation
pending upstream: the R2 caps (12/8 MiB/90 s) were raised by founder
decision 2026-07-18.

**Website deep read — crawl a company's whole site (PR #103)** — the
generic, powerful ingestion: an async River-queued crawl of a company's
site (bounded — ≤12 pages / 8 MiB / 90s, robots-honored, SSRF-guarded,
discovery deterministic and never model-chosen) that extracts far more
than the cold-start fields — company facts, offerings, market signals,
and team members — through the same evidence-or-omit gate, and stages
every finding as a confirm-first 🟡 proposal. New home
`organization_fact` (closed per-category vocabularies, enforced in prompt
+ schema + DB CHECK); the 11 cold-start fields keep
`organization_profile_field`. Team members become thin, published-only
`site_lead` proposals landing through the capture Sink as segregated
leads (NEVER-8 kept). `POST /organizations/{id}/deep-read` (202 + poll),
`GET .../site-reads/{readId}` reports pages read, pages skipped *with
reasons*, and any early-stop cause. Reused for onboarding *and*
enrichment of any org. Live-verified against gradion.com (12 pages, 40
facts, accepted end-to-end). The live run also caught a defect no fake
could: River's silent 1-min job timeout killed real crawls and the
exhausted context wedged the dossier `running` — fixed with an 8-min
worker timeout + `terminalCtx` (WithoutCancel + fresh deadline) so the
terminal write survives the work's death.

**Website read-back reads the SITE, and the onboarding design fix (PR #101)**
— the read-back now fetches the given page *plus* the well-known
Impressum/legal-notice paths and merges per-page (legal facts prefer the
page that legally states them), so German sites finally ground
legal_name/VAT/registered_address; `display_name` joined the
ColdStartField vocabulary. The fetcher moved to `platform/webread` and
keeps ADR-0006's promise: robots.txt honored (RFC 9309 semantics, named
UA), SSRF-guarded via the socket Control hook. The onboarding company
form was rebuilt on the design-system atoms (it had bespoke CSS that read
as a foreign screen).

**Onboarding first-run — a bare installation lands in a company form (PR #98)**
— a cold-start admin used to land in the main menu on top of a nameless
org; now the app shell gates on `GET /company` (404 = undescribed) and
routes them into a mandatory company step. `PUT /company` is the human's
confirm-first write (the unsaved form IS the 🟡 staged state, marked
`human-only`); `POST /coldstart/preview` pre-fills it without staging.
The anchor org is marked `organization.is_anchor` (0083). Required
identity block (name, legal entity, VAT, address, industry); the step
cannot be skipped.

**Cloud-provider review remediation (PR #102)** — the top-10 correctness
findings from the post-merge review of the cloud model providers (#96):
streams surface failure/truncation terminals instead of clean EOF (openai
`response.failed`/`incomplete`/`error`, gemini mid-stream error objects +
abnormal `finishReason`, applied to `Complete` too), one shared SSE
scanner with a 4MiB line cap, cache keys cover model override + response
schema, `OutputTokens` is reasoning-inclusive on every adapter (gemini
normalized), Responses API `store:false` pinned, `dimensions` omitted on
the generic OpenAI wire, canonical `models/…` ids accepted, vLLM
top-level errors decoded, and `make dev` enables real routing only when
every bound cloud provider's key is present.

**Single-organization installation (ADR-0061/A107, PR #90)** — the
ratified single-org concept, end to end. One installation serves one
organization: bootstrap moved off the public wire into a strict
`margince.yaml` deployment file (`platform/deployconfig`) consumed at
API boot under a pg advisory lock — organization + first admin + system
roles + configurable seeds (pipeline stages, consent purposes, starter
automations, booking page) in one transaction; 0 workspaces → create,
1 → bind, >1 → refuse for an operator-led migration (boot-enforced,
deliberately NOT a schema constraint so the cross-tenant RLS suites keep
proving isolation). `POST /workspaces`, the `{workspace}` subdomain
template, and every tenant selector (`X-Workspace-Slug`, MCP
`--workspace`) are gone; pre-bootstrap requests answer 503
(availability, never auth). The A74 account-recovery pair is live:
`auth_token` (0081), a STARTTLS-required SMTP mailer behind the
`email:` config section, enumeration-resistant forgot/reset (the whole
account-dependent path runs off-request), and `migrate reset-password`
as the operator recovery. Anonymous `GET /auth/capabilities` drives the
login UI, which is now a login-first single column (no signup mode, no
hero, no slug field) with capability-gated forgot/reset screens.
Spec-side: margince-foundation ADR-0061 + DECISIONS A107 + ADR-0043
Amendment 2 (merged there first, contract-first).

**Craft gate de-vendored** — `cli/craft` is now a first-class, locally-owned
part of this repo rather than a hash-pinned vendored copy: the
`craft-manifest.sha256` hash pin, the `craft-drift`/`craft-sync` targets,
and all "vendored / hash-pinned / fix upstream" language are gone (its own
Go tests gate its behaviour). `infra/branch-protection.json` and its
wiring fitness test were retired with it; live GitHub branch protection
remains the enforcement.

**OSS-baseline batch** — this repo is being groomed into the
baseline for the official open-source Margince repository, absorbing the
tooling and gate suite the baseline needs. Merged so far:

- **PR A** — craft gate v3, SHA-pinned GitHub Actions + an image-pin
  gate, `concurrency:` cancel groups, `.env.template`, `make tools`
  bootstrap, `config/ai-routing.example.yaml`.
- **PR B** — `infra/docker-compose.dev.yml` dev stack, the API-driven
  demo seed (`make seed-dev` / `seed-reset` / `verify-boot`), the README
  boot/log-in/verify quickstart, and the `live-boot` CI job.
- **PR C** — gate parity: oasdiff contract breaking-change gate, TS type
  drift gate, test-lane hygiene, zero-skip integration enforcement, the
  new-code-strict golangci arm, and the file-length ratchet.
- **PR D** — frontend RBAC primitives (`useMe`, `RoleBadge`,
  `FieldGuard`, role-aware automations editor) and the design-token
  purity gates.
- **PR E** — OSS-publication sanitization: this STATUS scrub,
  CONTRIBUTING rewritten for external contributors, the README
  internal-narrative scrub.
- **Identity fix** — the public auth paths (`/v1/auth/login`,
  `/v1/auth/logout`, `/oauth/token`, `/oauth/register`) now answer their
  protocol's client error instead of a 500 when the workspace slug
  resolves to nothing, without disclosing whether the workspace exists.
- **Blobstore seam** — `platform/blobstore` (S3/MinIO + in-memory fake),
  the object-bytes substrate behind the `attachment.storage_key` the
  schema already committed to. Ships with its first production consumer:
  the minimal `/attachments` surface (upload/download/list/soft-delete,
  owned by `activities`, authority inherited from the parent entity) and
  the Art. 17 erase-path object purge, so erasure reaches the bytes not
  only the rows. MinIO is in the dev compose stack and both CI integration
  jobs; a `/readyz` probe covers it.
- **Keyvault seam** — `platform/keyvault` (AES-256-GCM local provider +
  in-memory fake), secret-material storage behind an opaque,
  workspace-scoped `credential_ref`. Ships with its first real secret
  migrated: `connector_connection.auth` (bytea) moves off the tenant row
  onto the vault, leaving only a ref on the row (the `auth` column is
  dropped in a later additive migration after backfill). Isolation is
  cryptographic — the ref carries its workspace and the GCM AAD binds it,
  so a stolen ref is inert across the tenant edge; the `vault_secret`
  ciphertext table is operational infra (no `workspace_id`, no RLS), like
  River's tables. `WithKeyvault` feeds a `/readyz` probe; the worker
  backfills legacy rows at boot (idempotent). Env-only root key
  (`MARGINCE_KEYVAULT_ROOT_KEY`, base64 32-byte). The connector port is
  unchanged — capture resolves the ref and still hands the connector its
  `Auth`.
- **Field-history read** — `GET /field-history`: a per-field change
  timeline projected read-time from the audit spine's before/after
  diffs, homed in the privacy module beside the audit-log read. Gated
  exactly like every other record read (human-only + object-read +
  row-scope, activities dispatching through the link-walk); no new
  table or migration — the projection runs entirely off `audit_log`.
  First arc of the poc-1 feature-delta port.
- **Org hierarchy roll-up read** — `GET /organizations/{id}/hierarchy-
  rollup`: a tree or self account roll-up (weighted pipeline,
  current-quarter closed-won, 30-day activity) with RBAC-honest
  restricted-node disclosure and base-currency FX conversion (422 on a
  missing rate, never a silent rate=1). Compose-homed — the read spans
  organization, deal, stage, activity, and fx_rate — with no new table.
  Arc 1b of the poc-1 feature-delta port.
- **Record history read** — `GET /records/{entity_type}/{id}/history`:
  chronological plain-language history lines with actor + agent-authority
  attribution, viewer-masked before/after (by omission), keyset
  pagination, and the erasure boundary (pre-scrub rows withheld, the
  tombstone's own line served); third audit-spine read in the privacy
  module; the erase tombstones now carry their tallies on the evidence
  channel. Arc 1c — closes Wave 1 of the poc-1 delta port.
- **Custom-fields catalog + governed schema-change engine** —
  workspace-defined scalar fields on core objects (create 🟡/rename
  🟢/retire 🟡/picklist options 🟡), a new `modules/customfields` service
  running the one sanctioned runtime ALTER through a dedicated
  boot-optional owner pool (`--schema-dsn`/`MARGINCE_SCHEMA_DSN`, unwired
  ⇒ 501 — see
  [docs/reference/configuration.md](docs/reference/configuration.md))
  with the DDL-first-then-SET-ROLE single-tx dance, cross-workspace
  column-collision 409s, and an AST fitness gate pinning the privilege
  downgrade. Values-on-records parity — reading and
  writing the new fields through the record surface — is the follow-on
  arc, arc 2a-ii.
- **Custom-field VALUES ride person/organization/deal payloads**
  (create/update/read/list, top-level `cf_` keys via the contract's
  x-extension mechanism), the fieldcatalog seam
  (`shared/ports/fieldcatalog` provided by customfields, injected by
  compose), and the first real list-sort implementation — DM-VOCAB-
  aligned single-field sort + typed `cf_` equality filters on an
  extended keyset cursor (sort-fingerprinted, crafted-token-hardened);
  active columns join the vocabulary, retired leave it. Arc 2a-ii
  completes CF-T05's core parity (collections/saved-views cf-awareness
  flagged as follow-up; a merged-away record's cf values stay on the
  archived source row — merge survivorship fill is core-columns-only in
  V1).
- **Formula fields as database-GENERATED artifacts** (RD-T08) —
  `deal.amount_minor_base` GENERATED column + the
  `organization_open_pipeline_rollup` security_invoker view, surfaced as
  gated `computed_fields[]` display rows on the org 360 read (STATE-4:
  key absent without `computed_field:read`, a new read-only-everywhere
  RBAC object); the hierarchy-rollup closed-won and brief SQL adopt the
  column; schema-proof + no-runtime-authoring fitness tests stand guard.
  Closes Wave 2 of the poc-1 delta port.
- **Quotas & attainment (RD-T06)** — the `quota` aggregate (owner XOR
  team, explicit period, human-set target; workspace-shared config
  gated by the new `quota` RBAC object) with full CRUD and the
  server-computed attainment read: Σ closed-won `amount_minor_base` ÷
  base-converted target, decomposed per contributing deal
  (golden-number reconciliation), honest 422s for zero targets and
  missing FX, pace/band derivations on an injected clock. Wave 3 opener
  of the poc-1 delta port.
- **Attachment AI extraction (RD-T05/RD-T10 backend)** — `scan_status`
  gating (`scanning`/`blocked` refuse the download stream with typed
  409s; the module-local Scanner seam has no product, so uploads default
  `scanning`), the evidence-or-omit staged extraction read behind the
  `shared/ports/extraction` seam (NoOp default — honest empty), and the
  compose-orchestrated `extraction:accept` writing an allowlisted set of
  grounded fields onto the deal with per-field audited provenance
  (human-only V1). Closes Wave 3 of the poc-1 delta port.
- **DE/EN offer templates + branded PDF render (offers-depth arc 4a)** —
  the `offer_template` catalog (workspace config, one default per locale,
  name-unique, the two named 409s) with CRUD gated by the new
  `offer_template` RBAC object, and `POST /offers/{id}/render` producing
  a go-pdf/fpdf branded DE/EN PDF (labels driven by the offer's template
  locale) stored to the blobstore as `pdf_asset_ref` — render totals
  equal the server-computed totals exactly (no drift). poc-v1's offer
  lifecycle (send/accept/reject/FX-freeze/totals) is untouched. First
  half of Wave 4; AI-drafted regeneration (delta 1) is arc 4b.
- **AI-drafted offer regeneration (offers-depth arc 4b)** — a
  compose-orchestrated evidence-gated AI draft: on regenerate, the
  mechanical revision-mint runs first, then (when the OfferDraft model
  lane is wired via `--ai-routing`/`--ai-fake`) the orchestrator calls
  the model, keeps ONLY lines whose price + snippet are verbatim-grounded
  in the deal's captured context (drops the rest, never fabricates;
  blank price when ungrounded), stages them via the deals
  `AddStagedOfferLines` seam (excluded from server-computed totals until
  a human accepts), and returns the Art. 50 disclosure + a diff — all
  transient. Secret-stripped model calls; totals never AI-computed; the
  send/accept/reject/FX lifecycle untouched; unwired = mechanical-only.
  This CLOSES Wave 4 and the entire poc-1 delta port.

## Pick up here

Open work, roughly in priority order:

- **Deep-read durability-hardening pass** (from the #103 review, deferred
  as cross-cutting rather than rushed per-effect) — the redeem-then-execute
  accept effects (coldstart/scrape/deepread/site_lead) share the ADR-0036
  pattern where a consumed-but-unapplied approval can't be retried; the
  correct fix is transactional redeem+apply at the approvals-framework
  level, repo-wide. Plus: transactional River enqueue (Start→enqueue and
  stage→finish are separate module txns today; `closeUnqueued` is the
  current compensation), and a stale-`running` dossier reclaim/sweeper (a
  crash between Begin and Finish wedges the org's one in-flight slot;
  `terminalCtx` shrinks but doesn't close the window). Recorded in PR #103's
  tracking comment.
- **Website ingestion — upstream ratifications to reconcile** (spec repo,
  contract-first): founder ratifications R1–R5 (well-known-path probes
  within ADR-0006, crawl caps/robots posture, the `organization_fact`
  category home, thin-lead sourcing under NEVER-8) recorded in the #101/#103
  PR bodies; the two-page quick read measures ~13.3s vs ONBOARD-PARAM-1's
  8s p95 (re-pin the budget for the multi-page read, or parallelize once the
  fake client scripts per-page); and `crm.yaml`'s `deepReadCompany`
  description still mentions a `deepread`-vs-`enrich` proposal kind and a
  `budget` stop reason the v1 does not emit.
- **No scanner product + no boot wiring** — new uploads stay
  `scanning`/undownloadable until an admin or test drives
  `activities.Store.MarkScanResult`; no real scanning product is
  integrated anywhere in this codebase (operational gap, poc-1 parity).
  A production deployment needs a real Scanner behind the seam, or an
  admin verdict path, before new uploads are downloadable end-to-end.
- **The RD-AC-2 "every download audited" clause is NOT ported** — poc-v1
  audits only attachment create/archive; a deliberate
  delta from the spec, not an oversight.
- **`extraction:accept` carries no idempotency key on its notes** — the
  deal update and its per-field notes now commit atomically (one shared
  `database.WithWorkspaceTx`, driven via `deals.Store.UpdateDealTx` +
  `activities.Store.LogActivityTx`: a note failure rolls the deal update
  back too), but a client retry on a dropped response still re-applies the
  deal update (last-write-wins, harmless) and duplicates the provenance
  notes — there is no natural key on a note the way capture's
  `(source_system, source_id)` gives `LogActivity` its own idempotency.
- **The 🟡 agent-staged accept path (approvals effect) is deferred** —
  V1 ships human-only; an agent cannot currently propose an
  extraction-accept for confirm-first approval.
- **`RequestAttachmentAccess` is a courtesy-audit-only op** — poc-v1 has
  no restricted-but-disclosed state to actually gate on; the note is the
  entire effect. The final review ruled it a keep (honestly labelled,
  contract parity), not a defect.
- **The extraction read and the accept-write share the raw download's
  scan gate** — `GetAttachmentExtraction`/`extraction:accept` now refuse
  a `scanning`/`blocked` attachment with the same typed 409s before the
  extractor ever sees the bytes (defense-in-depth, RD-T05). Inert today
  under the NoOp/Fixture seams; essential the moment a real extractor
  (riding `modules/ai`) reads unvetted content.
- **§0 baseline ratification** (founder decision): confirm this repo as
  the OSS baseline and reconcile the spec tree with this
  repo's actual architecture. Until it lands, the docs refer to the spec
  as "a separate spec repo" without a literal path; they gain a concrete
  public spec URL once the canonical public spec home is decided.
- **EP05 §B capture-connection reshape** — now unblocked by the keyvault
  seam: multiple per-user connections, the connection-management contract
  surface + UI, and connector credential *rotation* (the ref/AAD scheme
  already carries a key version so rotation is not foreclosed). Its own PR
  arc. The `oauth` signing keypairs (`workspace_signing_key`) fold onto the
  same vault next, as a distinct migration.
- **ADR track** (parallel, each an open call recorded in the PR that resolves it): the
  design-system of record, and the optional advisory LLM craft-review CI
  job. (River shipped in #35, the blobstore seam in the prior batch, the
  keyvault seam in this one. The embedded SPA is retired — the API binary
  serves `/v1` only; the web UI is served separately.)
- **Frontend DECISION items**: router migration and a
  Storybook/component-test lane — adopt when the design system
  stabilizes, not before.
- **Publication mechanics** (founder decision): whether to publish full
  git history or squash-import into the public repository.

Next product arcs beyond the baseline groom live in the spec's build
backlog; route findings as you work — implementation decisions recorded in the
commit and PR that makes the change; spec/ticket defects reconciled upstream
against the spec.

## Cloud providers — upstream discrepancies to reconcile

Filed upstream as `gradionhq/margince-foundation` **#1073** (contract
reconciliation: interfaces.md §4 additive fields, ADR-0020 env-key posture,
`provider: local` naming gap, Mistral alias, richer `model.Message`) and
**#1074** (model-capability catalog incl. embedding dimensionality, = §7 #6).
Per-provider AIUC conformance (§7 #9) and the eval-binding matrix (§7 #4) are
already tracked in foundation #974 / #975 / #976.

Raised by the cloud BYOK model-providers change (generic `openai_compatible`
plus native `openai`/`gemini` adapters). Paths use the **live** foundation
layout (verified against `gradionhq/margince-foundation@main`, 2026-07-17 — the
local sibling checkout is 299 commits behind and still on the old
`specs/spec/…` tree). These are for the foundation session; never edited from
this build repo. The governing rule is contract-first / **spec wins** (the
`architecture.md` invariant), cited by name to avoid the P-number collision in
§7 #10 (product `principles.md` P3 = "agent-readable by construction", a
different principle).

- **#1 / #1a — reconciled in this change (the build side of the contract).**
  `specs/contract/interfaces.md §4` predates reasoning/attachments/rich-usage.
  This change adds the additive `Request.ProviderOptions`/`Attachments`,
  `Response.CachedTokens`/`ReasoningTokens`/`ProviderMetadata`, and the
  `Attachment` type + `ErrAttachmentUnsupported` capability error to
  `ports/model` — a model *capability* error parallel to
  `ErrEmbeddingsUnsupported`, **not** an `apperrors` domain sentinel, so the
  fixed `apperrors` registry and `interfaces.md §0` are untouched. The
  interfaces.md §4 struct listing should gain the same additive fields upstream.
- **#2 — fixed here.** `specs/adr/ADR-0020` §2 + `interfaces.md §4` name OpenAI
  and Gemini as BYOK providers; the build had only `fake`/`anthropic`/`ollama`/
  `vllm`. This change ships all three (`openai_compatible`, `openai`, `gemini`).
- **#3 — raise.** `specs/contract/ai-operational-spec.md §1.4` example binds
  `embeddings: {provider: local, …}` / `stt: {provider: local}` — a bare `local`
  provider name no adapter implements (`SelectBrain` has `ollama`/`vllm`, not
  `local`). A naming gap independent of this change; no `local` alias invented here.
- **#4 — raise.** `ai-operational-spec.md §1.1` names GPT/Gemini classes for
  cheap-cloud/premium, and the WP3 exit gate requires evals on "the local-default
  **and** the cloud-default bindings"; cloud-default is Anthropic, so OpenAI/Gemini
  are named-but-untested. This change ships the adapters + unit coverage; which
  cloud provider WP3 gates on is a spec/WP3 call.
- **#5 — raise.** Mistral is spec-named only as an open-weight **local** model
  (ADR-0012/A23), yet La Plateforme is an OpenAI-compat **cloud** endpoint —
  reachable now via `openai_compatible` + `base_url`. Whether to add a named
  `mistral` cloud alias is a product call.
- **#6 — raise.** No model-capability catalog exists (context window,
  supports-vision/-caching/-reasoning). Out of scope here (YAGNI — the router
  keys on tier); noted as a future item, not half-built.
- **#7 — raise.** `model.Message` is `{Role, Content}` — no per-part slot for
  Gemini-3.x thought signatures or OpenAI reasoning items, so full *native*
  multi-turn thought continuity can't be expressed on the seam. This change
  rides the `ProviderMetadata`→`ProviderOptions` pass-through instead (the Gemini
  thought-signature round-trip); a richer typed-parts `model.Message` is a future
  seam change. Single-shot tasks are unaffected.
- **#8 — documented (no code change).** `openai_compatible` `/embeddings` 404s on
  OpenRouter/Groq/DeepSeek (chat-only); Mistral `-latest` aliases drift/deprecate.
  Captured in `config/ai-routing.example.yaml` + `docs/reference/configuration.md`
  (bind embeddings to a vendor that serves the lane or a local model; pin explicit
  model versions).
- **#9 — raise + follow-up.** `specs/adr/ADR-0050`/A65 (per-provider AI-quality
  conformance, catalog at `specs/contract/ai-acceptance-catalog.md`) certifies AI
  quality *per provider* (Certified / Supported-degraded / Not-supported). Adding
  `openai`/`gemini`/blessed `openai_compatible` targets pulls them into that AIUC
  matrix — a test/catalog obligation to mark them "supported", tracked as a
  separate change, not shipped here. ADR-0050 explicitly leaves the ADR-0013/0020
  invariants and the `Client` seam untouched, so this is not a seam blocker.
- **#10 — no code.** Cite "contract-first / spec wins" (the `architecture.md`
  invariant) by name, not the bare "P3", in commits/comments — `product/principles.md`
  P3 is a different principle.
- **#11 — BYOK key sourced from the environment, not the routing file (reconcile
  upstream).** ADR-0020 / `interfaces.md §4` model the customer key as an
  `api_key` in `ai-routing.yaml`. This build instead reads each cloud provider's
  key from its conventional environment variable (`GEMINI_API_KEY`,
  `OPENAI_API_KEY`, `ANTHROPIC_API_KEY`, `OPENAI_COMPATIBLE_API_KEY`) at boot and
  fails closed (naming the var) if missing; the config carries no `api_key` field
  (a stray one is a parse error). This is a deliberate security-posture decision —
  secrets in the environment, config names only providers (12-factor) — to
  reconcile with ADR-0020's wording. The `Client` seam and the no-inference
  invariant are unchanged.

Implementation follow-ups deferred from this change (honest floors shipped now):

- **Image mapping on the generic `openai_compatible` wire.** The shared chat
  wire is text-only, so `openai_compatible` currently *rejects* every attachment
  (image and document) with `ErrAttachmentUnsupported` rather than accept-and-drop.
  Native `openai`/`gemini` carry images+PDFs today; mapping images to `image_url`
  content parts on the generic wire is the follow-up. `base_url` for the OpenAI-wire
  providers is the vendor host root with **no** `/v1` segment (the adapter adds it).
- **Gemini batch embeddings.** `gemini` Embed makes one `:embedContent` call per
  input (spec §3.5's named endpoint); a large retrieval batch is N sequential
  round-trips. Folding onto `:batchEmbedContents` is the follow-up.
- **Embedding dimensionality is provider/model-specific — own PR.** The store
  column is a fixed `vector(1024)` and `search.embeddingDims` pins it; cloud
  embedders default wider (Gemini 3072, OpenAI 1536), so this change adds
  `EmbedRequest.Dimensions` and the adapters truncate to 1024
  (`outputDimensionality` / `dimensions`). But native widths differ per
  provider/model, and mixed models cannot rank against each other. A proper
  design (store the dimension — and ideally the model — alongside each embedding
  row so the lane can change without a full re-embed, or make the column width
  configurable) is a separate PR. Until then, switching the embed binding means
  wiping the store (as the module comment already notes). Filed upstream as
  foundation #1074. Truncation applies to native `openai`/`gemini` only:
  the generic `openai_compatible`/`vllm` wire omits the `dimensions` knob
  entirely (vLLM rejects it on non-matryoshka models), so a model bound
  there must natively emit the store's width.
- **Native tool-use mapping for `openai`/`gemini`.** The tasks run in JSON mode
  today, so no caller sets `req.Tools`; the native adapters currently **reject**
  a non-empty `Tools` (loud, not a silent drop) rather than map it. Mapping to
  the Responses `tools` / Gemini `functionDeclarations` shapes is the follow-up
  when a tool-using task routes to these providers.
