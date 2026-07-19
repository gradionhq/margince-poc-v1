# Architecture

The condensed map of how this codebase is shaped. New backend contributors
should start at [backend-onboarding.md](backend-onboarding.md) — the
orientation hub — and read this for the *why* behind the structure.

## The triad DAG

All Go code is one module under `backend/`, arranged as the
`internal/{shared,platform,modules}` triad plus a composition layer and
process roles. The dependency direction is one-way:

```
shared  →  platform  →  modules  →  compose  →  cmd
```

- **`internal/shared/`** — Tier-0 leaves, stdlib-only:
  `kernel/{ids,events,provenance,principal}`, `apperrors` (the fixed
  error-sentinel registry), and `ports/` (the frozen seam interfaces:
  authz, datasource, mcp, connector, workflow, model, retrieval,
  jurisdiction).
- **`internal/platform/`** — technical plumbing that owns no domain:
  `database` (pool + the RLS workspace-transaction contract) and
  `database/storekit` (the one spelling of the write shape), `auth` (the
  one admission point), `events` (outbox relay/subscriber/dedupe),
  `dbmigrate`, `httperr`, `httpserver`.
- **`internal/modules/`** — the bounded capabilities (identity, people,
  deals, activities, approvals, agents, ai, search, capture, consent,
  privacy, collections, signals, and the `de` jurisdiction pack). A
  module package starts flat (store + mapping + transport + provider in
  one package, ADR-0054 §3) and earns a subpackage only under the
  module growth policy —
  e.g. `capture/imap` (protocol adapter), `agents/runner` (independent
  engine), `identity/internal/policy` (hidden ruleset). A module
  **never imports a sibling**; every cross-module edge is injected by
  the composition layer.
- **`internal/compose/`** — the one composition seam every process role
  shares: the contract HTTP surface, the composite datasource provider,
  the MCP registry, and all cross-module wiring. Cross-module
  orchestration groups live in subpackages under the same growth policy
  (`compose/briefs`), and the cross-module integration suites live in
  `compose/integration`; compose subpackages coordinate modules and
  never durably own a business entity. How it boots and where every
  cross-module edge is wired: [composition-layer.md](composition-layer.md).
- **`cmd/{api,worker,migrate,mcp}`** — thin process roles.

`cmd/<role>` is reserved for those **four deployable process-role
binaries** (ADR-0054/A69). A *developer/CI harness* binary — a tool a
human or a `make` target runs, not a role that gets deployed — does not
belong there: it lives **beside the package it serves** (e.g. the AI
certification report tool at `internal/compose/aicert/reportcmd`, run by
`make e2e-ai-report`) or in the separate `backend/tools/` module (the
codegen chain). Two reasons: a harness under `cmd/<role>` would read as
a fifth deployment role and blur A69's pinned count, and keeping the tool
next to the code it imports (the `aicert` internals) means it moves and
versions with that code. The rule of thumb: if it is composed through
`internal/compose` and meant to run as a server/job, it earns a
`cmd/<role>`; if it is tooling around one package, it stays with that
package.

The DAG is enforced three ways, and deliberately mechanically: depguard
(golangci-lint), go-arch-lint, and the fitness tests in
`backend/arch_test.go`, which derive their package and module lists from
the tree — a new module is enrolled in the rules the moment its
directory exists, never by editing a list.

## The two spine shapes

Modules follow one of two sanctioned shapes — don't invent a third:

- **Handlers → Store** (CRUD modules: people, deals, activities, …).
  Transport handlers map contract DTOs and call the store; the store
  owns the transactional write shape and the RBAC gate at its entry
  points.
- **Handlers → Service** (engine modules: approvals, identity). A
  service object owns multi-step domain logic (decide/redeem,
  bootstrap/sessions) and drives stores/SQL inside it.

## The write shape

Every mutation commits **domain row + `audit_log` row + `event_outbox`
row in one transaction**, spelled once in `platform/database/storekit`
(`Audit` + `Emit`) and called by every store. Provenance
(`captured_by`) is stamped from the authenticated principal, never
accepted from a request body. Publishing is always through the outbox —
the relay ships committed rows to Redis Streams; no domain code touches
the bus directly — and consumers wrap handlers in `events.Dedupe`
because the bus is at-least-once. Every store entry point is RBAC-gated:
object denial answers 403, a row-scope miss answers 404
(existence-hiding). The full mechanism — `audit_log`, the outbox
envelope, the relay, dedupe — is detailed in
[write-backbone.md](write-backbone.md).

## Tenancy as structure

Every tenant table carries `ENABLE`+`FORCE` row-level security with
deny-on-unset policies, reachable only through the one
workspace-transaction helper; every tenant-local foreign key is
composite `(workspace_id, col)`, so a cross-workspace reference is
rejected by the database itself. Both invariants are fitness functions
derived from the live schema.

## One governed agent surface

The 🟢/🟡 autonomy tier of an action is declared once in the contract
(`x-mcp-tool`) and enforced **below the transport**: an agent mutation
over MCP or REST resolves the same tier, stages the same approval when
🟡, and default-denies any mutating operation carrying no tier.
Approving is human-only, and an agent never exceeds the granting
human's live RBAC.
