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
approval engine, two-record merge, and the embedded SPA. The full,
current inventory of built surface is
[README.md → *What works today*](README.md#what-works-today); what is
deliberately still stubbed (answering explicit 501) is
[*Deliberately not here yet*](README.md#deliberately-not-here-yet).

The merge gate (`make check`), the real-Postgres integration lane
(`make test-integration`), and the live-boot job are all green.

## Recently landed

**Skeleton-baseline batch** — this repo is being groomed into the
baseline for the official open-source Margince repository, absorbing the
tooling and gate suite the foundation skeleton carries. The classified
delta and sequencing live in
[docs/worklists/skeleton-baseline-2026-07-09.md](docs/worklists/skeleton-baseline-2026-07-09.md).
Merged so far:

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
  the OSS baseline and reconcile the foundation spec tree with this
  repo's actual architecture. Until it lands, the docs refer to the spec
  as "a separate spec repo" without a literal path; they gain a concrete
  public spec URL once the canonical public spec home is decided.
- **EP05 §B capture-connection reshape** — now unblocked by the keyvault
  seam: multiple per-user connections, the connection-management contract
  surface + UI, and connector credential *rotation* (the ref/AAD scheme
  already carries a key version so rotation is not foreclosed). Its own PR
  arc. The `oauth` signing keypairs (`workspace_signing_key`) fold onto the
  same vault next, as a distinct migration.
- **ADR track** (parallel, each an open call recorded in the PR that resolves it): retiring or
  keeping the second (embedded) SPA, the design-system of record, and the
  optional advisory LLM craft-review CI job. (River shipped in #35, the
  blobstore seam in the prior batch, the keyvault seam in this one.)
- **Frontend DECISION items** from worklist §1d: router migration and a
  Storybook/component-test lane — adopt when the design system
  stabilizes, not before.
- **Publication mechanics** (founder decision): whether to publish full
  git history or squash-import into the public repository.

Next product arcs beyond the baseline groom live in the spec's build
backlog; route findings as you work — implementation decisions recorded in the
commit and PR that makes the change; spec/ticket defects reconciled upstream
against the spec.
