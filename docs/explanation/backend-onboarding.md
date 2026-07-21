# Backend contributor orientation

The single starting point for a developer new to the **margince-next** backend — the map that ties the
other docs together, plus the code-level detail and change-recipes they leave out. It does **not**
re-explain what they already own.

## Start here (reading order)

1. **[tutorials/getting-started.md](../tutorials/getting-started.md)** — clone → running instance in five commands. Do this first.
2. **[explanation/architecture.md](architecture.md)** — the shape: the `shared → platform → modules → compose → cmd` DAG, the spine shapes, tenancy-as-structure. The *why*.
3. **[explanation/contract-first.md](contract-first.md)** — how the Go surface is generated from `crm.yaml`, and why drift is merge-blocking.
4. **[explanation/authorization.md](authorization.md)** — why the auth check lives at the store entry point, not the handler.
5. **This page** — the system in one screen, the find-it map, generated-vs-written, the code you call, the gates, the change-recipes.
6. **[CONTRIBUTING.md](../../CONTRIBUTING.md)** + **[AGENTS.md](../../AGENTS.md)** — the PR loop and the binding engineering rules.

**Deep dives** — read when you touch that area:

- **[reference/modules.md](../reference/modules.md)** — what each module owns.
- **[reference/platform-toolkit.md](../reference/platform-toolkit.md)** — the reusable utilities (reach for these, don't reinvent).
- **[explanation/write-backbone.md](write-backbone.md)** — storekit, `audit_log`, the outbox.
- **[explanation/composition-layer.md](composition-layer.md)** — how `internal/compose` boots and wires the modules.
- **[explanation/agent-surface.md](agent-surface.md)** — the agent reasoning loop + model runtime.
- **[explanation/privacy-and-consent.md](privacy-and-consent.md)** — the consent gate + GDPR engines.
- **[explanation/custom-fields.md](custom-fields.md)** — the one runtime `ALTER TABLE` chokepoint + the `fieldcatalog` seam.

**Reference** — look it up:

- **[reference/make-targets.md](../reference/make-targets.md)** — every `make` target.
- **[reference/configuration.md](../reference/configuration.md)** — every flag and env var.
- **[how-to/apply-migrations.md](../how-to/apply-migrations.md)**, **[mint-a-passport.md](../how-to/mint-a-passport.md)** → **[run-the-mcp-server.md](../how-to/run-the-mcp-server.md)** — common tasks.

---

## The system in one screen

Margince is a **governed, multi-tenant CRM** — a Go backend serving a contract-defined HTTP API under
`/v1` (plus an MCP tool surface for AI agents) over Postgres + Redis. "Governed"
is the theme: every read is tenant-isolated and RBAC-scoped, every write is audited and announced as an
event, and every AI action is tiered (auto-execute vs. stage for human approval).

**What happens on one request:**

1. **`cmd/api`** receives the request; middleware binds actor + workspace + `correlation_id` onto the context (and, for an agent, resolves the autonomy tier).
2. The **generated router** dispatches to the operation's method on `compose.Server` — a real module handler shadowing a generated 501 stub.
3. The **handler** decodes and calls its module's **Store/Service**, where the RBAC gate and the workspace transaction (RLS) live. Handlers never decide authorization.
4. The **Store** runs SQL over the tables it owns inside `WithWorkspaceTx`; a mutation writes the domain row + an `audit_log` row + an `event_outbox` row in that one transaction.
5. After commit, the **outbox relay** ships the event to Redis, where consumer groups (context-graph, workflows, overnight reasoning) react. The handler maps any error through a sentinel and returns.

Everything below is the detail behind those five steps:

- **The contract** (`backend/api/crm.yaml`) defines the surface; the Go is generated from it — [contract-first.md](contract-first.md).
- **The modules** (`internal/modules/`) are the capabilities; each owns its tables and never imports a sibling — [reference/modules.md](../reference/modules.md).
- **The platform toolkit** (`internal/platform/`, `internal/shared/`) is the reusable plumbing every module composes — [reference/platform-toolkit.md](../reference/platform-toolkit.md).
- **The compose layer** (`internal/compose/`) wires the modules into the four binaries and injects every cross-module edge — [composition-layer.md](composition-layer.md).

---

## Find-it map — where things live, where to put things

One Go module, `github.com/gradionhq/margince/backend`, rooted at `backend/`. When you're looking for
something (or deciding where new code goes):

| You want… | It lives in |
|---|---|
| An HTTP operation's request/response shape | `backend/api/crm.yaml` (the contract — source of truth) |
| Generated types + the `ServerInterface` | `internal/contracts/api_gen.go` *(generated — never edit)* |
| A capability's store, handlers, SQL | `internal/modules/<name>/` (flat package; **which module owns what → [reference/modules.md](../reference/modules.md)**) |
| A reusable utility (don't reinvent it) | `internal/platform/*`, `internal/shared/*` (**catalog → [reference/platform-toolkit.md](../reference/platform-toolkit.md)**) |
| The pool + the workspace-transaction contract | `internal/platform/database/database.go` |
| The one write shape (audit + event) | `internal/platform/database/storekit/` |
| The admission gate (RBAC + tier + seat) | `internal/platform/auth/` |
| The outbox relay / bus dedupe | `internal/platform/events/` |
| Cross-module wiring, the HTTP `Server`, the MCP registry | `internal/compose/` (**how it boots + the edge map → [composition-layer.md](composition-layer.md)**) |
| Stdlib-only leaves (ids, principal, apperrors, ports) | `internal/shared/` |
| SQL migrations | `backend/migrations/core/` (upstream) · `custom/` (fork) |
| The four process binaries | `backend/cmd/{api,worker,migrate,mcp}/` |
| Codegen tools | `backend/tools/` (its own Go module) |
| The architecture gates (fitness tests) | `backend/*_test.go` (see below) |

**The rule you'll hit first:** a module in `internal/modules/` **never imports a sibling module**.
If capability A needs capability B, the edge is injected in `internal/compose/`, never by importing B.
Three gates enforce this (depguard, go-arch-lint, `arch_test.go`), so a sibling import fails
`make check` — it is not a style preference.

---

## What's generated vs what you write

A common early confusion is which files you edit. `make gen` derives Go (and TS) from the contract;
you never hand-edit its output — the drift gate (`make drift`, part of `make check`) fails a hand
edit. Everything else you author.

**Generated by `make gen` — never hand-edit** (each carries a `DO NOT EDIT` header):

| File | What it is | Produced from |
|---|---|---|
| `internal/contracts/api_gen.go` | request/response model types, the `ServerInterface`, the chi router | `crm.yaml` → 3.0 overlay → oapi-codegen |
| `internal/compose/stubs_gen.go` | one explicit **501** stub per operation (the shadow-able fallback) | `ServerInterface` |
| `internal/compose/agentpolicy_gen.go` | the agent admission table (verb/tier per route) | `crm.yaml` `x-mcp-tool` / `x-agent-access` |
| `internal/modules/ai/tasks_gen.go` | the AI task registry (task → tier ladder / execution mode) | `api/ai-tasks.yaml` (via `tools/gen-aitasks`) |
| `config/ai-routing.schema.json` | the routing-config validation schema | `api/ai-tasks.yaml` (via `tools/gen-aitasks`) |
| `.build/openapi30.yaml` | the downgraded contract (build artifact, gitignored) | `crm.yaml` |
| `frontend/src/api/schema.d.ts` | the SPA's TS types | `crm.yaml` (via `pnpm gen:api`) |

**Hand-written — you author these:**

| File | What you write |
|---|---|
| `backend/api/crm.yaml` | the contract itself — the *input* to codegen (this is the one "source" file the generators read) |
| `internal/modules/<name>/` | the handler methods, the store/service, the SQL |
| `backend/migrations/{core,custom}/*.sql` | schema changes (up + down) |
| `internal/compose/{server,provider,registry}.go` + adapters | cross-module wiring and edges |
| `backend/**/*_test.go` | unit, fitness, and integration tests |
| `docs/**` | docs |

So a normal feature is: **edit `crm.yaml` → `make gen` (machine writes the plumbing) → you write the
handler + store + SQL + tests** (the recipes below).

---

## The deployment model (the binaries)

Four process roles, all assembled through `internal/compose`. Flags/env are tabled in
[reference/configuration.md](../reference/configuration.md); the shapes to understand:

- **`cmd/api`** — the HTTP surface on `:8080`. By default (`--inline-relay=true`) it *also* ships the
  outbox to Redis in-process, so **one `cmd/api` is a complete install** for dev and small self-hosted
  deployments.
- **`cmd/worker`** — background consumer (the standalone relay, the River periodic jobs, retention,
  the automation trigger runtime — event dispatch off `cg:workflows` plus the clock time-scan — the
  Surface-B runner). Only needed for **split deployments**: run `cmd/api --inline-relay=false`
  alongside one or more workers. River gives leader election, so worker replicas never double-run a
  job.
- **`cmd/migrate`** — `up`/`down`, connects with the **owner** role (the app role never owns schema).
- **`cmd/mcp`** — the governed agent tool surface over stdio or hosted HTTP.

---

## How a store reads and writes (the shape)

Every store method follows one shape: open the **workspace transaction**, gate at the entry point, run
SQL over the tables the module owns, and — for a mutation — commit the domain row + an audit row + an
event row together. That is the whole write contract in one call site (illustrative, not copy-paste Go):

```go
func (s *Store) CreateDeal(ctx context.Context, in CreateDealInput) (Deal, error) {
    if err := auth.Require(ctx, "deal", principal.Create); err != nil { return Deal{}, err }
    return database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
        // INSERT INTO deal (...) VALUES (...)                        ← the domain row(s)
        auditID, _ := storekit.Audit(ctx, tx, "create", "deal", id, nil, after)   // ← audit_log row
        return storekit.Emit(ctx, tx, auditID, "deal.created", "deal", id, payload) // ← event_outbox row
    })
}
```

`WithWorkspaceTx` binds the tenant to Postgres row-level security, so isolation holds even if a query
forgets its `WHERE`; the three rows commit in the one transaction. Go deeper where you need it: the
**tenant isolation** (RLS, `WithWorkspaceTx`, the auth gate) is in
[authorization.md](authorization.md); the **write backbone** (the `audit_log` DDL, the outbox envelope,
the relay, dedupe) is in [write-backbone.md](write-backbone.md).

Notes that trip people up:

- **`captured_by` / the actor is server-stamped** from the authenticated principal, never the request body.
- **`Emit` requires a `correlation_id`** on the context (the HTTP middleware binds one per request; a
  bespoke background job must bind its own).
- **Updates need a concurrency guard** — `storekit.Patch.ApplyWithVersion` / `ApplyGuarded`, never a
  bare by-id UPDATE (a fitness test enforces it).
- **RBAC is gated at the store entry point** (`auth.Require` + `auth.EnsureVisible`): object denial →
  403, row-scope miss → 404 (existence-hiding). Why it lives here: [authorization.md](authorization.md).
- **Publish only through the outbox** — never `XADD` from domain code.

You compose a store from the toolkit rather than hand-rolling it — the full helper set (auth gate,
storekit, ids, principal, error sentinels, seams) is catalogued in
[reference/platform-toolkit.md](../reference/platform-toolkit.md).

---

## The gates that judge your PR (fitness functions)

The root of `backend/` holds `go test` "fitness functions" — each turns a rule you'd otherwise have to
remember into a mechanical gate that derives its scope from the tree or the live schema. Knowing they
exist tells you what will fail your PR and why:

| Test file | Fails your change if… |
|---|---|
| `arch_test.go` | you break the import DAG (e.g. a module imports a sibling) |
| `writeshape_test.go` | an audited mutation doesn't also emit an outbox event |
| `tableownership_test.go` | a module writes SQL against a table it doesn't own |
| `rbacgate_test.go` | an exported `*Store`/`*Service` method doesn't reference the auth gate |
| `updateguard_test.go` | a single-row-by-id UPDATE of a versioned table has no concurrency guard |
| `enumsync_test.go` | a Go enum drifts from its schema `CHECK (col IN (...))` set |
| `consentproof_test.go` | a `person_consent` state write skips its append-only proof row |
| `piicoverage_test.go` | a PII table isn't reached by erasure + SAR |
| `errmatch_test.go` | code classifies an error by its `Error()` string instead of SQLSTATE |
| `license_test.go` | a hand-written `.go` file lacks the BUSL-1.1 SPDX header |
| `auditcoherence_test.go` | the contract's `audit_log` action/actor enums drift from the schema CHECKs |
| `contractrefs_test.go` | `crm.yaml` contains a dangling local `$ref` |
| `formulafieldscope_test.go` | a contract operation accepts a writable `formula_sql` (formula fields are DB-generated, never runtime-authored) |
| `idempotencymap_test.go` | compose's idempotent-operations map drifts from the contract's Idempotency-Key declarations |
| `integrationmigrateonce_test.go` | a compose/integration suite re-runs its own migrate instead of the shared migrate-once harness |
| `workflowhandler_test.go` | a workflow `Match`/`Plan` mutates (only `Apply` may write) |
| `migrations/migrations_test.go` | the embedded core/custom migration namespaces don't form a loadable sequence |
| `internal/compose/integration/rls_coverage_integration_test.go` | a `workspace_id` table lacks RLS ENABLE+FORCE+policy |

They run in `make check` (unit ones uncached — they walk the module tree) and `make test-integration`
(the RLS one). The full merge loop (gates, DCO sign-off, craft pre-push hook) is in
[CONTRIBUTING.md](../../CONTRIBUTING.md); every target is in
[make-targets.md](../reference/make-targets.md).

---

## Change recipes

Step-by-step task walkthroughs live in **how-to** (the home for recipes) — they thread the contract,
codegen, and the store shape above into one checklist:

- **Add or change an API endpoint** → [how-to/add-an-endpoint.md](../how-to/add-an-endpoint.md)
- **Add a new module or a cross-module edge** → [how-to/add-a-module.md](../how-to/add-a-module.md)
- **Add a database migration** → [how-to/apply-migrations.md](../how-to/apply-migrations.md)
- **Create an automation workflow** → [how-to/create-a-workflow.md](../how-to/create-a-workflow.md)

---

## Coordinates

| | |
|---|---|
| Go module | `github.com/gradionhq/margince/backend` (root `backend/`) |
| API port | `:18080` under `make dev`, behind the app on `:8080`, which proxies `/v1` to it (`:8080` when the api runs standalone) |
| Postgres / Redis / MinIO | `localhost:55432` / `56379` / `59000` |
| Owner DSN (migrate) | `postgres://margince_owner:dev@localhost:55432/margince` |
| App DSN (api/worker) | `postgres://margince_app:margince_app_dev@localhost:55432/margince` |
| Tenant GUC | `app.workspace_id` (set transaction-local by `WithWorkspaceTx`) |
| Contract | `backend/api/crm.yaml` — regenerate with `make gen` |
| Generated (never edit) | `internal/contracts/api_gen.go`, `compose/stubs_gen.go`, `compose/agentpolicy_gen.go`, `modules/ai/tasks_gen.go`, `config/ai-routing.schema.json` |
| Merge gate | `make check` (+ `make test-integration`, needs `make db-up`) |

Every flag/env: [configuration.md](../reference/configuration.md). Every target:
[make-targets.md](../reference/make-targets.md).
