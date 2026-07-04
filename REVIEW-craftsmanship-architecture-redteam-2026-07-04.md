# Red-Team Review: `fable-poc` Craftsmanship, Go Architecture, and Data Design

**Date:** 2026-07-04  
**Reviewer stance:** senior engineering red-team, focused on craftsmanship, Go-standard architecture, clean code, database layout, and development principles.  
**Scope:** local `fable-poc` tree only. This is a half-finished implementation PoC, not the finished Margince product.

## Executive Verdict

This is a serious PoC, not a toy. The project already has many of the habits I would expect from a strong Go team: small composition root, generated OpenAPI server interface, explicit 501 stubs for unfinished contract surface, RLS enforced with negative integration tests, atomic audit/outbox writes in the core stores, server timeouts, panic recovery, architecture linting, and a surprisingly careful MCP approval loop.

The bad news is also serious: the strongest claims are not uniformly true across every surface. The MCP path has a green/yellow admission gate, but the REST bearer-token path lets agent passports call mutating endpoints with only `write` scope. The approval inbox protects the final decision, but not the read/triage surface. The schema has good RLS, but several workspace-scoped foreign keys do not enforce same-workspace ownership at the database boundary. The read-seat/full-seat model is present in the schema and error taxonomy but not actually enforced.

My short version: **the code is clean enough that the remaining defects are mostly architectural, not stylistic. That is good news, but the top defects are authorization/data-integrity defects and should be fixed before expanding feature surface.**

## What I Verified

- `make check` passed after allowing the architecture linter to download:
  - `go build ./...`
  - `go vet ./...`
  - `golangci-lint run ./...`
  - `go-arch-lint check`
  - `go test ./...`
  - `go generate ./...` plus generated-code drift check
- `make db-up`, `make migrate`, and `make test-integration` passed against local Postgres 16 and Redis 7.
- Integration coverage includes RLS deny-on-unset, cross-tenant RLS gates, migration up/down/reapply, bus relay/subscriber/dedupe behavior, HTTP e2e, MCP/passport flows, version skew, audit immutability, RBAC, promotion, and lifecycle invariants.

## What Is Genuinely Well Done

### Contract-first HTTP surface

`internal/httpapi.Server` embeds real module handlers over generated stubs and satisfies `crmcontracts.ServerInterface` at compile time. Unimplemented contract operations return explicit 501s instead of falling through to 404s. That is the right contract-first shape for a partial build.

Evidence: `internal/httpapi/server.go`, `internal/httpapi/stubs_gen.go`, `crm-contracts/api_gen.go`.

### Database isolation is not hand-waved

RLS is enabled and forced for tenant tables, `pg.WithWorkspaceTx` binds `app.workspace_id` with transaction-local `set_config`, and the integration tests derive the RLS coverage obligation from the schema instead of a static test list.

Evidence: `internal/pg/pg.go`, `migrations/core/0014_rls.up.sql`, `migrations/schema_integration_test.go`.

### Core write shape is disciplined

The store layer generally commits domain row, audit row, and outbox row in one transaction. The audit/event helpers are plain and legible, not magical. That is exactly the kind of boring, reliable foundation a CRM needs.

Evidence: `crm-core/internal/store/store.go`, entity store files under `crm-core/internal/store/`.

### Go architecture is better than average for a PoC

The project uses one Go module, but it still has clear package boundaries: leaf seams, domain packages, `internal/` platform code, generated contract package, and a thin `cmd/crm` composition root. The architecture is backed by depguard, go-arch-lint, and a leaf-purity test. That is a good compromise for an application repo at this stage.

Evidence: `.golangci.yml`, `.go-arch-lint.yml`, `arch_test.go`, `cmd/crm/main.go`.

### Operational basics are not forgotten

The HTTP server has explicit read/write/idle/header timeouts and graceful shutdown. Panic recovery writes problem+json. The pg pool has explicit limits. These are common omissions in early Go services; they are present here.

Evidence: `cmd/crm/serve.go`, `internal/httpapi/server.go`, `internal/pg/pg.go`.

### The MCP approval loop is thoughtfully designed

The registry gates before `Handle`, strips `approval_id` out before hashing, stages yellow actions with a target version, binds redemption to tool + diff hash + passport + target version, and consumes approvals once. This is a strong design direction.

Evidence: `crm-agents/registry.go`, `crm-agents/approvals.go`, `crm-approvals/service.go`.

## What Is Good, But Not Yet Durable Enough

- The top-level package layout is readable, but many boundaries are still social contracts. The arch linter helps, yet the repo remains one module and most packages are importable by any code not blocked by config.
- Store code is readable, but similar list/update/pagination patterns are repeated across entity files. The duplication is currently controlled, but every new object will increase drift pressure.
- The generated contract types are used heavily, which is good. Some seams still use `any`/`json.RawMessage` because they are real boundary seams; that is acceptable, but output schemas are mostly documentary rather than enforced.
- The migration discipline is good. The schema is still young enough that cross-workspace FK shape should be corrected now, before there is data to migrate painfully.
- The embedded no-build-chain SPA is appropriate for a PoC. It should not become the long-term frontend architecture if the UI grows much beyond the current surfaces.

## Genuinely Bad / Must Fix

### C1. Agent passport REST calls bypass the green/yellow autonomy gate

**Severity:** Critical  
**Area:** Auth, REST, agent governance

The MCP path correctly routes tool calls through `gate.Admit`, where yellow actions return `ErrRequiresApproval`. The REST bearer-token path does not do that. A passport bearer token is authenticated as `PrincipalAgent`, checked only for coarse `read` vs `write` scope, then passed directly to the regular REST handler.

Evidence:

- `crm-auth/handlers.go:268-282` authenticates a passport bearer token and calls the next REST handler.
- `crm-auth/handlers.go:324-334` maps every non-GET/HEAD request to `write`.
- `internal/gate/gate.go:29-64` is the actual green/yellow gate, but it is only invoked by `crm-agents.Registry.Invoke`.
- `crm-core/handlers_deal.go:101-120` advances a deal directly through the store.

Impact:

An agent with a `write` passport can call REST `POST /v1/deals/{id}/advance` and close a deal as won/lost without the yellow approval flow. The same applies to REST archive/update/promote-style operations unless separately blocked. This breaks the central autonomy invariant.

Recommendation:

Either do not allow passport bearer tokens on REST mutating endpoints, or add an agent REST admission layer that maps operationId + body to the same `mcp.ToolSpec`/tier resolver/staging flow used by MCP. There should be one agent governance choke point for both transports.

### C2. Read/full seat ceiling is modeled but not enforced

**Severity:** Critical  
**Area:** Auth, billing/security boundary

The database has `app_user.seat_type`, the error registry has `ErrSeatTierInsufficient`, and the comments say read seats are a hard capability ceiling. But `crmctx.Principal` does not carry `SeatType`, the middleware drops `Identity.SeatType`, and neither REST nor MCP checks seat type before mutations.

Evidence:

- `migrations/core/0002_identity.up.sql:27-29` defines `seat_type` as a hard capability ceiling.
- `crm-auth/service.go:228-252` and `crm-auth/service.go:297-305` load `seat_type`.
- `crmctx/crmctx.go:58-70` has no seat field on `Principal`.
- `crm-auth/passport.go:171-181` builds an agent principal without seat information.
- `kernel/errs/errs.go` defines `ErrSeatTierInsufficient`, but `rg` shows no enforcement call sites.

Impact:

A `read` seat with a permissive role, or an agent acting for that user, can still mutate if permissions/scopes allow it. That undercuts pricing, licensing, and the "read seat is safe to distribute broadly" product promise.

Recommendation:

Add `SeatType` to `crmctx.Principal`, bind it for human and agent principals, and enforce the ceiling before RBAC: read seats may read only, full seats may mutate. Add REST and MCP integration tests proving a read-seat human and read-seat agent cannot create/update/archive/approve/export.

### C3. Approval inbox leaks proposed changes to any human in the workspace

**Severity:** High  
**Area:** Approvals, RBAC, information disclosure

Approval list/get only checks `humanOnly`. It does not check whether the human can read the target row, has the role needed to approve the action, or is the relevant owner/manager. The wire mapper includes `proposed_change`, target id, summary, proposer, and diff hash.

Evidence:

- `crm-approvals/service.go:136-166` lists approvals after only `humanOnly`.
- `crm-approvals/service.go:169-187` gets an approval after only `humanOnly`.
- `crm-approvals/service.go:206-224` checks decision grants only when approving.
- `crm-approvals/handlers.go:112-144` exposes the proposed change in the response.

Impact:

A read-only user, or any low-privilege human in the workspace, can inspect pending yellow actions for records outside their normal row scope. Approval is protected; triage/read is not.

Recommendation:

Filter list/get by target visibility and/or an explicit approval-inbox permission. At minimum, `Get` should return 404 unless the viewer can read the target row or approve the target action. `List` should not become a workspace-wide side channel.

### C4. Workspace-scoped foreign keys do not enforce same-workspace references

**Severity:** High  
**Area:** Database layout, tenancy integrity

Many tables carry `workspace_id`, but foreign keys point only to the referenced row id, not `(workspace_id, id)`. Examples include `person.owner_id -> app_user(id)`, `deal.owner_id -> app_user(id)`, `team_membership.team_id -> team(id)`, and `role_assignment.role_id/user_id/team_id`.

Evidence:

- `migrations/core/0004_people.up.sql:9-14`
- `migrations/core/0006_deals.up.sql:60-64`
- `migrations/core/0002_identity.up.sql:54-88`
- Store writes assign owner ids directly, e.g. `crm-core/internal/store/person.go:100-102`, `crm-core/internal/store/person.go:281-283`, `crm-core/internal/store/deal.go:245-247`.

Impact:

RLS protects row visibility, but it does not by itself prove that a foreign key target belongs to the same workspace. A bad app path, import job, custom extension, or guessed UUID can create cross-tenant ownership/assignment references that pass normal FK checks. That is a data-integrity hole and can later confuse row-scope predicates.

Recommendation:

For every tenant-local reference, enforce composite FKs: add unique `(workspace_id, id)` constraints on referenced tables, then reference `(workspace_id, owner_id)`, `(workspace_id, team_id)`, etc. Add a schema fitness test that detects workspace tables with FK columns pointing at tenant tables without including `workspace_id`.

### C5. Workspace bootstrap is not atomic across auth and core defaults

**Severity:** High  
**Area:** Transaction boundaries, module composition

`Service.Bootstrap` creates workspace, admin, roles, session, and audit in one transaction. Then the HTTP handler calls `onBootstrap` afterward to seed core defaults. If default seeding fails, the handler returns an error, but the tenant and admin rows already exist.

Evidence:

- `crm-auth/service.go:91-153` commits the auth/bootstrap transaction.
- `crm-auth/handlers.go:96-107` seeds defaults afterward and explicitly notes "The tenant exists but its defaults did not land".
- Cookie is set only after seeding (`crm-auth/handlers.go:110`), so the client receives an error while persistent partial state remains.

Impact:

A transient failure can leave a workspace that exists, can collide on slug for retry, and may be missing required default pipeline/stages. That is exactly the kind of partial provisioning state that becomes painful in support.

Recommendation:

Move bootstrap orchestration to a composition-level transaction that includes auth rows and core seed rows, or make bootstrap a resumable provisioning state machine with idempotent retry. For a CRM, I prefer the atomic transaction until provisioning expands beyond one database.

### H1. The half-finished merge store is complex, unwired, and untested

**Severity:** High while in flight  
**Area:** Craftsmanship, delivery hygiene

The status file says merge is "half done": store layer written, but no HTTP handlers, no SoR verb, no MCP tool, no integration tests, no decision record. The code is complex: relinking person/org relationships, domains, consent, activity links, hierarchy, partner extension, audit, and events.

Evidence:

- `STATUS.md:12-38` states the merge layer compiles but is unwired and untested.
- `crm-core/internal/store/merge.go` is 531 lines of cross-table mutation logic.
- Contract operations still answer 501 via `internal/httpapi/stubs_gen.go`.

Impact:

For small functions, "compiled but unwired" is harmless. For merge logic, it is dangerous because correctness depends on referential integrity, collision cases, consent semantics, and audit/event shape. This is the one place I would not allow code to sit long without tests.

Recommendation:

Finish the slice immediately or keep it behind a build branch. The minimum bar is integration tests for every collision case, consent restriction, hierarchy reparenting, no remaining source references, 409/422 error mapping, and the full MCP yellow approval loop.

## Medium Findings / Improvements

### M1. The gate talks about quota, but quota is not implemented

Several comments describe scope/tier/quota, but `internal/gate.Admit` only checks scope and tier. This is not a bug if quota is not in scope yet, but comments should not claim it exists.

Recommendation: either add quota/budget to `Admit`, or remove quota language from comments until the budget layer lands.

### M2. JSON schema on MCP tools is documentation, not enforcement

Tool handlers decode strict Go structs, which is good. But `InputSchema` and `OutputSchema` are not actually validated generically. This means required fields embedded only in JSON schema are not guaranteed unless the handler struct and validation cover them.

Recommendation: either validate `InputSchema` centrally in the registry, or treat schemas as client-facing documentation and keep all validation in typed handler decode functions with tests.

### M3. Store list/update patterns are duplicated across entities

The entity store files are readable, but list filtering, cursor handling, `rows.Err`, patch application, child attachment, archive cleanup, and error mapping repeat by hand. It is controlled now, but drift will grow with every entity.

Recommendation: extract only the repeated mechanical bits: keyset cursor builder, where builder, and same-workspace FK visibility helpers. Do not abstract domain writes into a generic CRUD engine.

### M4. Some constraints are app-level when they should be schema-level

The schema does well on RLS and check constraints, but same-workspace relationships and owner visibility are too dependent on application code. The more this project leans into source customization, the more the database should reject impossible tenant-local references by construction.

Recommendation: prefer composite FKs and small trigger/check helpers for invariants that must survive custom code.

### M5. Dependency freshness and toolchain choice are very forward-leaning

`go.mod` pins `go 1.26.0` / `toolchain go1.26.4`. That is fine in this local environment, but it narrows contributor/operator compatibility. It is a deliberate choice, not a correctness bug.

Recommendation: document that Go 1.26 is intentional, or lower to the oldest Go version that supports the APIs used here if contributor portability matters.

## Craftsmanship Assessment

The code mostly reads as senior-authored Go:

- Comments generally explain invariants and trade-offs, not syntax.
- Package responsibilities are clear.
- Error handling is explicit and mostly sentinel-aware.
- Integration tests pin real security properties instead of just happy paths.
- Generated code is isolated.
- The web UI is intentionally small and boring, which is correct for this PoC.

The main craftsmanship smell is not "AI slop"; it is **strong comments getting ahead of enforcement**. Examples: "ONE admission point" while REST bearer mutates outside the gate, "hard capability ceiling" while seat type is not in `Principal`, "human triage" while approval visibility is not permission-filtered. The source is clean, but several comments currently describe the architecture the project wants, not the architecture all paths actually enforce.

## Database Layout Assessment

Strong:

- UUIDv7 id strategy is consistent.
- Money uses minor units and currency, not floats.
- RLS is forced and tested.
- Audit log is append-only and tested.
- Event outbox is transactional and relayed at least once.
- Core/custom migration namespaces exist.
- Version bump and optimistic concurrency are modeled.

Weak:

- Same-workspace FKs need composite enforcement.
- Some global/infra tables rely on envelope or app discipline instead of DB-visible tenant columns; that is acceptable for `event_outbox`, but should stay exceptional.
- The schema has forward-looking columns (`seat_type`, consent/retention, record_grant) whose enforcement is partial or absent in current code.
- Merge logic touches many tables but has no integration proof yet.

## Go Architecture Assessment

Strong:

- One binary with a thin composition root is the right Go shape here.
- `internal/` is used for platform and store internals where it matters.
- Domain packages are separated enough for the current size.
- Generated contract server interface prevents silent route drift.
- Architecture lints are part of `make check` and passed.

Weak:

- The "module" language in docs overstates reality: these are packages in one Go module, not independently versioned Go modules.
- The agent governance boundary is package-level, not transport-level, which is why REST passport calls escape it.
- The project will need a stronger policy for where cross-cutting features live before adding search, capture, Surface B, hosted MCP, and GDPR flows.

## Remediation Order

1. Close the REST passport autonomy bypass: passport bearer mutating REST calls must enter the same green/yellow gate or be disallowed.
2. Enforce read/full seat ceilings in `Principal`, REST, MCP, and tests.
3. Permission-filter approval list/get; do not let the inbox leak proposed changes workspace-wide.
4. Add composite same-workspace foreign keys and a schema fitness test for tenant-local FK shape.
5. Make workspace bootstrap atomic or resumable/idempotent.
6. Finish or quarantine merge; do not leave complex unwired mutation code untested.
7. Clean comment drift around quota, "one gate", and capability ceilings.
8. Extract minimal store helpers only where duplication is already causing drift.

## Final Call

This PoC is in materially better shape than most half-finished CRM backends. The build gates are real, the integration lane is meaningful, and the codebase has a coherent taste. I would not call it production-ready, and I would not expand feature scope until the authorization and same-workspace data-integrity findings are fixed.

The project has the right bones. The next step is to make the invariants impossible to bypass, not merely well documented.
