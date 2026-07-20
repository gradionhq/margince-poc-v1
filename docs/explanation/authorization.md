# Authorization & access control

How Margince decides **who may do what**, and — importantly — **where** that decision is made. If you
are looking for the auth check in an HTTP handler and not finding it, this page is the explanation:
the check is at the store/service entry point, and Postgres row-level security is the backstop beneath
it. That is a design decision, not drift.

## Why authorization lives below the handlers

Most web apps gate authorization in the HTTP layer — a middleware checks the permission and everything
behind it trusts the caller. This codebase deliberately does not, because **HTTP is only one caller**.
The same module behavior is reached by:

- the REST surface (`internal/compose/server.go`),
- the MCP tool surface (`cmd/mcp`),
- agent runs (the Surface-B runner acting under a passport — see [agent-surface.md](agent-surface.md)),
- workers (retention, reconciliation, the close-date sweep, the outbox relay's consumers),
- compose orchestration flows (briefs, reports, exports, enrichment).

A check in HTTP middleware protects exactly one of those paths; every other caller becomes a bypass.
So the check lives at the one boundary all of them cross: the module's **store** (CRUD modules) or
**service** (engine modules) entry points.

## Who is calling — principals and the three transports

Every request carries a **principal** (`shared/kernel/principal`): its type (human / agent / connector
/ system), its identity, its seat, its scopes, and — for agents — the human it acts on behalf of.
There are three ways a caller reaches `/v1` or the tools, and **all three resolve the same admission +
RBAC**:

| Caller | Transport | Credential |
|---|---|---|
| A **human** | web app / HTTP | the `crm_session` cookie (from `POST /v1/auth/login`) |
| An **agent** | REST | `Authorization: Bearer mgp_…` (a passport) |
| An **agent** | MCP (stdio or hosted HTTP) | a passport (`MARGINCE_PASSPORT_TOKEN`, or an OAuth-minted bearer) |

(No request names a tenant: one installation serves one organization (A107/ADR-0061), and the
admission middleware binds that singleton workspace itself before any handler runs.)

### What a passport is

A **passport** (formally an *Agent Seat Passport*) is the credential an AI agent uses on both agent
transports. It is a scoped, expiring, revocable bearer token — a `mgp_`-prefixed string — that a
**human mints for an agent** (`POST /v1/passports`, session-authed, human-only). Two properties make
it safe:

- **An agent never has more rights than the human who minted it.** Effective authority is the
  passport's scopes (`read`, `draft`, `write`, `send`, `enrich`) **intersected with the granting
  human's live RBAC and seat**.
- **It is re-authenticated on every call**, and the human's seat + RBAC are re-derived each time — so
  revoking the passport (or demoting the human) **binds mid-session**, including for an MCP session
  that is already connected.

Minting and using one: [how-to/mint-a-passport.md](../how-to/mint-a-passport.md) →
[how-to/run-the-mcp-server.md](../how-to/run-the-mcp-server.md).

## The two layers, precisely

**1. Admission — *may this agent take this kind of action at all?*** `platform/auth`'s `Gate.Admit`
combines the agent **scope**, the **seat ceiling** (a `read` seat may GET but never mutate), and the
**autonomy tier** (below), re-derived live on every call. Object-level RBAC and row visibility are
**not** `Admit`'s job — they live at the store (layer 2). Handlers decode the request and encode the
response; they never decide authorization.

**2. Object RBAC + row scope — *may this principal do this to this particular record?*** Enforced where
the SQL is, at the store/service entry: **`auth.Require`** (object level — does the role grant this
verb on this object type?), plus **`auth.EnsureVisible`** / the list-scope clauses (`ScopeClause`) /
`auth.EnsureLinkTarget` (row level — may they see this row?). These often must run **inside the same
transaction** as the read or write they guard — checking in a handler would be checking a different
snapshot. What the roles actually grant, how row scope (own/team/all) and teams decide "which rows,"
and how a per-record share widens visibility on top, is its own page:
[rbac-roles-and-teams.md](rbac-roles-and-teams.md).

## Autonomy tiers — how agent actions are governed (🟢 / 🟡)

An action's autonomy tier is **declared once in the contract** (`x-mcp-tool: { tier: … }`) and enforced
**below the transport**, so REST and MCP behave identically:

- **🟢 (green)** — reversible internal actions **auto-execute**, audited, with agent-stamped provenance.
- **🟡 (yellow)** — outbound / irreversible actions (send, merge, archive, close a deal, …) **stage a
  confirm-first approval** that a human decides in the inbox; the agent then redeems the decision by
  re-issuing the same call.
- **Human-only** routes (approvals, consent, DSR) **refuse an agent principal outright**.
- A mutating operation carrying **no tier is default-denied** for agents (the agent-policy generator
  refuses to ship an un-tiered mutation in the first place — see
  [contract-first.md](contract-first.md)).

Approving is always human-only, and an agent never exceeds the granting human's live authority.

## The structural backstop — Postgres row-level security

Everything above is application code; beneath it, the database itself enforces tenant isolation, so a
bug in a scope clause still cannot cross a workspace boundary.

- Every tenant table has `ENABLE`+`FORCE` row-level security with a **deny-on-unset** policy keyed on
  the `app.workspace_id` GUC (`FORCE` binds even the table owner; `ENABLE`-only "looks secure and is
  not").
- Those tables are reachable **only** through `database.WithWorkspaceTx`, which binds the GUC
  transaction-local (`SELECT set_config('app.workspace_id', $1, true)`) and **fails closed before any
  SQL** if no workspace is bound. If the GUC is unset, the policy's `NULLIF(current_setting(...), '')`
  is `NULL` and `workspace_id = NULL` is never true → the connection **sees zero rows and writes
  nothing**.
- The runtime **`margince_app`** role is not a superuser, has no `BYPASSRLS`, and does not own the
  tables — so there is no bypass path. (The schema owner `margince_owner` is used only by migrations.)
- Every tenant-local foreign key is composite `(workspace_id, col)`, so a cross-workspace reference is
  rejected by the database.

Both RLS coverage and composite-FK obligations are fitness functions derived from the live schema —
see [write-backbone.md](write-backbone.md) for the write path that rides inside this transaction.

## How it is wired

- **One gate, injected once.** `platform/auth` is the admission point; no module re-implements it. It
  depends on the `ports/authz` seam (implemented by `identity`) so platform never imports a module.
- **At the store/service entry** every exported method calls the gate (`auth.Require` +
  `auth.EnsureVisible`); a fitness test (`rbacgate_test.go`) fails any store entry point that doesn't.
- **REST** rides a compose middleware (`agentGate`): for an agent principal it resolves the operation's
  tier/policy from the generated admission table before the handler runs, and default-denies an
  un-policied mutation. A human caller skips it — their RBAC at the store *is* the approval.
- **MCP** binds the same tool registry and admission gate (`compose.NewRegistry`) — one gate, two
  transports.

## The rules that follow

- **Anything that returns a record is a read** and carries the row-scope gate — including replay,
  conflict, and error paths. A 409 that echoes a hidden row's id is a leak.
- **A foreign-key reference to a row-scoped record is also a read** (`auth.EnsureLinkTarget`): naming a
  deal's organization or an activity's link target asserts the target exists, so it is gated like a
  read of that target.
- **Object denial answers 403** (`apperrors.ErrPermissionDenied`): your role cannot do this at all.
- **A row-scope miss answers 404** (`apperrors.ErrNotFound`): a record you cannot see is
  indistinguishable from one that does not exist, so a leaked UUID buys nothing (existence-hiding).
- **REST, MCP, agents, and workers share one gate** — an agent under a passport is capped by the
  granting human's live seat and RBAC at the same store entry point.

## Where this is recorded

The binding short form is in [AGENTS.md](../../AGENTS.md) under "The write shape".
