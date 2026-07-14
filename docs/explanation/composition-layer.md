# The composition layer

`internal/compose/` is the only layer that knows about more than one module. Modules stay flat and
**never import each other** ([architecture.md](architecture.md)); compose is where they are assembled
into the running binaries and where **every cross-module edge is injected**. This page explains how it
initializes and where things are wired. To *add* a feature that touches it, see
[how-to/add-a-module.md](../how-to/add-a-module.md).

## What compose owns

- **The composite HTTP `Server`** — every module's handlers, shadowing the generated 501 stubs.
- **The cross-module edges** — injected as small adapters, so no module imports a sibling.
- **The datasource `Provider`** — the system-of-record seam the agent/MCP surface binds to.
- **The MCP tool registry** — the one governed tool surface, shared by `cmd/mcp` and the REST agent gate.
- **The background wiring** each binary needs — the River job runner, the Surface-B runner, the
  workflow engine, the capture registry.
- **The per-role `Option`s** — how each binary customizes the wiring.

## The `Server`: module handlers shadow generated stubs

`Server` (`server.go`) embeds each module's handler set (via a type alias per module) **and** the
generated `stubs`. Go promotes the shallower method, so a module handler shadows the matching 501 stub:

```go
type Server struct {
    authHandlers        // = identity.Handlers
    peopleHandlers      // = people.Handlers
    dealsHandlers       // = deals.Handlers
    …                   // one embedded handler set per module
    // + injected infra: busReady, blob, vault, log
}
var _ crmcontracts.ServerInterface = Server{}   // compile-time completeness guarantee
```

That assertion is load-bearing: if a regenerated contract adds an operation nothing implements,
`Server` stops satisfying `ServerInterface` and the build fails **here** — the stubs are simultaneously
the fallback (an unimplemented op answers a loud 501, never a silent 404) and the drift gate's
inventory. (The generated stubs live in `stubs_gen.go`; see [contract-first.md](contract-first.md).)

## How it boots — `compose.New` (the api handler)

`cmd/api` calls `compose.New(pool, log, opts...) http.Handler`. The pipeline (`server.go`):

```go
func New(pool, log, opts...) http.Handler {
    dealsH := deals.NewHandlers(pool)
    identitySvc := identity.NewService(pool)
    authH := identity.NewHandlers(identitySvc, workspaceSeed(dealsH)) // ← bootstrap hook (below)

    srv := newServer(pool, log, authH, dealsH)  // 1. assemble handler sets + cross-module edges
    for _, opt := range opts { opt(&srv, pool) } // 2. per-role customization

    api := contractAPI(srv, pool, identitySvc)   // 3. mount /v1 (generated router + admission)
    mux := operationalMux(srv, pool, log, authH, api) // 4. health/ready/metrics/public/oauth/SPA
    return httpserver.RecoverPanics(log, httpserver.LimitBodies(httpserver.SecureHeaders(mux))) // 5.
}
```

1. **`newServer`** builds every module's handler set and injects the cross-module edges (see the map
   below).
2. **Options** apply per-role customization (blobstore, keyvault, bus probe, …).
3. **`contractAPI`** mounts the generated chi router at `BaseURL: "/v1"` with two middlewares:
   `agentGate` (the transport-agnostic admission layer — same tier table as the MCP surface) then
   `idempotency`. Idempotency sits **outermost** so a staged-approval refusal is never recorded as
   "the" response for an idempotency key (the approved retry is the same request under the same key).
4. **`operationalMux`** mounts the contract surface next to `/healthz`, `/readyz` (role-specific
   dependency probes), `/metrics`, the anonymous `/v1/public/*` edges, the `/oauth` A2 authorization
   server.
5. The whole thing is wrapped `RecoverPanics → LimitBodies → SecureHeaders`.

## The workspace-bootstrap seed (one transaction)

`workspaceSeed` is the hook `identity` runs when a workspace is bootstrapped (`POST /v1/workspaces`). It
seeds **every module's per-workspace defaults in ONE transaction** — they stand or fall together — yet
identity imports none of those modules, because the hook is injected here:

```go
func workspaceSeed(dealsH) func(ctx, tx) error {
    // deals default pipeline → consent purposes → consent retention →
    // agents starter automations → activities booking page — all in the caller's tx
}
```

## The cross-module edges (the map)

Every edge is an **adapter constructed in compose** that implements the consumer's small interface,
backed by the provider module's store — so neither module names the other. The current edges
(`newServer` + the blob/vault Options):

| Consumer | ← needs | Wired as |
|---|---|---|
| identity (bootstrap) | deals + consent + agents + activities defaults | `workspaceSeed(dealsH)` (one tx) |
| activities | consent's outbound suppression gate; people (public booking); consent (unsubscribe link) | `.WithConsent(...)`, `.WithPublicBooking(...)`, `.WithUnsubscribe(...)` |
| consent (DSR erase) | privacy's `Eraser` (blob-aware under `WithBlobstore`) | `consent.NewHandlers(pool).WithEraser(privacy.NewEraser(pool))` |
| agents / automations | approvals' staging + redemption (the 🟡 confirm-first effects) | `approvalsHandlersWithEffects(pool)` (`.WithEffects(...)`) |
| signals | people's relationship-strength | `signalStrength{people: people.NewStore(pool)}` adapter |
| imap connect | capture's connector registry (vault under `WithKeyvault`) | `imapConnectHandlers{registry: NewCaptureRegistry(pool, vault)}` |
| filtered export | collections' saved-view/list source | `filteredExportHandlers{collections: collections.NewStore(pool)}` |

The shape to copy: *a small consumer-side interface + a compose adapter struct that satisfies it from
the provider's store.*

## Per-role `Option`s, and "declare absence by omission"

An `Option func(*Server, *pgxpool.Pool)` customizes the wiring for one process role; everything not
optioned keeps its safe default. The current options: `WithBusReady`, `WithBlobstore`, `WithKeyvault`,
`WithPublicBaseURL`, `WithColdStart`, `WithScrape`, `WithBrief`.

The important rule: **a capability whose infra a role wasn't given leaves its endpoints as the generated
501 stub rather than nil-derefing at request time.** No `WithBlobstore` → the `/attachments` endpoints
answer 501 (a role that stores no objects declares that by omission). A capture-capable role must pass
`WithKeyvault` or fail to boot. `/readyz` probes exactly the dependencies the role wired — so a split
deployment answers ready on what it actually depends on.

## The other compose entry points (per binary)

Each binary composes only what its role needs, all through this one layer:

| Entry point | Builds | Used by |
|---|---|---|
| `New(pool, log, opts…)` | the api HTTP handler | `cmd/api` |
| `NewProvider(pool)` | the `datasource.SystemOfRecordProvider` (people/deals/activities/reports) | the agent gate + MCP registry |
| `NewRegistry(pool)` | the MCP tool registry | `cmd/mcp` + the REST agent gate |
| `NewJobRunner(pool, log, …)` | the River periodic jobs (close-date sweep, reconcile) | `cmd/worker` |
| `NewRunnerService(pool, brain, retriever, log)` | the Surface-B reasoning runner | `cmd/worker` |
| `NewWorkflowEngine(pool)` | the workflow dispatcher | `cmd/worker` |
| `NewCaptureRegistry(pool, vault)` | the connector registry | `cmd/worker` backfill, api imap-connect |

## Where the code lives

| | |
|---|---|
| `Server`, `New`, `newServer`, Options, `contractAPI`, `operationalMux` | `internal/compose/server.go` |
| The datasource provider | `internal/compose/provider.go` |
| The MCP registry | `internal/compose/registry.go` |
| The REST admission middleware | `internal/compose/agentgate.go`, `idempotency.go` |
| Background wiring | `internal/compose/{jobs,runnerservice,workflows,capture}.go` |
| Generated (never edit) | `internal/compose/{stubs_gen,agentpolicy_gen}.go` |
