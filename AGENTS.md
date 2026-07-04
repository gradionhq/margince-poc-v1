# AGENTS.md — operating this repo

Margince CRM implementation PoC (WP0 foundation + WP1 core spine), built
from the spec in `../margince/specs/` (contract-first, P3: when this code
and the spec disagree, the spec wins).

**Start at [STATUS.md](STATUS.md)** — progress, in-flight work, and the
session-pickup point; update it at the end of every working session.
Route findings as you work: implementation decisions →
decisions/; spec/ticket defects → a numbered file in
`feedback/` + a row in its README table.

## Build / test / seed

All Go code lives under `backend/` (one Go module,
`github.com/gradionhq/margince/backend`); the root Makefile delegates there.

```
make db-up              # start PG16 + Redis 7 containers, create the app role
make migrate            # apply core + custom migrations (owner DSN)
make check              # the merge gate: build, vet, lint, arch-lint, unit tests, contract drift
make test-integration   # real-Postgres lane: RLS gates + HTTP end-to-end (needs db-up)
make dev                # db-up + migrate + api on :8080
```

Four process-role binaries (decisions/0011), all wired through
`internal/compose`: `cmd/api` (HTTP; inline outbox relay behind
`--inline-relay`, default true), `cmd/worker` (standalone relay),
`cmd/migrate` (up|down), `cmd/mcp` (the A1 stdio server).

MCP (Surface A1): mint a passport (`POST /v1/passports`, session-authed),
then `MARGINCE_PASSPORT_TOKEN=mgp_… mcp --workspace <slug> --dsn …`
serves the tool surface over stdio. The same token is a REST Bearer
credential (read-only on REST — C1). Every call re-authenticates:
revocation binds mid-session.

Host requirements: Go ≥ 1.26, Docker, `golangci-lint`, and python3
(`make gen`/`make drift` run `tools/gen-stubs` through it).

Local API calls need the workspace header (prod uses the subdomain):
`curl -k https://localhost:8080/v1/me -H 'X-Workspace-Slug: <slug>' --cookie 'crm_session=…'`

## Layout (spec ADR-0054/A69 as amended; see decisions/0011)

The `backend/internal/{modules,platform,shared}` triad — the DAG is
`shared → platform → modules → compose → cmd`, enforced three ways
(depguard, go-arch-lint, `backend/arch_test.go` fitness tests):

- `internal/shared/` — Tier-0 leaves, stdlib-only (test-enforced):
  `kernel/{ids,events,provenance,principal}`, `apperrors` (the fixed
  sentinel registry — extend only with the spec's interfaces.md §0), and
  `ports/{datasource,mcp,connector,workflow,model,retrieval,jurisdiction}`
  (the frozen seam interfaces + additive provider mechanics).
- `internal/platform/` — technical plumbing, owns no domain:
  `database` (pg pool + the RLS `WithWorkspaceTx` GUC contract) +
  `database/storekit` (the ONE spelling of the audit+outbox write shape,
  keyset cursors, version patches), `auth` (the ONE admission point:
  `Admit` (scope ∧ tier) + object RBAC + row-scope clauses incl. the
  activity link-walk), `events` (outbox relay/subscriber/dedupe),
  `dbmigrate`, `httperr` (RFC 7807 + wire helpers), `httpserver` (chassis).
- `internal/modules/` — bounded capabilities, flat per ADR-0054 §3
  (store + mapping + transport + provider in one package); a module NEVER
  imports a sibling: `identity` (sessions, passports, RBAC policy docs —
  ONLY in `identity/internal/policy`, decisions/0006), `people` (person,
  organization, lead + merge + promote — cross-aggregate single-tx SQL
  ownership per decisions/0011), `deals` (deal, pipeline, workspace
  seed), `activities` (timeline), `approvals` (the 🟡 confirm-first
  engine, ADR-0036: staged rows ARE the authority object, decisions/0008),
  `agents` (the governed MCP tool surface: registry + tools + stdio).
- `internal/compose/` — the composition layer every process role shares:
  the contract HTTP surface (module handlers shadow generated 501 stubs),
  the composite `datasource.SystemOfRecordProvider`, the MCP registry +
  approvals adapter, and the cross-module integration suites. Every
  cross-module edge is injected HERE (identity's workspace seed ←
  deals; agents' staging ← approvals).
- `internal/contracts/` — GENERATED from `backend/api/crm.yaml`. Never edit.
- `backend/api/crm.yaml` — the authoritative OpenAPI 3.1 contract.
- `backend/web/` — the embedded SPA (static, no build chain); served at
  `/`, talks only to `/v1`. `backend/migrations/core|custom/` — the
  ADR-0017 namespaces. `modules/<name>/custom/` + `migrations/custom/` —
  the fork-owned seam: upstream never writes there (ADR-0054 §7).

## DO NOT TOUCH

- `internal/contracts/api_gen.go`, `internal/compose/stubs_gen.go` —
  generated (`make gen`); the drift gate fails a hand edit.
- `migrations/core/*` that have shipped — additive migrations only.
- RLS policies and the `database.WithWorkspaceTx` GUC contract — every
  tenant query goes through it; there is no raw-pool path for tenant data.
- `internal/shared/apperrors` — the fixed sentinel registry; extend only
  together with the spec's interfaces.md §0.

## The write shape (non-negotiable)

Every mutation commits domain row + `audit_log` row + `event_outbox` row
in ONE transaction — spelled once in `platform/database/storekit`
(`Audit` + `Emit`), called by every module store. `captured_by` is
stamped from the authenticated principal, never from the request body.
The outbox envelope is the `shared/kernel/events` contract (events.md
§2): the HTTP layer mints one `correlation_id` per request, `Audit()`
returns the audit row id, `Emit()` links both into the trace —
publishing is ALWAYS through the outbox (`platform/events.Relay` ships
it; no direct XADD from domain code) and consumers wrap handlers in
`events.Dedupe` because the bus is at-least-once. Every store entry
point is RBAC-gated (`auth.Require` + `auth.EnsureVisible` + the list
scope clauses in `platform/auth`): object denial →
`apperrors.ErrPermissionDenied` (403), row-scope miss →
`apperrors.ErrNotFound` (404, existence-hiding).

## Craftsmanship

Match architecture/15: comments say *why*, domain names not `data/tmp`,
no `any` escapes, no dead code, no speculative abstraction; handle the
honest hard cases (empty page, version skew, cross-tenant, GUC-unset).
Tests read as specs and the integration lane fails loudly without a
database — a skipped security gate looks exactly like a passing one.

## Rules learned from the review loop (binding)

Full rationale in [README.md](README.md#engineering-rules-learned-from-the-review-loop);
the short form:

1. **Fix the invariant, not the call site** — grep every mutation/read
   site of the same column/constraint/record and fix them as one change
   (the recurring reviewer catch here was "fixed the case under review,
   missed the sibling copy").
2. **Prefer fitness functions over point fixes** — derive the obligation
   from the system (e.g. every `workspace_id` table must have FORCE RLS;
   every CHECK violation maps to a 4xx; `backend/arch_test.go` derives
   its package lists from the tree), don't maintain it as a list.
3. **Anything that returns a record is a read** and carries the row-scope
   gate — including replay, conflict, and error paths.
4. **No build-process residue in comments** — no review-ticket numbers or
   fix narration; state the invariant so it stands alone. History belongs
   to git and `feedback/`, not the source. Same for test names.
5. **Never rationalize a known gap in a comment** — restructure it away
   or gate it with a test.
