# 0024 — Custom fields: `modules/customfields` + the owner-privileged schema pool

Date: 2026-07-11. Ratifies the placement and mechanism for the custom-fields
arc (Wave 2, arc 2a of the poc-1 delta port; foundation tickets
CF-T01…CF-T05). This record covers the design; the contract surface lands
with it (four `/custom-fields` path items in `crm.yaml`), and the migration,
engine, service, and HTTP wiring follow in the same PR arc.

## Context

The tickets demand workspace-admin-defined scalar fields on the five core
objects — a **real runtime `ALTER TABLE`** (CF-T03 AC-1), with EAV and
jsonb value stores explicitly forbidden (NEVER-1, DM-CONV-16). poc-1 proved
the mechanism: a single transaction that adds the physical column, inserts
the catalog row, and writes exactly one audit entry — Postgres transactional
DDL makes the three land or roll back together.

Two of this repo's hardenings make poc-1's shape unportable as-is:

- **poc-1's `platform/customfields` home is illegal here.** A field catalog
  is a domain aggregate — it owns a tenant table, an RBAC object, and a
  lifecycle — and `TestPlatformOwnsNoDomain` exists precisely to keep domain
  out of `platform/`. Compose cannot own it either: decisions/0018 rules
  that "a compose subpackage must never become the durable owner of a
  business entity."
- **The main app pool CANNOT run DDL.** Its role (`margince_app`) carries
  DML-only grants — a deliberate hardening over poc-1, whose pool's base
  role owned the tables. There is no path from the app pool to
  `ALTER TABLE person …`, and granting it one would dissolve the hardening
  for every query the product runs.

So the question this record answers: where does the catalog live, and what
executes the DDL?

## Decision

**A new `modules/customfields` owns the catalog aggregate** (Handlers→Service
engine-module shape, like approvals — the multi-step create is domain logic,
not a CRUD store). **The DDL executes on a dedicated, boot-optional
schema-change pool** — an owner-privileged DSN configured as flag
`--schema-dsn` / env `MARGINCE_SCHEMA_DSN`, injected by compose as a second
pool into the service via a `WithSchemaPool` option — the same
conditional-seam pattern as blobstore (decisions/0022) and keyvault
(decisions/0023).

### The one-transaction shape (DDL first, then downgrade)

Inside the engine's ONE transaction on the schema pool:

1. `ALTER TABLE <object> ADD COLUMN cf_<slug> <type>` runs first, as the
   schema pool's owner role — the only statement that needs the privilege.
2. `SET ROLE margince_app` + the workspace GUC downgrade the connection to
   exactly the authority every other tenant write runs under.
3. The RLS-governed catalog INSERT and the audit row commit under that
   downgraded role.

Postgres transactional DDL makes the three land or roll back together —
poc-1's proven dance, relocated onto the dedicated pool. The unique indexes
on `(workspace_id, object, slug)` and `(workspace_id, object, column_name)`
turn a mid-transaction collision into a whole-transaction rollback,
including the ALTER. The owner credential is used by exactly one audited
chokepoint (the engine's transaction); tenant traffic never reaches the
schema pool.

### Unwired by default

The seam is boot-optional: with no `--schema-dsn`, `createCustomField` and
`updateCustomFieldOptions` (the two DDL paths) answer 501, while catalog
reads, rename, and retire — app-pool, DML-only — still work. Unwired-by-
default keeps the OSS baseline conservative: an operator opts into runtime
DDL by minting and mounting the second credential, and a deployment that
never does retains poc-v1's existing privilege posture unchanged. A
narrowly-held second credential used by one audited chokepoint **extends**
the security posture, not breaks it — the alternative (widening the app
role) would.

### Why the illegal homes are illegal

- **`platform/` owns no domain.** The catalog is a business entity with
  tenant rows, RBAC, and lifecycle; `TestPlatformOwnsNoDomain` is the
  tree-derived gate. Platform packages are plumbing (pool, storekit, auth,
  events) that any module may consume — a catalog is not that.
- **`compose/` never durably owns a business entity** (decisions/0018).
  Compose wires cross-module edges; the pilot exception (`compose/briefs`)
  is an orchestration group over other modules' entities, not an owner.
- **A new module is the only ratified home**, and the fitness tests
  (arch_test.go, depguard, tableownership) derive their package lists from
  the tree, so `modules/customfields` auto-enrolls; only `.go-arch-lint.yml`
  needs explicit component entries.

### `cf_` vs `x_` — two distinct custom namespaces

Runtime-added columns carry the `cf_` prefix (CUSTOM-FIELDS-SCHEMA-2),
derived server-side from the label (never client-supplied). The fork seam's
`x_` prefix (ADR-0054 §7 — fork-owned migrations in `migrations/custom/`)
remains distinct: `x_` columns arrive by reviewed fork migration, `cf_`
columns by the governed runtime engine. Both surface as "custom" through
`datasource.FieldDef.Custom` (the doc note lands with arc 2a-ii, when
values ride record payloads).

## Rejected alternatives

- **Deploy-time-only migrations.** No runtime mechanism at all — fails
  CF-T03's acceptance outright; an admin adding a field must not require a
  release.
- **jsonb / EAV value store.** Explicitly forbidden (NEVER-1, DM-CONV-16):
  values live in real columns with real types and real CHECKs, or the type
  system, indexes, and the sort/filter vocabulary are all lies.
- **`platform/customfields` (poc-1's home).** Owns domain — see above.
- **Running the API as the table owner.** Breaks `db-init.sql`'s invariant
  (the app role is DML-only by construction) and re-creates poc-1's
  weakness everywhere to serve one endpoint.

## Consequences

- **Capability win:** admins get governed runtime fields — the closed
  6-type × 5-object matrix, catalog-listed, audited, RLS-scoped — without
  any EAV shadow schema.
- **Contract change is additive:** four new `/custom-fields` path items +
  five schemas; the oasdiff breaking-change gate stays green. Create /
  retire / options are 🟡 on the existing transport-agnostic autonomy gate
  (a schema change is never 🟢); list / rename are 🟢.
- **New config surface:** `--schema-dsn` / `MARGINCE_SCHEMA_DSN`
  (documented in docs/reference/configuration.md when the wiring lands);
  absent ⇒ the two DDL operations answer 501.
- **Blast radius contained:** one new module + a compose option + one
  additive migration for the catalog table; no change to the app pool's
  grants, RLS policies, or the storekit write shape — the catalog INSERT
  uses the same audit+outbox spelling as every other mutation.
- **Ops posture:** the schema DSN is the owner credential — operators hold
  it like a migration credential, because that is what it is; the engine is
  the only consumer.

## Resolved (2026-07-11, the engine task) — global column-namespace collision across workspaces

The unique indexes are **per-workspace**, but the physical column namespace
on the shared table is **global**: two workspaces deriving the same slug
(e.g. both add "Renewal date" on deal) collide on the second
`ADD COLUMN cf_renewal_date` with `42701 duplicate_column` — a case poc-1
never resolved.

**Decision: keep the tickets' global `cf_<slug>` names and surface the
collision as an honest 409.** Inside the engine's transaction, BEFORE the
ALTER, the create pre-checks `information_schema.columns` for the derived
column on the target table:

- column exists **and this workspace holds a catalog row** for it → the
  ordinary per-workspace duplicate-slug conflict (409, "already exists on
  <object> in this workspace") — the same answer the unique index gives.
- column exists **with no catalog row here** → another workspace claimed
  the name: a typed `ColumnTakenError` → 409 with the actionable remedy
  ("this field name is taken platform-wide; choose another label"). The
  response discloses only that a bare column NAME is in use — not which
  tenant holds it or what it contains.
- `42701` from the ALTER itself is caught as the same `ColumnTakenError`
  (belt for columns added outside the engine, e.g. a fork's `x_`-adjacent
  migration racing a create).

Concurrent creates are serialized by a `pg_advisory_xact_lock` keyed on
the target table (`hashtextextended('customfields:' || object)`), taken
before the pre-check: the loser of a race sees the winner's committed
column in the pre-check instead of colliding mid-ALTER, so the 42701 path
is a defensive branch, not the mechanism. The lock is transaction-scoped
and auto-releases at COMMIT/ROLLBACK; SetOptions takes the same lock
(row-lock-then-advisory in every flow that holds both, so the two DDL
paths cannot deadlock each other).

This is an **honest limitation of the shared-table model**: tenant A
claiming `cf_renewal_date` on `deal` blocks tenant B's identical label
platform-wide (B picks another label; the per-workspace catalog stays
authoritative for what B actually has). Rejected alternatives:

- **Per-workspace column prefixes (`cf_<wsprefix>_<slug>`)** — removes
  the cross-tenant collision but breaks the tickets' `cf_<slug>` naming
  contract (CUSTOM-FIELDS-PARAM-3/SCHEMA-2: column_name derives from the
  slug alone), and leaks a workspace identifier into every physical
  column name.
- **Sharing the physical column across workspaces when the type matches**
  — RLS would isolate the values, but the column's lifecycle (retire,
  options CHECK regeneration) is per-workspace catalog state: one
  tenant's options edit would rewrite a CHECK constraint that other
  tenants' values must then satisfy — cross-tenant blast radius through
  DDL, disqualifying.

### Resolved with it — the write shape's event half

The catalog mutations are **audit-only by ratification**: the closed
event catalog (events.md §5) defines no `custom_field.*` type, and the
spec's custom-fields.md §Events pins the add/rename/retire trail to the
audit entry ("not a bus event"). A cross-object catalog change also has
no single family stream to ride (the same reasoning as attachments).
Ratified in `backend/writeshape_test.go`'s `auditOnlyWrites`; custom-field
*value* changes on records will ride the owning entity's `*.updated`
delta events when values land (arc 2a-ii).
