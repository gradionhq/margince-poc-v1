# CLAUDE.md — operating this repo

This file provides guidance to Claude Code (claude.ai/code) when working in this
repository. It mirrors [AGENTS.md](AGENTS.md); keep the two in sync when either changes.

Margince CRM implementation PoC (WP0 foundation + WP1 core spine). This is the
**build repo** — the running Go software. The *specification* lives in a separate
repo (see below); this code is built **from** that spec, contract-first.

## Where the spec is (read before building)

The normative spec is the sibling repo, at **`../margince/margince specs/`**
(absolute: `/Users/lars/develop/margince/margince specs/`) — note the space in the
directory name, so quote paths in shell commands.

- **`../margince/margince specs/spec/README.md`** — live status + reading order; the
  "Continue here" block is the canonical spec-side pickup point.
- **`../margince/margince specs/spec/contract/`** — implementation source-of-truth:
  `crm.yaml` (OpenAPI 3.1), `data-model.md`, `events.md`, `interfaces.md` (incl. the
  §0 error-sentinel registry), `ai-operational-spec.md`, `formulas-and-rules.md`.
- **`../margince/margince specs/spec/architecture/`** — the build blueprint (`00`–`13`);
  `11-conventions.md` is the style guide.
- **`../margince/margince specs/spec/product/build-backlog/`** — the 701-leaf V1 ticket
  breakdown this repo is working through.
- **`../margince/margince specs/spec/decisions/`** — `DECISIONS.md` (locked) + `ADR-*.md`.

**Contract-first (principle P3): when this code and the spec disagree, the spec wins.**
Product name **Margince** is locked; older docs say "Gradion CRM" — same product.

**Start at [STATUS.md](STATUS.md)** — progress, in-flight work, and the session-pickup
point; update it at the end of every working session. Route findings as you work:
implementation decisions → [decisions/](decisions/); spec/ticket defects → a numbered
file in [`feedback/`](feedback/) + a row in its [README table](feedback/README.md).

## Build / test / seed

```
make db-up              # start PG16 + Redis 7 containers, create the app role
make migrate            # apply core + custom migrations (owner DSN)
make check              # the merge gate: build, vet, lint, unit tests, contract drift
make test-integration   # real-Postgres lane: RLS gates + HTTP end-to-end (needs db-up)
make dev                # db-up + migrate + serve on :8080
```

MCP (Surface A1): mint a passport (`POST /v1/passports`, session-authed), then
`MARGINCE_PASSPORT_TOKEN=mgp_… crm mcp --workspace <slug> --dsn …` serves the tool
surface over stdio. The same token is a REST Bearer credential. Every call
re-authenticates: revocation binds mid-session.

Host requirements: Go ≥ 1.22, Docker, `golangci-lint`, and python3 (`make gen`/`make
drift` run `tools/gen-stubs` through it).

Local API calls need the workspace header (prod uses the subdomain):
`curl -k https://localhost:8080/v1/me -H 'X-Workspace-Slug: <slug>' --cookie 'crm_session=…'`

## Layout (ADR-0016; see decisions/0001)

- `kernel/` + top-level seam packages (`crmctx sor mcp connector workflow model
  retrieval jurisdiction`) — Tier 0, stdlib-only (test-enforced).
- `crm-core/`, `crm-auth/`, … — domain modules; guts under each module's `internal/`;
  public surface = transport `Handlers` + service types. `crm-core` also exports the
  SoR-mode `Provider` (the `sor` seam).
- `crm-agents/` — the governed MCP tool surface: registry + tools + the A1 stdio server.
  Depends ONLY on seams + platform, never on sibling modules (the composition root
  injects the provider and the approvals adapter).
- `crm-approvals/` — the 🟡 confirm-first engine (ADR-0036): staged approvals, the
  /approvals inbox, redemption (single-use, content-hash + passport + target-version
  bound). The staged row IS the authority object; no bearer secret travels
  (decisions/0008).
- `crm-contracts/` — GENERATED from `contracts/crm.yaml`. Never edit.
- `internal/` — the platform layer the composition root owns: pg pool + RLS tx helper,
  migration runner, RFC 7807 mapper, contract assembly, and `internal/gate` — the ONE
  admission point (scope ∧ tier) every governed tool call passes; nothing else may mint
  an admitted call.
- `cmd/crm` — thin composition root (migrate | serve | mcp).
- `web/` — the embedded SPA (static, no build chain); served at `/`, talks only to `/v1`.
- `migrations/core|custom/` — the ADR-0017 namespaces.

## Seams implemented

`sor`, `mcp`, `connector`, `workflow`, `model`, `retrieval`, `jurisdiction` are defined
(interfaces.md shapes). `crm-core/provider.go` implements `sor.SystemOfRecordProvider`
(SoR-mode subset: Read/Search/Create/Update/AdvanceDeal; report + schema introspection
error loudly), and `crm-agents` implements the `mcp` registry + the 🟢 tool set over it.

## DO NOT TOUCH

- `crm-contracts/api_gen.go`, `internal/httpapi/stubs_gen.go` — generated (`make gen`);
  the drift gate fails a hand edit.
- `migrations/core/*` that have shipped — additive migrations only.
- RLS policies and the `pg.WithWorkspaceTx` GUC contract — every tenant query goes
  through it; there is no raw-pool path for tenant data.
- `kernel/errs` — the fixed sentinel registry; extend only together with the spec's
  interfaces.md §0.

## The write shape (non-negotiable)

Every mutation commits domain row + `audit_log` row + `event_outbox` row in ONE
transaction (see `crm-core/internal/store`). `captured_by` is stamped from the
authenticated principal, never from the request body. The outbox envelope is the
`kernel/events` contract (events.md §2): the HTTP layer mints one `correlation_id` per
request, `audit()` returns the audit row id, `emit()` links both into the trace —
publishing is ALWAYS through the outbox (`internal/bus.Relay` ships it; no direct XADD
from domain code) and consumers wrap handlers in `bus.Dedupe` because the bus is
at-least-once. Every store entry point is RBAC-gated (`require` + `ensureVisible` + the
list `scopeClause` in `crm-core/internal/store/authz.go`): object denial →
`errs.ErrPermissionDenied` (403), row-scope miss → `errs.ErrNotFound` (404,
existence-hiding). Role policy documents live ONLY in `crm-auth/internal/policy`
(decisions/0006).

## Craftsmanship

Match architecture/15: comments say *why*, domain names not `data/tmp`, no `any`
escapes, no dead code, no speculative abstraction; handle the honest hard cases (empty
page, version skew, cross-tenant, GUC-unset). Tests read as specs and the integration
lane fails loudly without a database — a skipped security gate looks exactly like a
passing one.

## Rules learned from the review loop (binding)

Full rationale in [README.md](README.md#engineering-rules-learned-from-the-review-loop);
the short form:

1. **Fix the invariant, not the call site** — grep every mutation/read site of the same
   column/constraint/record and fix them as one change (the recurring reviewer catch
   here was "fixed the case under review, missed the sibling copy").
2. **Prefer fitness functions over point fixes** — derive the obligation from the system
   (e.g. every `workspace_id` table must have FORCE RLS; every CHECK violation maps to a
   4xx), don't maintain it as a list.
3. **Anything that returns a record is a read** and carries the row-scope gate —
   including replay, conflict, and error paths.
4. **No build-process residue in comments** — no review-ticket numbers or fix narration;
   state the invariant so it stands alone. History belongs to git and
   [`feedback/`](feedback/), not the source. Same for test names.
5. **Never rationalize a known gap in a comment** — restructure it away or gate it with
   a test.
