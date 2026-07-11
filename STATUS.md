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
  new-code-strict golangci arm, and the file-length ratchet
  (decisions/0020).
- **PR D** — frontend RBAC primitives (`useMe`, `RoleBadge`,
  `FieldGuard`, role-aware automations editor) and the design-token
  purity gates (decisions/0014 lineage).
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
  jobs; a `/readyz` probe covers it (decisions/0022).
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
  `Auth` (decisions/0023).
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
  downgrade (decisions/0024). Values-on-records parity — reading and
  writing the new fields through the record surface — is the follow-on
  arc, arc 2a-ii.

## Pick up here

Open work, roughly in priority order:

- **§0 baseline ratification** (founder decision): confirm this repo as
  the OSS baseline and reconcile the foundation spec tree with this
  repo's actual architecture. Until it lands, the spec-path references in
  CLAUDE.md / AGENTS.md / README (`../margince/specs/`) are left as-is;
  they repoint together once the canonical public spec home is decided.
- **EP05 §B capture-connection reshape** — now unblocked by the keyvault
  seam: multiple per-user connections, the connection-management contract
  surface + UI, and connector credential *rotation* (the ref/AAD scheme
  already carries a key version so rotation is not foreclosed). Its own PR
  arc. The `oauth` signing keypairs (`workspace_signing_key`) fold onto the
  same vault next, as a distinct migration (decisions/0023).
- **ADR track** (parallel, each needs a decision record): retiring or
  keeping the second (embedded) SPA, the design-system of record, and the
  optional advisory LLM craft-review CI job. (River shipped in #35, the
  blobstore seam in the prior batch, the keyvault seam in this one.)
- **Frontend DECISION items** from worklist §1d: router migration and a
  Storybook/component-test lane — adopt when the design system
  stabilizes, not before.
- **Publication mechanics** (founder decision): whether to publish full
  git history or squash-import into the public repository.

Next product arcs beyond the baseline groom live in the spec's build
backlog; route findings as you work — implementation decisions to
`decisions/`, spec/ticket defects to a local note in `feedback/`
(git-ignored).
