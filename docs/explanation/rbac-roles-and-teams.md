# Roles, teams, and record sharing

The companion to [authorization.md](authorization.md). That page explains **where** the access check
lives (at the store, with Postgres RLS beneath) and how the three transports resolve one gate. This
page explains the **data model that gate reads**: what a role grants, how row scope narrows it, how
teams widen it, and how a single-record share layers on top.

If you just watched a freshly-created user get "permission denied" on every screen, skip to
[A user with no role sees nothing](#a-user-with-no-role-sees-nothing) — that is almost always why.

## Three independent questions

A read or write is allowed only when all three pass. They are separate gates; widening one does not
substitute for another.

1. **Admission** — *may this caller act at all?* Scope ∧ seat ceiling ∧ autonomy tier. (See
   authorization.md; not covered here.)
2. **Object RBAC** — *may this role do this verb on this **type** of record?* e.g. "may a `rep`
   `read` a `deal`?" Decided by the caller's **role permissions**. Failure → **403**.
3. **Row scope** — *may this caller see this **particular** record?* e.g. "may this rep read *deal
   #42*?" Decided by **row scope + record grants**. Failure → **404** (existence-hiding — a row you
   can't see is indistinguishable from one that doesn't exist).

The trap the whole feature hinges on: **a record share only answers question 3.** It never grants
question 2. Sharing a deal with someone whose role has no `deal.read` still denies them — and the
share is invisible until they have a role that clears the object gate.

## Roles

A role is a row in the `role` table (`migrations/core/0002_identity.up.sql`), scoped to one
workspace. Its `permissions` JSONB holds two things:

- **`objects`** — a per-object-type grant of `{create, read, update, delete}` over the ~20 core
  objects (`person`, `organization`, `deal`, `lead`, `activity`, `pipeline`, `list`, `custom_field`,
  `quota`, …).
- **`row_scope`** — `own` | `team` | `all` (see below).

A fresh workspace is seeded with five **system roles** (`is_system = true`), whose exact grants are
compiled in and are the source of truth — do not transcribe the full matrix elsewhere, it will
drift. Read it in **`backend/internal/modules/identity/internal/policy/policy.go`** (`defaults`).
The shape:

| Role | Posture | Row scope |
|---|---|---|
| `admin` | Full CRUD on everything (config included). | `all` |
| `ops` | Same CRUD reach as admin — the operations counterpart. | `all` |
| `manager` | CRUD on records; **read-only** on config (pipeline, automation, custom_field, quota). | `team` |
| `rep` | Create/read/update records (delete only where it's routine, e.g. disqualify a lead); **read-only** on config. | `team` |
| `read_only` | Read everything; write nothing (except one's own saved views). | `all` |

Two things surprise people:

- **`read_only` is `row_scope: all`, `rep`/`manager` are `row_scope: team`.** Scope and object reach
  are orthogonal — a read-only auditor is *meant* to see the whole workspace; a rep is scoped to
  their team's records but can write them.
- **Config objects (pipeline, custom_field, automation, quota) are read-only below admin/ops.** This
  is why a `rep` gets `pipeline.read: permission denied`-adjacent behaviour only when they have **no
  role at all** — with the `rep` role they *can* read pipelines; they just can't edit them.

Custom roles are additive on the same shape. When a user holds several roles, permissions **merge to
the widest** held (object grants union; row scope takes the widest — `all` > `team` > `own`); see
`policy.Merge`.

## Row scope — which rows of a permitted object

Row scope is evaluated in SQL at every list/read over an owner-scoped table
(`platform/auth/rbac.go`). Given the object gate already passed:

- **`all`** — no row filter. Sees every row in the workspace. (`Unbounded` — also the system actor.)
- **`team`** — sees rows they **own**, rows owned by a **teammate** (any member of a team they belong
  to, via `team_membership`), and **ownerless** rows.
- **`own`** — sees rows they own, and ownerless rows.

**Ownerless rows (`owner_id IS NULL`) are workspace-shared and visible at every scope** — that's how
reference-ish records stay readable regardless of scope.

Note that `owner_id` is **optional and not auto-stamped on create** — a person/organization/deal
created without one (the API and the current create UI both omit it) is ownerless, hence
workspace-visible, until an owner is assigned. This is why the dev seed explicitly makes Demo Admin
the owner of its records (`scripts/seed-dev.sql`): otherwise every scope would see them and row
scope / sharing couldn't be observed.

No system role ships `row_scope: own`; it exists for custom roles. The seeded `rep`/`manager` are
`team`, so team membership is what actually decides who sees whose records.

## Teams

A **team** (`team` table) is a named group; **`team_membership`** joins users to teams (many-to-many
— a user can be in several). Teams do two jobs:

1. **They resolve `row_scope: team`.** "My team's records" = records owned by anyone sharing a team
   with me. Add a rep to a team and they immediately see that team's records (subject to the object
   gate).
2. **They are a share target.** A record grant can name a team instead of a person, so everyone in
   it — present and future members — gets the widened visibility.

Teams do **not** carry their own permissions — a team is not a role. (A role *assignment* can be
scoped to a team, but the grants still come from the role.)

## A user with no role sees nothing

`role_assignment` links a user to a role. **A user with zero role assignments has zero object
permissions** — every object gate (question 2) fails closed, so every list and record 404/403s, even
the pipeline board. This is not a row-scope subtlety; the user simply cannot clear the object gate
for anything.

The workspace bootstrap assigns the founding admin the `admin` role (`identity/service.go`,
`seedSystemRoles`). Any user created by another path — a SQL seed, a future invite flow — **must be
given a role explicitly**, or they log in to a wall of permission errors. (This is exactly what bit
the dev seed's second user before it assigned `rep`; see `scripts/seed-dev.sql`.)

## Record sharing — a per-record grant on top of scope

Row scope is coarse (own / team / all). **Record sharing** (A52/ADR-0039) is the fine-grained layer:
grant **one specific record** to **one person or team**, at **read or write**, optionally expiring,
with a reason. This is the Share screen (`frontend/src/screens/share.tsx`, `#/share/<type>/<id>`) and
the `record_grant` table / `/v1/record-grants` API.

How it composes with everything above:

- **It only widens question 3 (row visibility), never question 2 (object RBAC).** The grantee still
  needs a role granting the verb on that object type. Share a deal with a user whose role lacks
  `deal.read` and they still can't open it — the grant is inert until their role clears the object
  gate.
- **It applies only to shareable tables** — `person`, `organization`, `deal`, `lead` (`rbac.go`
  `shareableTables`; the `record_grant` CHECK is the schema-side twin). Config and other objects have
  no per-record share.
- **A `write` grant satisfies a read** (write ⊇ read).
- **It is evaluated live on every query** — the visibility predicate `OR EXISTS (…record_grant…
  AND (expires_at IS NULL OR expires_at > now()))`. So **revoking or expiring a share binds on the
  next read**, no session to wait out.
- **A grant can't exceed the granter.** The server rejects a grant wider than the granter's own
  access to that record (surfaced as `approval_required` / 422 in the UI), so sharing can't launder
  privilege.

In SQL terms, a read over a shareable table is `ownerPredicate OR liveGrantExists` — the grant is a
second way in, checked in the same statement as the scope filter (`VisiblePredicate` in `rbac.go`).

## Worked example — the dev seed

The dev seed (`scripts/seed-dev.sql`) sets up three seats so every branch above is observable:

- **Demo Admin** — `admin` role, `row_scope: all`, member of **DACH Sales**. Owns the seeded people
  and deals; sees everything.
- **Rep One** — `rep` role, `row_scope: team`, member of **DACH Sales** (with Demo Admin). *The
  team-scope seat.*
  - Object gate: `rep` grants `deal.read`, `pipeline.read` (read-only) → the deals board loads.
  - Row scope: `team` → Rep One sees every record owned by a DACH Sales teammate — i.e. **all of
    Demo Admin's records, with no share needed**. A per-record share on top is redundant, so Rep One
    is *not* how you observe sharing; they're how you observe team scope.
- **Rep Two** — `individual` role (an own-scoped clone of `rep`), **in no team**. *The sharing seat.*
  - Object gate passes (same object grants as `rep`) → the board loads, but **empty**.
  - Row scope: `own` → owns nothing, has no teammates → sees **zero records by default**.
  - Share a specific person/deal with Rep Two (or with a team you add them to) and it appears — the
    grant is the **sole** reason it's visible, which is exactly what makes sharing observable.

Remove a user's role assignment entirely and every read fails at the object gate (403/404 across the
board) — the symptom that means "no role," distinct from "role present but scope hides the row."

## Where this is enforced (pointers)

- Role definitions + merge: `backend/internal/modules/identity/internal/policy/policy.go`
- Row-scope + record-grant SQL predicates: `backend/internal/platform/auth/rbac.go`
  (`OwnerPredicate`, `VisiblePredicate`, `ScopeClauseFor`)
- Schema: `role`, `role_assignment`, `team`, `team_membership`, `record_grant`
  (`backend/migrations/core/`)
- The enforcement architecture (one gate, three transports, RLS backstop):
  [authorization.md](authorization.md)
