# Custom fields — the governed add-field engine

How a workspace admin adds a field to `person` at runtime without anyone shipping code, and why that
power is fenced in as tightly as it is. `customfields` is the **single chokepoint in the system
allowed to run a runtime `ALTER TABLE`** — every other module is forbidden it. A custom field is a
real, typed, physical column (`cf_<slug>`) on the core object's own table; the `custom_field` catalog
is the system-of-record describing it.

**The rejected alternative names the design.** The obvious way to ship user-defined fields is an EAV
value store — a generic `(record_id, key, value)` sidecar. Margince refuses it, and the reason is
narrower than "EAV is slow": a generic value column **cannot carry a per-field type or constraint**
(one `text` column cannot be both a date and a three-way picklist), so every read pivots rows back
into columns through joins and casts, and the planner estimates those pivots poorly on exactly the
reporting queries custom fields exist to serve. An EAV table *can* be indexed — what is lost is
**type-specific** constraints and indexes, and honest `GROUP BY`, not indexability as such. The spec
names the same bet from the other side: a custom field is a real column so that reporting stays
honest SQL with no metadata-engine indirection on the hot path. Paying for that with one governed DDL
path is the trade this module exists to make. Picklist values live in the catalog's own `options`
jsonb column — that is field *metadata*, not a value store.

## What a custom field may be — the closed sets

Six types (`text`, `number`, `date`, `currency`, `picklist`, `boolean`) on five objects (`person`,
`organization`, `deal`, `lead`, `activity`). **No cap on how many, no widening of what** — the
surface itself is the knob. Each type maps to one storage type: `number` → `numeric` (round-tripped
as a string, never a float, so precision survives), `currency` → `bigint` minor units with the
ISO-4217 code held in the catalog row rather than the column, `picklist` → `text` plus a generated
`CHECK` constraint.

Those literals are spelled in three places — the engine's constants, the catalog's `CHECK`
constraints (`migrations/core/0063`), and `ports/fieldcatalog` — and **must never drift apart**. The
port carries its own copy because `shared` may not import `modules` without inverting the DAG.

The admin never names the column. `label` is the only text a caller supplies; the engine derives the
slug from it (lowercased, non-alphanumeric runs collapsed, capped at 40 chars so that
`cf_<slug>_check` stays under Postgres's 63-byte identifier limit) and the column from the slug.
Column identity is **server-derived and immutable** — rename moves the label and nothing else.

## The structural refusal

A label that smells like a new object, a relationship, a formula, or a validation rule is **refused,
never quietly accepted as a text column**. It answers a 422 `structural_change_refused` that names
the route out (`details.route: source_development_path`). Runtime custom fields add bounded scalar
attributes to objects that already exist; anything structural ships as a reviewed source change. The
check is a keyword heuristic over the label — deliberately blunt, and deliberately biased toward
refusing, because the failure it prevents (a "Linked Contract" text column that silently becomes a
fake relationship) is far worse than the one it causes (an admin rewords a label).

## The privilege boundary

The engine rides **two pools with deliberately different authority**. The app pool
(`margince_app`, RLS-bound, DML-only) serves every catalog-only operation. The owner-privileged
**schema pool** is touched by exactly two paths — create, and a picklist's options edit — because
only those run DDL.

Inside one transaction on that pool:

1. Bind the workspace GUC, bound every lock wait (`SET LOCAL lock_timeout = '2s'`), and take a
   transaction-scoped **advisory lock keyed on the target table**.
2. Pre-check the column namespace, then run **the one privileged statement** — the `ALTER TABLE`.
3. **`SET LOCAL ROLE margince_app`** — the transaction downgrades itself to exactly the authority
   every other tenant write runs under, and the catalog `INSERT` + audit row land under forced RLS
   with no owner privilege in reach.

Postgres's transactional DDL makes the column, the catalog row, and the audit entry land or roll back
**together** — a half-added field is not a state this system can reach.

Two details carry more weight than their size suggests. **The DDL is generated only from the
validated spec**, never from raw request text: identifiers go through `pgx.Identifier.Sanitize`,
picklist literals through the module's own quoter, and both re-validate at the DDL boundary rather
than trust the request-side check that already ran. And **the `lock_timeout` is not a nicety** — an
`ACCESS EXCLUSIVE` request queued behind one long-running reader parks every subsequent DML on a
shared core table behind it: a platform-wide stall from a single admin call. Timing out instead
answers a retryable 409 (`ErrTableBusy`). The advisory lock closes the duplicate-column race between
two concurrent creates; lock order is row-then-advisory in every flow holding both, so the two DDL
paths cannot deadlock each other.

`privilege_boundary_test.go` pins both downgrade call sites, because deleting one is invisible: the
schema pool's role is superuser in dev, so `FORCE RLS` would not bite and every other test would
still pass.

**Two honest surprises.** The schema pool is **unwired by default** — without `--schema-dsn`, create
and options-edit answer 501 and declare the gap by omission rather than nil-dereferencing at request
time (the unwired-blobstore posture); wired, it also gains a `/readyz` probe. And the physical column
namespace on a shared core table is **global across workspaces**, which per-workspace unique indexes
cannot see: a slug another tenant already claimed answers a 409 naming the remedy ("choose another
label"). A bare column name discloses nothing about who holds it.

## The lifecycle: retire, never drop

**Retire is a status flip.** The physical column and every value in it are preserved — the engine
never issues a `DROP COLUMN`. The field leaves record payloads and the sort/filter vocabulary; the
row stays fetchable; `archived_at` stays null (retire is not an archive). Because the column survives,
**the slug stays reserved**: the catalog's unique indexes cover retired rows too, and the admin list
deliberately does not default-exclude them — it is the one surface that still shows a retired field.

Retirement is **terminal**: a retired field refuses rename and options edits with a 409. Re-retiring
is a no-op that returns the row unchanged and writes nothing to the audit trail — nothing changed.

An options edit regenerates the picklist's `CHECK` from the new set, and `ADD CONSTRAINT` validates
existing rows: **removing an option that records still use refuses the edit** ("migrate them first")
rather than stranding data outside its own constraint. A picklist always keeps at least one value.

Changing the catalog is **admin/ops-owned; every role may read it** — the pipeline-config precedent,
because a field definition reshapes what the system stores for everyone's records. The catalog is
workspace-shared config with no `owner_id`, so the object grant is the whole authority question and
there is no row scope to compose. Creation is 🟡: an agent caller stages for approval upstream, and a
human's direct call is itself the approval. The field is attributed to the human — or, behind an
agent, to the granting human (*agent ≤ human*). Catalog changes are **audit-only by ratification**:
the closed event catalog defines no `custom_field.*` type, and a cross-object catalog change has no
single family stream to ride.

## How record stores see `cf_*` columns

A record store **never imports this module** — that would be the sibling edge ADR-0054 §3 forbids.
It depends on `ports/fieldcatalog.Reader`, a one-method seam answering *which `cf_*` columns are
active on this object, and of what type*. Compose injects the concrete service; a nil Reader is the
zero-cost pass-through for tests and deployments that never mounted the module.

`Column` is deliberately thin — name and type, nothing else. Slug, label, lifecycle status, and
picklist options stay inside `customfields`: a record store has no business with admin metadata. The
store drives `storekit`'s custom-column helpers, which are pure SQL-fragment and value mechanics that
touch no database, and folds the result into the same `Patch` that carries core columns — so custom
fields ride the ordinary audit before/after and version-guarded update with no extra bookkeeping.
Note that the seam-side service is wired with a **nil schema pool**: reading the catalog never needs
DDL authority.

`ActiveColumns` runs no RBAC check, and that is deliberate rather than an oversight: it is called
from inside a store's own gated `Get`/`List`/`Create`/`Update`. What it exposes is workspace-visible
schema shape — the same thing the admin list already answers — not row data. The store's row-level
gate is what protects the values.

One rule surprises people: custom-field values convert **drop-on-mismatch**. A request body's
`additionalProperties` carries no per-key shape contract, so a value whose shape does not match its
column's type is silently excluded rather than answered with a 422.

## No `cf_*` column is indexed — an open discrepancy

The engine emits `ADD COLUMN` and, for a picklist, its `CHECK`. **It creates no index, ever** — yet
custom fields are first-class in the list vocabulary: `?sort=cf_contract_end` and `?cf_region=emea`
are both accepted, validated against the active catalog, and compiled into real SQL. Such a query is
answered by narrowing to the workspace on a core index and then scanning that workspace's rows to
filter or sort on the unindexed `cf_` column. At PoC volumes this is invisible. For a large
workspace it is work proportional to the workspace's row count *per page*, since keyset paging
repeats the sort on every page.

This is recorded here rather than settled here, because **the spec and this code disagree, and under
P3 the spec wins**. The spec's language is split. `features/10` and `E15` promise a real
**indexable** column — which this delivers, and which is precisely the point of refusing EAV. But
`data-model.md` §9 promises a real **indexed** column that reports group/filter on "at the same speed
as a core column", names "static schema → real indexes → correct, fast reporting" as the P1/P2
honesty bet, and its worked example creates one alongside the column:

```sql
CREATE INDEX idx_org_renewal_risk
  ON organization (workspace_id, renewal_risk)
  WHERE renewal_risk IS NOT NULL AND archived_at IS NULL;
```

`EP02` goes further and asks for a test asserting the new column "exists, **is indexed**, and
round-trips". No such index and no such test exist here.

Worth knowing before anyone closes the gap: **add-field is the cheapest possible moment to index.**
The column is brand new, so every value is NULL, so a partial index `WHERE <col> IS NOT NULL` starts
empty — and the transaction already holds `ACCESS EXCLUSIVE` for its `ALTER`. Indexing later is the
expensive path, and `CREATE INDEX CONCURRENTLY` — the usual way to dodge the write-blocking build —
**cannot run inside a transaction block**, so it cannot be reconciled with this module's
one-transaction guarantee. The real open question is not *how* but *which*: an index per field on
create (simple; pays storage and write amplification on every field nobody ever filters), or an
explicit admin "index this field" operation (honest about the cost; new surface to govern). That
choice belongs upstream in the spec, not to whoever implements it next.

## Where the code lives

| | |
|---|---|
| The pure engine (validation, slug/DDL generation, quoting) | `internal/modules/customfields/engine.go` |
| The service seam, typed refusals, catalog scan | `internal/modules/customfields/service.go` |
| The two DDL paths | `internal/modules/customfields/create.go`, `options.go` |
| Rename + retire (catalog-only, app pool) | `internal/modules/customfields/lifecycle.go` |
| The `fieldcatalog` provider half | `internal/modules/customfields/catalogreader.go` |
| The cross-module seam | `internal/shared/ports/fieldcatalog/` |
| The record-store mechanics | `internal/platform/database/storekit/customcolumns.go` |
| The sort/filter vocabulary `cf_*` columns join | `internal/platform/database/storekit/listquery.go` |
| The catalog table + its RBAC backfill | `backend/migrations/core/0063_custom_field_catalog.up.sql`, `0064_custom_field_rbac.up.sql` |
| The privilege-boundary gate | `internal/modules/customfields/privilege_boundary_test.go` |
| The admin UI | `frontend/src/screens/customfields.tsx` |

The owner-pool flag and its `/readyz` probe: [reference/configuration.md](../reference/configuration.md).
The write shape these mutations still ride: [write-backbone.md](write-backbone.md). The role matrix
behind the admin/ops posture: [rbac-roles-and-teams.md](rbac-roles-and-teams.md).
