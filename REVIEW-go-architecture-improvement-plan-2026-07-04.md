# Go Architecture Improvement Plan: `fable-poc`

**Date:** 2026-07-04  
**Revision:** updated after reviewing the unfinished `factory/` architecture and after the founder direction that preserving the current PoC tree is less important than creating the best long-term open-source architecture.  
**Reviewer stance:** senior Go architecture review, focused on source quality, contributor onboarding, agent-safe parallel work, clean boundaries, database integrity, and long-term maintainability.  
**Scope:** local `fable-poc` tree plus the architecture concept under `factory/`.

## Executive Verdict

The original recommendation was conservative: keep the current `fable-poc` layout because it already works and the architecture is directionally sound.

With the new goal stated clearly — **make this open-source software something we can be proud of, and accept a rework while the PoC is still young** — the recommendation changes:

**Adopt the factory architecture as the target layout.**

That does not mean microservices. It does not mean splitting the backend into many Go modules. The best architecture is still a **contract-first modular monolith**. The change is that the source tree should be made cleaner, more teachable, and more scalable for contributors and AI agents.

The new target should be:

- **one backend Go module**
- **one modular-monolith backend**
- **multiple command entrypoints for process roles**
- **bounded modules under `backend/internal/modules`**
- **technical plumbing under `backend/internal/platform`**
- **ports, kernel types, and application errors under `backend/internal/shared`**
- **contract-first API under `backend/api/crm.yaml`**
- **migrations under `backend/migrations`**
- **frontend under `frontend/` when the real frontend starts**
- **jurisdiction packs as separate Go modules later, only when they become real**

The current PoC is not bad. It is serious work. But if the ambition is best-in-class open-source architecture, the factory layout is cleaner than the current top-level `crm-*` layout.

## The Important Clarification

The architecture question is not:

> one binary vs many packages

The better question is:

> what source layout makes a modular monolith easiest to understand, extend, test, and govern?

The answer is:

```text
backend/
  cmd/
    api/
    worker/
    migrate/
    mcp/                 # optional, if local MCP remains its own process role
  internal/
    modules/
      identity/
      people/
      deals/
      activities/
      approvals/
      agents/
      search/
      ai/
      capture/
      gdpr/
    platform/
      config/
      database/
      httpserver/
      events/
      auth/
      logger/
      observability/
      blobstore/
      keyvault/
    shared/
      kernel/
      apperrors/
      ports/
        datasource/
        model/
        connector/
        mcp/
        retrieval/
        jurisdiction/
  api/
    crm.yaml
  migrations/

frontend/
  src/
    app/
    features/
    shared/
    lib/api-client/

jurisdictions/
  de/                    # future separate Go module, not now
```

This is the factory architecture, with one adjustment: do not add every listed module on day one. Add module directories when there is real code to own.

## What I Verified

I reviewed:

- the current `fable-poc` package graph
- command composition under `cmd/crm`
- architecture lint and depguard configuration
- generated OpenAPI contract setup
- HTTP assembly
- auth/gate behavior
- database migrations and RLS tests
- the factory architecture docs under `factory/docs-drafts/architecture`
- the repo-layout conflict note under `fable feedback/01-repo-layout-conflict-adr0016-vs-factory-target.md`
- `fable-poc/decisions/0001-layout-and-module-path.md`

Validation results from the current PoC:

- `make check` passed:
  - `go build ./...`
  - `go vet ./...`
  - `golangci-lint run ./...`
  - `go-arch-lint check`
  - `go test ./...`
  - `go generate ./...`
  - generated-code drift check
- `make test-integration` passed after running with the required local service permissions.

Important context: several earlier critical red-team findings have already been fixed in the current code. The tree now has tests for passport REST mutation restriction, read-seat mutation restriction, composite same-workspace foreign-key enforcement, and merge store behavior.

## Current Architecture Assessment

The current architecture is better than an average PoC:

- one Go module
- thin composition root
- contract-first HTTP
- explicit generated stubs
- RLS-aware database helper
- audit/outbox discipline in stores
- architecture linting
- semantic integration tests
- mostly clean domain package boundaries

The problem is not that the current code is sloppy. The problem is that the **layout is not the best long-term shape** if the product is meant to be open-source, agent-built, and contributor-friendly.

The current layout:

```text
cmd/crm/
internal/
crm-core/
crm-auth/
crm-approvals/
crm-agents/
crm-ai/
crm-capture/
crm-search/
crm-contracts/
crmctx/
sor/
mcp/
connector/
workflow/
retrieval/
model/
jurisdiction/
kernel/
migrations/
contracts/
web/
```

This is workable, but it has avoidable weaknesses:

- top-level `crm-*` packages make the root noisy
- `crm-core` risks becoming a catch-all
- seam packages at the root are easy to mistake for implementations
- `internal/` mixes several platform concerns without an explicit platform vocabulary
- "module" is used ambiguously even though there is only one Go module
- the current tree is less predictable for new contributors than `modules/<name>/{domain,app,adapters,transport}`
- the factory backlog and docs already point toward a different target layout

Because the PoC is still young, these are worth fixing now.

## Direct Answer: One Binary or Packages/Modules?

For the best open-source architecture:

- **Do not use microservices.**
- **Do not split the backend into many Go modules.**
- **Do use many packages.**
- **Do use clear bounded modules inside one backend Go module.**
- **Do allow multiple command entrypoints for process roles.**

The older "one binary" answer was correct for preserving PoC simplicity, but it should not become dogma.

The better rule is:

> one modular-monolith backend, one backend Go module, clear process roles.

That can be shipped as separate binaries:

```text
backend/cmd/api
backend/cmd/worker
backend/cmd/migrate
backend/cmd/mcp
```

Or as one CLI binary with subcommands:

```text
margince api
margince worker
margince migrate
margince mcp
```

Both are acceptable. For open-source clarity, I recommend separate `cmd/<role>` directories in the source tree. Packaging can still provide an all-in-one Docker Compose profile for small self-hosted installs.

The important thing is that `api`, `worker`, `migrate`, and `mcp` are **process roles**, not separate services with separate domain ownership.

## Factory Architecture: What Should Be Adopted

The factory architecture has five ideas that are better than the current PoC layout.

### 1. Clear `platform`, `shared`, and `modules`

The factory split is excellent:

- `platform` = technical implementation plumbing
- `shared/kernel` = domain-neutral primitives
- `shared/apperrors` = sentinel error taxonomy
- `shared/ports` = dependency-light cross-module interfaces
- `modules/<name>` = bounded business capability

This is teachable. A new contributor can usually answer "where does this go?" without asking.

### 2. Standard Module Shape

For real modules, use:

```text
backend/internal/modules/<name>/
  domain/
  app/
  ports/
  adapters/
  transport/
  module.go
```

Meaning:

- `domain`: entities, invariants, pure rules
- `app`: use cases and orchestration
- `ports`: interfaces this module needs from the outside
- `adapters`: database repositories and external implementations
- `transport`: HTTP/MCP handlers
- `module.go`: module wiring/manifest

This should not be forced onto tiny leaf packages. But for `identity`, `people`, `deals`, `agents`, and `approvals`, it is the right shape.

### 3. Port-First Cross-Module Work

The rule should be:

> A module reaches another module's behavior only through a port under `shared/ports` or through a dependency injected by the composition root.

No direct sibling-module internals. No "just import the store." No hidden coupling.

This is especially important for AI-agent parallel work: two agents can agree a port, then implement each side independently.

### 4. Generated Manifests Later

Factory's generated route/import manifests are useful once many agents add tools, jobs, and endpoints concurrently.

Do not build a generator too early. But design the module manifests so generation can be introduced later without another layout migration.

### 5. Vocabulary Discipline

Use these terms consistently:

- **Go module:** a compilation/versioning unit with `go.mod`
- **module:** a bounded business capability under `backend/internal/modules/<name>`
- **package:** a Go package
- **port:** dependency-light cross-module interface
- **adapter:** implementation of a port or external system
- **platform:** technical infrastructure, no domain ownership

This matters. Bad vocabulary becomes bad architecture.

## Recommended Target Architecture

### Repository Shape

```text
backend/
  go.mod
  go.sum
  Makefile                 # optional; root Makefile may delegate here
  cmd/
    api/
    worker/
    migrate/
    mcp/
  internal/
    modules/
      identity/
        domain/
        app/
        ports/
        adapters/
        transport/
        module.go
      people/
        domain/
        app/
        ports/
        adapters/
        transport/
        module.go
      deals/
      activities/
      approvals/
      agents/
      search/
      ai/
      capture/
      gdpr/
    platform/
      config/
      database/
      httpserver/
      events/
      auth/
      logger/
      observability/
    shared/
      kernel/
      apperrors/
      ports/
        datasource/
        model/
        connector/
        mcp/
        retrieval/
        jurisdiction/
  api/
    crm.yaml
  migrations/

frontend/
  package.json
  src/
    app/
    features/
    shared/
    lib/api-client/

jurisdictions/
  de/
    go.mod                 # future only, when there is real DE code
```

### Dependency Rules

- `shared/*` imports only standard library and other safe shared packages.
- `platform/*` imports `shared/*`, but not `modules/*`.
- `modules/<name>/domain` imports only `shared/kernel` and `shared/apperrors`.
- `modules/<name>/app` imports its own `domain`, its own `ports`, and shared packages.
- `modules/<name>/adapters` implements repositories/external clients and may import platform database/events helpers.
- `modules/<name>/transport` maps HTTP/MCP to app use cases.
- one module must not import another module's internals.
- `cmd/*` may wire modules, platform, and enabled jurisdiction packs.
- generated contract code is imported by transport and mapping layers, not used as the domain model.

### Process Roles

Recommended command entrypoints:

```text
backend/cmd/api       # HTTP API, static assets if needed, hosted connector endpoints
backend/cmd/worker    # background jobs, outbox relay, async workflows
backend/cmd/migrate   # schema migration runner
backend/cmd/mcp       # local stdio MCP process, if kept separate from api
```

For very small installs, `api` may run the relay inline behind a config flag. That is an operational option, not a different architecture.

## Mapping From Current PoC to Target

| Current PoC | Target |
|---|---|
| `cmd/crm/serve.go` | `backend/cmd/api` |
| `cmd/crm/mcp.go` | `backend/cmd/mcp` or `backend/internal/modules/agents/transport` plus `cmd/mcp` |
| `cmd/crm/migrate` | `backend/cmd/migrate` |
| `internal/pg` | `backend/internal/platform/database` |
| `internal/bus` | `backend/internal/platform/events` |
| `internal/httpapi` | `backend/internal/platform/httpserver` plus module `transport` packages |
| `internal/httperr` | `backend/internal/platform/httpserver` or `backend/internal/shared/apperrors` mappings |
| `internal/gate` | `backend/internal/modules/agents/app` or `backend/internal/platform/auth`, depending on final ownership |
| `crm-auth` | `backend/internal/modules/identity` |
| `crm-core/internal/store` | split into module adapters: `people/adapters`, `deals/adapters`, etc. |
| `crm-core` people/org logic | `backend/internal/modules/people` |
| `crm-core` deal/pipeline logic | `backend/internal/modules/deals` |
| `crm-core` activity/timeline logic | `backend/internal/modules/activities` |
| `crm-approvals` | `backend/internal/modules/approvals` |
| `crm-agents` | `backend/internal/modules/agents` |
| `crm-search` | `backend/internal/modules/search` |
| `crm-ai` | `backend/internal/modules/ai` |
| `crm-capture` | `backend/internal/modules/capture` |
| `crm-contracts` | generated code from `backend/api/crm.yaml` |
| `contracts/crm.yaml` | `backend/api/crm.yaml` |
| `kernel/errs` | `backend/internal/shared/apperrors` |
| `kernel/ids`, `kernel/prov`, `crmctx` | `backend/internal/shared/kernel` |
| `sor` | `backend/internal/shared/ports/datasource` |
| `mcp` | `backend/internal/shared/ports/mcp` |
| `connector` | `backend/internal/shared/ports/connector` |
| `workflow` | `backend/internal/platform/events` or `shared/ports/workflow`, depending on whether it remains domain-neutral |
| `retrieval` | `backend/internal/shared/ports/retrieval` |
| `model` | `backend/internal/shared/ports/model` |
| `jurisdiction` | `backend/internal/shared/ports/jurisdiction` |
| `migrations` | `backend/migrations` |
| `web/static` | temporary; later `frontend/src/features` |

## What Is Already Good and Should Be Preserved

### Contract-First HTTP

Keep the OpenAPI contract as the source of truth. Keep generated server interfaces and generated DTOs. Keep drift checks.

Improve the layout, not the principle.

### RLS and Database-Enforced Tenancy

Keep RLS, transaction-local workspace binding, and schema-derived integration tests. The database must reject impossible tenant relationships.

The target layout should make the database helper a platform package, but the enforcement model should remain.

### Audit and Outbox Discipline

Core writes should continue to write domain row, audit row, and outbox row atomically.

In the target architecture, this should be easier to standardize through platform events/audit helpers and module adapters.

### Architecture Gates

Keep depguard, go-arch-lint, generated drift checks, schema tests, and semantic integration tests.

After the restructure, architecture lint becomes even more important because it can enforce the `shared/platform/modules/cmd` rules more cleanly.

### Recent Security Fixes

Preserve the recent fixes:

- agent passports cannot mutate over REST
- read-seat principals cannot mutate
- composite workspace FKs enforce tenant-local references
- approval redemption remains bound and single-use

These are architecture invariants, not just tests.

## What Must Improve

### `crm-core` Must Stop Being a Catch-All

For open-source clarity, avoid a permanent `core` module. It becomes the place everything goes when no one wants to decide ownership.

Split it conceptually into:

- `people`
- `deals`
- `activities`
- later `lists` or `segmentation`, if needed
- later `gdpr`, if consent/data-subject behavior deserves a focused module

Do not over-split tiny code. But do not keep all CRM domain behavior under one vague "core" label.

### Transport Should Move to Owning Modules

The current `internal/httpapi` composition layer is useful, but the actual handlers should live in the owning module's `transport` package.

The platform HTTP layer should own:

- router setup
- middleware
- panic recovery
- security headers
- problem+json mapping
- generated route registration

The module should own:

- endpoint handler method
- request-to-app mapping
- app error propagation

### Shared Ports Should Be Explicit

Top-level seam packages are okay in a small PoC. For the long-term architecture, ports should live together under:

```text
backend/internal/shared/ports/
```

This makes them visible as architecture seams, not random utility packages.

### Platform Should Be a First-Class Concept

Technical plumbing should be grouped under `backend/internal/platform`.

That includes:

- database/pool/transaction helper
- events/outbox/worker runtime
- HTTP chassis
- auth middleware
- config
- logging
- observability
- blob/key storage later

Platform owns no business capability.

### Command Entrypoints Should Be Boring

Each `cmd/<role>` package should only:

- read config
- create platform dependencies
- compose modules
- start the process role
- handle shutdown

No business logic should live in `cmd`.

## Database Layout Recommendations

The database direction is already sound. Preserve and strengthen it.

Rules:

1. Every tenant table carries `workspace_id`.
2. Every tenant-local foreign key includes `workspace_id`.
3. RLS is enabled and forced for tenant tables.
4. Workspace context is set only through the platform transaction helper.
5. Domain writes include audit/outbox where required.
6. Version columns and If-Match behavior remain consistent.
7. Integration tests derive schema obligations where possible.

Target migration ownership:

- migrations live centrally in `backend/migrations`
- module docs say which tables each module owns
- migration filenames should make module ownership obvious
- cross-module FKs require explicit design review

## Clean Code Recommendations

### Prefer Explicit SQL

Do not introduce a generic repository framework. Keep aggregate-specific SQL visible.

Use small helpers for repeated mechanics:

- keyset pagination
- optimistic version checks
- patch validation
- audit/outbox append
- RLS-bound transaction setup
- test fixtures

### Keep Domain and Transport Separate

Generated contract types are transport DTOs. They should not become the domain model.

Transport maps contract DTOs into app commands. App/domain code should be testable without HTTP.

### Avoid Empty Ceremony

The factory structure is good, but empty folders are not quality.

Create a module when there is real code. Create `domain/app/adapters/transport` where those responsibilities exist. Do not scaffold ten empty packages just to look architectural.

### Package Docs Matter

Every public package under `shared`, `platform`, and `modules` should have a short package comment stating:

- what it owns
- what it may import
- what must not go there

For open source, these comments are onboarding infrastructure.

## Required Decision Update

`fable-poc/decisions/0001-layout-and-module-path.md` currently records the opposite decision: keep top-level `crm-*` packages for this build.

If the project adopts this plan, that decision must be superseded or amended.

Recommended new decision:

> The active build target adopts the factory `backend/internal/{modules,platform,shared}` layout. The system remains a modular monolith with one backend Go module. The previous top-level `crm-*` PoC layout is treated as harvested implementation material, not the target architecture.

This should also reconcile the known conflict in:

- `fable feedback/01-repo-layout-conflict-adr0016-vs-factory-target.md`
- factory backlog `module_footprint` paths
- architecture lint rules
- generated contract paths
- README/AGENTS architecture guidance

## Rework Plan

### Phase 0: Decide and Freeze the Target

Before moving files:

1. Amend or supersede `decisions/0001-layout-and-module-path.md`.
2. Add a short architecture README for the new layout.
3. Define the allowed-import matrix for `shared`, `platform`, `modules`, and `cmd`.
4. Decide command shape: separate `cmd/api` etc. vs one `cmd/margince` multi-command binary.
5. Decide the initial module roster. Keep it small.

Recommended initial roster:

- `identity`
- `people`
- `deals`
- `activities`
- `approvals`
- `agents`

Keep `search`, `ai`, `capture`, `gdpr` as stubs only if they already have meaningful code or immediate tickets.

### Phase 1: Create the Target Skeleton

Create:

```text
backend/go.mod
backend/cmd/api
backend/cmd/migrate
backend/cmd/mcp
backend/internal/shared
backend/internal/platform
backend/internal/modules
backend/api
backend/migrations
```

Move the contract source to `backend/api/crm.yaml`.

Move migrations to `backend/migrations`.

Keep tests green after each small move.

### Phase 2: Move Shared Leaf Packages

Move dependency-light packages first:

- `kernel/errs` -> `shared/apperrors`
- `kernel/ids` -> `shared/kernel/ids`
- `kernel/prov` -> `shared/kernel/provenance`
- `crmctx` -> `shared/kernel/principal`
- `sor` -> `shared/ports/datasource`
- `mcp` -> `shared/ports/mcp`
- `connector` -> `shared/ports/connector`
- `retrieval` -> `shared/ports/retrieval`
- `model` -> `shared/ports/model`
- `jurisdiction` -> `shared/ports/jurisdiction`

Run architecture tests after this phase. Leaf purity matters.

### Phase 3: Move Platform Packages

Move:

- `internal/pg` -> `platform/database`
- `internal/pgmigrate` -> `platform/migrations` or keep migrate-specific under `cmd/migrate` if small
- `internal/bus` -> `platform/events`
- `internal/httperr` -> `platform/httpserver` mapping package or `shared/apperrors/http`
- HTTP router/middleware -> `platform/httpserver`

Keep platform free of module imports.

### Phase 4: Move Domain Modules

Move and split:

- `crm-auth` -> `modules/identity`
- `crm-approvals` -> `modules/approvals`
- `crm-agents` -> `modules/agents`
- `crm-core` people/org code -> `modules/people`
- `crm-core` deal/pipeline code -> `modules/deals`
- `crm-core` activity/timeline code -> `modules/activities`

Inside each module, use:

```text
domain/
app/
ports/
adapters/
transport/
module.go
```

Do not try to perfect every internal split in the same move. First get ownership and imports right. Then refine.

### Phase 5: Rebuild Composition and Gates

Update:

- command wiring
- generated stubs
- contract generation paths
- architecture lint
- depguard
- integration test package paths
- Makefile targets
- README/AGENTS instructions

Add architecture fitness tests:

- `shared` does not import `platform`, `modules`, or `cmd`
- `platform` does not import `modules` or `cmd`
- modules do not import sibling module internals
- domain packages do not import transport, adapters, platform database, or generated HTTP types
- transport packages do not contain business rules

### Phase 6: Restore and Expand Integration Coverage

At the end of the move, the following must still pass:

- unit tests
- integration tests
- architecture lint
- generated drift checks
- migration up/down/reapply tests
- RLS tests
- passport REST mutation restriction tests
- read-seat mutation restriction tests
- approval redemption tests
- composite FK tests

No architecture cleanup is successful if it weakens these.

## What Not To Do

Do not split into microservices.

Do not create many backend Go modules.

Do not create empty architecture theater.

Do not hide SQL behind a generic repository framework.

Do not let `platform` import business modules.

Do not let generated DTOs become the domain model.

Do not preserve `crm-core` as a permanent catch-all.

Do not perform a huge move without keeping tests green in phases.

## Final Recommendation

Yes: rework and clean up.

The current PoC is good enough to harvest, but the factory architecture is the better long-term home for an open-source product.

The best target is:

- **modular monolith**
- **one backend Go module**
- **clear process-role commands**
- **`backend/internal/modules` for bounded business capabilities**
- **`backend/internal/platform` for technical infrastructure**
- **`backend/internal/shared` for ports, kernel types, and error taxonomy**
- **contract-first API**
- **database-enforced tenancy and integrity**
- **architecture gates plus semantic integration tests**

This is a better architecture than the current top-level `crm-*` layout. It is more readable, more teachable, more agent-safe, and more credible as open-source software.

The right next move is to make the architecture decision explicit, then migrate in small verified phases.
