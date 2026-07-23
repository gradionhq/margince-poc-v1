# Platform & shared toolkit

The reusable utilities every module composes — **reach for these instead of reinventing them**. A new
store, handler, or consumer is mostly assembling the pieces below. Everything here lives under
`backend/internal/platform/` (technical plumbing) or `backend/internal/shared/` (stdlib-only leaves);
a module may import both, never a sibling module.

The signatures are abbreviated (contexts/errors elided) — read the package for the exact shape.

---

## `platform/` — plumbing

### `platform/database` — the pool & the workspace transaction
The **only** place the RLS GUC contract is implemented; no store issues its own `SET LOCAL`.
- `WithWorkspaceTx(ctx, pool, fn func(pgx.Tx) error) error` — the workspace transaction every store uses (binds the tenant to `app.workspace_id`).
- `WithInfraTx(ctx, pool, fn) error` — a no-tenant-GUC transaction for the few cross-tenant infra paths (relay, bootstrap).
- `NewPool(ctx, dsn) (*pgxpool.Pool, error)`, `RegisterIDTypes(conn)`.
- **Reach for it when:** you need a DB transaction — always through these, never a raw `pool.Begin`.

### `platform/database/storekit` — the write shape & store mechanics
The one spelling of "domain row + `audit_log` + `event_outbox` in one tx", plus pagination, version
patches, predicate compilation, and SQLSTATE branch helpers. (Deep dive:
[write-backbone.md](../explanation/write-backbone.md).)
- Write triple: `Audit(...) (auditID, err)`, `AuditWithEvidence(..., evidence)`, `Emit(..., auditID, eventType, ...)`.
- Version patch: `NewPatch()`, `Patch.Set(col, old, new)`, `ApplyWithVersion(...)`, `ApplyGuarded(..., ifVersion)`, `ApplyLocked(..., lock)` → `ErrVersionSkew`.
- Row locks: `LockRow(...)`, `LockPair(...)`.
- Keyset pagination: `EncodeCursor`, `DecodeCursor` (→ `MalformedCursorError`), `ClampLimit`, `QuickFindClause`.
- List predicates: `CompilePredicate(pred, fields, arg)`, `Query.SelectIDs(...)`.
- SQLSTATE branch: `IsUniqueViolation`, `UniqueViolation(err) (constraint, ok)`, `IsForeignKeyViolation`, `CheckViolation`, `ExclusionViolation`.
- Context/provenance: `Actor(ctx)`, `CapturedBy(ctx)`, `MustWorkspace(ctx)`, `StampFields`, `FieldOrigins`, `EmailSuppressed`, `SuppressionHash`, `EscapeLike`, `JSONArg`, `UUIDOrNil`.
- **Reach for it when:** writing a store — compose these instead of hand-rolling audit/outbox/pagination/version SQL.

### `platform/auth` — the admission point
Object RBAC + row scope enforced at every store entry point, so HTTP and MCP ride one gate. (Why it
lives here: [authorization.md](../explanation/authorization.md).)
- `Require(ctx, object, action)` — object-level gate (may this role do this verb on this type?).
- `EnsureVisible(ctx, tx, table, id)` — row scope on a single-row get/update/archive (out-of-scope → `ErrNotFound`).
- `EnsureLinkTarget(ctx, tx, table, id)` — RLS-scoped existence probe for FK targets.
- `VisibleTo(ctx, tx, table, id) (bool, error)` — non-erroring probe for dedupe-409 paths.
- `ScopeClause` / `ScopeClauseFor(table, alias, arg)` — the SQL row-visibility predicate for list/search builders.
- `AuthzRule(p, entityType, action)` — the `audit_log.authorization_rule` attribution string.
- `NewGate(authority).Admit(ctx, spec, resolve)` — the agent admission gate (scope ∧ tier ∧ seat), re-derived live.
- **Reach for it when:** any store read/write over an owner-scoped table, or admitting an agent/MCP call.

### `platform/events` — the bus side of the backbone
Outbox relay + consumer-group subscriber + dedupe. Never originates events. (Deep dive:
[write-backbone.md](../explanation/write-backbone.md).)
- `NewRelay(pool, rdb, log)`, `OutboxBacklog(ctx, pool)`, `PublishedTotal()`.
- `NewSubscriber(rdb, group, handler, log)`; `type Handler func(ctx, env) error`.
- `Dedupe(rdb, group, next)` (`DedupeTTL = 96h`), `ForWorkspace(wsID, next)`, `NewClient(ctx, addr)`.
- **Reach for it when:** consuming domain events or wiring the relay.

### `platform/httperr` — the sentinel → HTTP choke point
Maps `apperrors` sentinels to RFC 7807 problem+json with stable machine codes; no handler hand-writes a status body.
- `Write(w, r, err)` — maps any sentinel / `DetailedError` / parse error onto the wire; unknown → opaque 500.
- `NotImplemented(w, r, op)` — explicit 501 (what the generated stubs return).
- `Unauthorized(w, r, detail)`, `Validation(field, code, msg) *DetailedError` (422), `Duplicate(code, existingID) *DetailedError` (409).
- **Reach for it when:** a handler needs to return any error — return a sentinel/`DetailedError` and call `Write`.

### `platform/httpserver` — the HTTP chassis
The middleware every process role rides; owns no domain.
- Middleware: `Correlate` (mints the per-request `correlation_id`), `SecureHeaders`, `RecoverPanics`, `LimitBodies`, `AccessLog`.
- Probes: `Healthz`, `Readyz(checks…)`, `Metrics(pool, backlog, published)`.
- Logging: `LogHandler(w, level, format)`, `WithCorrelation(handler)`.
- **Reach for it when:** assembling any HTTP surface — wrap routes in these, don't reinvent middleware.

### `platform/jobs` — durable background work (River)
Peer of the outbox: an event announces something happened, a job asks for work to be done.
- `New(pool, cfg, log) (*Runner, error)`, `Migrate(ctx, pool)`.
- **Reach for it when:** you need durable, retryable background work (the worker's periodic passes ride this).

### `platform/blobstore` — object bytes
DB row stays system-of-record; the store holds opaque bytes at a workspace-prefixed key.
- `type Store interface { Put; Get; Delete; Health }`, `WorkspaceKey(ws, kind, id)`, `NewMemory()`, `New(ctx, cfg)`, `FromEnv(ctx)`, `ErrNotFound`.
- **Reach for it when:** persisting/fetching binary blobs tied to an entity (attachments, logos).

### `platform/keyvault` — secret material
A domain row references an opaque, workspace-scoped `Ref`; the vault holds the secret bytes. Plaintext/root key never reach a log.
- `type Vault interface { Put; Get; Delete; Health }`, `type Ref string` (log-safe, resolves only under its minting workspace), `New(cfg)`, `NewMemory()`, `FromEnv(pool)`.
- **Reach for it when:** storing/retrieving a credential a domain row points at (e.g. `connector_connection.credential_ref`).

### `platform/netguard` — SSRF egress guard
A tenant-supplied host must never probe the deployment's own network; classifies the *resolved* IP so DNS can't bypass it.
- `RefusePrivate(network, address, rawConn)` — a `net.Dialer.Control`; `PublicIP(ip) bool`.
- **Reach for it when:** building an HTTP client that fetches a tenant-supplied URL — set `Dialer.Control = netguard.RefusePrivate`.

### `platform/ratelimit` — in-process fixed-window limiter
For unauthenticated endpoints (login brute-force, workspace-bootstrap).
- `New(limit, window)`, `Allow(key)`, `Record(key)`, `Blocked(key)`, `NewWithClock(...)` (inject a clock in tests).
- **Reach for it when:** throttling an expensive unauthenticated endpoint by key (IP/email).

### `platform/dbmigrate` — the migration runner
Hand-rolled runner for the ownership namespaces (core, custom, packs), each with its own tracking table.
- `Load(fsys, dir)`, `Up(ctx, conn, namespaces…)`, `Down(ctx, conn, ns, n)`.
- **Reach for it when:** applying/rolling back migrations in tooling (usually you just run `cmd/migrate`).

### `platform/deployconfig` — the installation config (`margince.yaml`)
Loads the operator's deployment file (A107/ADR-0061): the singleton organization, the bootstrap admin,
auth/email/AI/capture posture, and the ordered `company_context.rollout` capability
(`off < read < tasks < onboarding`; empty resolves to `onboarding`).
- `Load(path)`, the typed `Config` tree, `EffectiveRollout()`.
- **Reach for it when:** a behavior is an operator deployment choice, not workspace data — it belongs in this file, not a new flag.

### `platform/mailer` — transactional email
The ONE outbound channel for product-originated mail (A74/ADR-0056), operator-configured SMTP; first
consumer is password-reset delivery. Consent's marketing lanes are a separate, gated concern.
- **Reach for it when:** the product itself must send a mail (never for tenant marketing sends).

### `platform/webread` — the public-web fetcher
The outbound page fetcher behind the scrape/enrichment seam: plain GETs of tenant-named pages reduced
to whitespace-normalized text. SSRF-guarded post-dial (every redirect hop re-enters the guard),
robots-aware, paced, byte/time-bounded. No extraction, no discovery policy — those stay with callers.
- **Reach for it when:** fetching a tenant-supplied URL — never hand-roll an `http.Client` for one.

### `platform/testdb` — test database harness
Migrate-once schema setup + fast `TRUNCATE` reset for the integration lanes (`EnsureSchema`,
`Truncate`); the `integrationmigrateonce_test.go` gate enforces its use.
- **Reach for it when:** writing a real-Postgres test setup.

---

## `shared/` — stdlib-only leaves

### `shared/apperrors` — the fixed error-sentinel registry
Callers branch with `errors.Is`; the HTTP/MCP choke-points own the wire mapping. **Never** invent a
new error string a handler must parse — extend this registry (with the contract) instead.
- Core sentinels: `ErrNotFound`, `ErrConflict`, `ErrScopeExceeded`, `ErrPermissionDenied`, `ErrRequiresApproval`, `ErrVersionSkew`, `ErrBudgetExceeded`, `ErrApprovalTokenInvalid`, `ErrConsentNotGranted`, `ErrSeatTierInsufficient`.
- Overlay sentinels: `ErrModeNotOverlay`, `ErrUnsupportedBySoR`, `ErrIncumbentAlreadyConnected`, `ErrOverlayFlipBlocked`, `ErrIncumbentBudgetExhausted`.
- **Reach for it when:** returning any domain error.

### `shared/kernel/ids` — UUIDv7 identifiers
Dependency-free so seam signatures don't drag in a UUID library.
- `type UUID [16]byte`, `Nil`, `NewV7()` (time-ordered), `Parse`, `MustParse`, `String()`, `IsZero()`.
- Typed ids: `type ID[K]`, `New[K]()`, `From[K](u)`, `ParseAs[K](s)`, and aliases `WorkspaceID`, `UserID`, `PersonID`, `DealID`, … (per-entity phantom types).
- **Reach for it when:** minting or parsing any entity id.

### `shared/kernel/principal` — per-request identity
The tenant key, acting principal, and trace ids on the context — read only through typed accessors (loose context keys are forbidden).
- Accessors: `WithActor`/`Actor`, `WithWorkspaceID`/`WorkspaceID`, `WithCorrelationID`/`CorrelationID`, `WithCausationEvent`/`CausationEvent`.
- `type Principal { Type, ID, UserID, TeamIDs, PassportID, OnBehalfOf, Scopes, SeatType, Permissions }`.
- Enums: `PrincipalType` (Human/Agent/Connector/System), `Scope` (Read/Draft/Write/Send/Enrich), `SeatType` (Full/Read, `.CanMutate()`), `Action` (Create/Read/Update/Delete), `RowScope` (Own/Team/All).
- `Permissions.Allows(object, action)`, `ScopeSet.Has(scope)`.
- **Reach for it when:** reading who's acting / the tenant / trace ids from context, or checking RBAC in memory.

### `shared/kernel/events` — the bus wire contract
The `Envelope`, the `<entity>.<verb>` catalog, and the stream layout — shared by publisher and consumer.
- `type Envelope { EventID, Type, Version, WorkspaceID, OccurredAt, Actor, Entity, Payload, Trace }`, `Envelope.Validate()`.
- `type Trace { CorrelationID, CausationID, AuditLogID }`, `type Actor`, `type EntityRef`.
- Catalog: `StreamFor(type)`, `VersionOf(type)`, `Types()`, `Streams()`, `Groups()`, `SplitType(type)`.
- **Reach for it when:** publishing to the outbox (via storekit) or writing a consumer. Adding an event type = one catalog line.

### `shared/kernel/provenance` — write provenance
Store APIs accept no write without it (missing provenance is a compile error).
- `type Provenance { Source, CapturedBy }`, `Validate()`.
- **Reach for it when:** any write — pass where the value came from and who wrote it.

### `shared/kernel/values` — domain value objects
Parse-don't-validate types for formats that would otherwise travel as bare strings: email lowercasing,
E.164 phones, host-only domains, slugs, timezones, the money pair. Each parses/normalizes ONCE at the
seam where input enters a store; a malformed value is unrepresentable downstream. Constructors return
`ParseError`, which the transport maps to the 422 validation shape.
- **Reach for it when:** accepting an email/phone/domain/money/… input — parse it here, don't validate ad hoc.

### `shared/kernel/diffhash` — the one diff-hash canonicalization
The ONE spelling of a `diff_hash` (ADR-0036): decode to maps, re-marshal (sorting keys at every
depth), hash. Staging, redemption, and modify-then-approve all hash through here — "identical call"
is a property of content, never whitespace or key order.
- **Reach for it when:** comparing or binding a staged payload by content.

### `shared/schema` — structured-output JSON Schema builder
Composable `Object`/`Array`/`String`/… builders rendering the `model.Request.ResponseSchema` value, so
every structured-output schema is compile-checked and built one way (objects are CLOSED —
`additionalProperties: false`).
- **Reach for it when:** constraining a model call to a JSON shape — never hand-write the schema string.

### `shared/ports/*` — the frozen seam interfaces
Dependency-free interfaces that decouple platform/AI/UI (and one module from another) from concrete
implementations; the composition root injects the impls. **Depend on the interface, never the concrete
module.**

| Port | Interface | Role |
|---|---|---|
| `authz` | `Resolver { EffectiveRBAC; SeatType }` | live RBAC/seat resolver the auth gate re-derives an agent's authority through (impl: identity) |
| `datasource` | `SystemOfRecordProvider { Read/Search/Create/Update/Archive/Merge/AdvanceDeal/PromoteLead/StageSemantic/RunReport/Freshness/ListObjects/ListFields }` | the system-of-record seam AI/MCP/UI bind to (impl: the compose `Provider` over people/deals/activities/reports) |
| `mcp` | `Tool { Spec; Handle }`, `Registry { Register; Invoke; Specs }` | the governed tool contract (`ToolSpec`, `RiskTier` auto_execute/confirmation_required/dynamic, tier resolver) — admission runs before `Handle` |
| `connector` | `Connector { Descriptor/Authenticate/Sync/Normalize/HealthCheck }`, `Sink { Upsert }` | the capture/integration seam; a connector normalizes, the capture module writes |
| `model` | `Client { Complete/Stream/Embed/Caps }`, `SecretStripper` | the provider-agnostic LLM seam (model choice is config, not architecture) |
| `retrieval` | `Retriever { Search; AssembleContext }` | grounded hybrid retrieval with per-item evidence (evidence-or-omit) |
| `workflow` | `Handler { Spec; Match; Plan }` | the automation seam — typed trigger + effect + idempotency key + risk tier; runs ride the job queue |
| `extraction` | `Extractor { Extract }` | the staged attachment-extraction seam — grounded-or-omitted fields, never a guess (prod default: the honestly-empty `NoOpExtractor`) |
| `fieldcatalog` | `Reader { ActiveColumns }` | how record stores learn the active `cf_*` custom-field columns per object (impl: customfields' Service) without importing the module |
| `jurisdiction` | `Pack { Code; Retention }` + `Register`/`For`/`Applicable` | compile-time country packs (the `de` pack); core never names a jurisdiction |

**Reach for a ports interface when:** code above a seam needs a module's capability without importing it.

---

## The pattern, in one line

A typical CRUD store method = `WithWorkspaceTx` (database) → `auth.Require`/`EnsureVisible` (auth) →
SQL over your owned tables → `storekit.Audit` + `Emit` (storekit) → return, mapping errors through the
`apperrors` sentinels that `httperr.Write` renders. You wrote the SQL and the mapping; everything else
is composed from the toolkit above.
