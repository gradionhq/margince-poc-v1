# Why authorization lives below the handlers

Most web applications gate authorization in the HTTP layer — a middleware or
controller checks the permission, and everything behind it trusts the caller.
This codebase deliberately does not. If you are looking for the auth check in
a handler and not finding it, this page is the explanation: **the check is at
the store/service entry point, and that is a design decision, not drift.**

## The problem with handler-only authorization

HTTP is only one of the callers. The same module behavior is reached by:

- the REST surface (`internal/compose/server.go`),
- the MCP tool surface (`cmd/mcp`, the hosted transport),
- agent runs (the Surface-B runner acting under a passport),
- workers (retention, reconciliation, close-date sweeps, the outbox relay's
  consumers),
- compose orchestration flows (briefs, reports, exports, enrichment).

A permission check that lives in HTTP middleware protects exactly one of
those paths. Every other caller becomes a bypass waiting to happen. So the
shared boundary is the one place all of them must pass: the module's store
(CRUD modules) or service (engine modules) entry points.

## The two layers, precisely

**Admission** — *may this principal perform this kind of action at all?* —
is minted in one place, `platform/auth`: `Admit` combines the agent gate
(🟢/🟡 tier), the seat ceiling, and object RBAC, re-derived live on every
call so a revocation binds mid-session (ADR-0055). Handlers decode the
request, validate transport shape, and encode the response; they never
decide authorization.

**Row scope** — *may this principal see or touch this particular row?* — is
enforced where the SQL is: `auth.Require` + `auth.EnsureVisible` at store
entry, the list-scope clauses composed into every query, and
`auth.EnsureLinkTarget` for references. Row-scope checks often have to run
inside the same transaction as the read or write they guard — checking in a
handler would be checking a different snapshot. Beneath all of it, Postgres
row-level security (`FORCE RLS` on every tenant table, reachable only through
`database.WithWorkspaceTx`) is the structural backstop: even a bug above it
cannot cross a workspace boundary.

## The rules that follow

- **Anything that returns a record is a read** and carries the row-scope
  gate — including replay paths, conflict responses, and error paths. A 409
  that echoes a hidden row's id is a leak.
- **A foreign-key reference to a row-scoped record is also a read.** Naming a
  deal's organization, an activity's link target, or a merge survivor asserts
  the target exists — so the reference is gated like a read of the target
  (`auth.EnsureLinkTarget`).
- **Object denial answers 403** (`apperrors.ErrPermissionDenied`): you asked
  for something your role cannot do.
- **A row-scope miss answers 404** (`apperrors.ErrNotFound`): a record you
  cannot see must be indistinguishable from a record that does not exist,
  so a leaked UUID buys nothing (existence-hiding).
- **REST, MCP, agents, and workers therefore share one gate.** An agent under
  a passport is capped by the granting human's live seat and RBAC; the same
  store entry point enforces both.

## Where this is recorded

The RBAC defaults and row-scope semantics are decisions/0006; the
transport-agnostic admission gate is ADR-0055 (decisions/0012); the spec-side
control map is `architecture/06-governance-as-structure.md` in the spec repo.
The binding short form lives in [AGENTS.md](../../AGENTS.md) under "The write
shape".
