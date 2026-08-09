# Extension tier — capability expansion

The tier today composes Go registrations only: two capability kinds (jurisdiction packs, agent tools),
with the `api/`, `frontend/` and `migrations/` slices refused at generation. This design lands those
slices plus secrets and background jobs, proves them with a demo unit a human drives from the SPA, and
leaves `extensions/zalo-personal` as a unit that adds no tier surface of its own. **Two of the three:
`api/` and `migrations/` landed; `frontend/` is still refused, and §4.5 records what shipped in its
place and why.**

Review history is in `REVIEW-v1.md`. Demo detail is in `CRM-DEMO-SCOPE.md`.

> **Reconciled against the build, 2026-08-09.** This document was the source of truth the plan was
> written from, and it is the thing a follow-on author reads first — so it has been corrected in place
> against what fifteen task slices, two whole-branch reviews (Codex, Fable) and two acceptance runs
> actually produced on `feat/extension-tier-capabilities` (PR
> [#659](https://github.com/gradionhq/margince-poc-v1/pull/659)). Corrections are made where the claim
> was, not appended elsewhere; where a correction came from a demonstrated failure the demonstration is
> named. **Where the ledger and the code disagreed, the code won.** The evidence, in order of
> authority: the code; `.superpowers/sdd/extension-tier-slices/progress.md` (the ledger — every task,
> finding, ruling and deferral, in order); then `REVIEW-fable.md`, `REVIEW-codex.md`,
> `UAT-EVIDENCE-RERUN.md` and the `task-*-report.md` files, all in the same directory.
>
> One thing not to lose in the corrections: the four load-bearing properties this design was built
> around — additive composition, the inert declaration, validate-then-apply, and the empty-tree
> byte-identity guarantee — **all held, and are machine-held rather than asserted.** §2.3 records how.
> `zalo-personal` (PR 2) was not built; the tier surface it would consume was.

---

## 1. Principles

The tier's existing four, unchanged:

1. **Presence is enablement.** A unit is enabled because its directory exists under `extensions/`.
2. **A declaration is inert data.** `New()` returns a plain value holding no handle into the running
   server. Only boot reconciliation, after the whole set validates, applies anything.
3. **Grow additively, never in place.** Capabilities are fields; existing units keep compiling.
4. **One narrow backend surface, enforced.** A unit imports only marker-allowlisted `backend/pkg/**`.

Four this design adds:

5. **One namespace token, every surface.** `ext_<name>`, derived from the manifest name, applied to
   tables, roles, routes, job kinds, RBAC objects, secrets and frontend routes alike. Note that "roles"
   here is the *gate-time* role only — see §3 and §4.3.
6. **Sovereign inside the namespace, powerless outside it.** A unit manages its own data, resources,
   secrets and jobs however it likes. It reaches core only through published interfaces. This is a
   *design* principle — the shape of the surface a unit is offered — and, apart from the compiler-enforced
   half, it is not enforced against a unit that declines to follow it. See §2.0. **Read this as the
   weaker half of a pair:** "powerless outside it" holds; "sovereign inside it" does not, because
   `margince_app` holds DML on *every* unit's tables, so one unit's namespace is not walled off from
   another's at runtime. Quote #628, not the principle.
7. **The consumer drives the surface.** A slot, port or capability ships only with a concrete consuming
   extension — never speculatively. Every published DTO is frozen from its first consumer.
8. **Contract-first for declaration and governance.** An extension declares its surface in the same
   contract shapes core uses, and the manifest an operator approves derives from those contracts.
   **Extensions do not join core's closed generated sets** — they register through seams (§4.0).

## 2. What the tier guarantees

### 2.0 The threat model, first

Read this before any guarantee below, because every one of them means something different without it.

**The units this tier is built for are reviewed, first-party or otherwise trusted code.** They are
compile-time and operator-installed, never dynamically loaded, and they run **in the same process** as
the core. The composed set *is* the trust boundary: the vanilla tree ships only first-party units and an
installation adds one deliberately.

Consequently every wall this design describes is **defence in depth against mistakes, not a sandbox
against malice**. Its job is to turn the accidental cross-tenant query, the forgotten scope, the
retained handle and the wrong namespace into loud, early failures. Against a unit that is *trying*, none
of it holds, and three of the reasons are structural rather than unfinished work:

- **In-process Go.** A handler can `import "os"` and read `MARGINCE_KEYVAULT_ROOT_KEY` exactly as
  `keyvault.FromEnv` does, then decrypt any unit's ciphertext directly. It can open its own database
  connection, reach the network, or import anything its own `go.mod` lists. No published-surface design
  can prevent this while the code runs in the process; only a different execution model (out-of-process
  units, WASM) would.
- **The tenant pin is a settable GUC.** `Runtime.Tx` binds `app.workspace_id` from the invocation and the
  RLS policies read it — but a unit can rebind it with `SELECT set_config('app.workspace_id', …, true)`
  through the very verbs the seam publishes. **Verified against the shipped schema, not assumed.** It is
  not fixable at that layer: re-binding before every statement is defeated by one statement (a CTE that
  rebinds and a sibling scan that reads under the new value), and a PostgreSQL GUC cannot be made
  immutable. SQL-parsing defences are explicitly rejected — a wall made of statement inspection is a wall
  made of guesses.
- **One shared runtime role.** Every handler runs as `margince_app`, which holds DML on core tables, on
  *every* unit's `ext_<name>_*` tables, and on `extension_secret`. So within one tenant the unit wall
  exists at the *port* (the `Secrets` interface cannot express another namespace) and not in the
  database.

**Issue #628 (a per-unit database role) is the single change that would move any of this from convention
to enforcement**, and even then it bounds only the database — the in-process root-key reach is inherent
and would remain. Running an untrusted unit in a composed build is outside what this design supports, and
nothing in this repository should be read as claiming otherwise.

### 2.1 What is guaranteed

Against the mistakes above, and inside §2.0's trust model:

- **Full use of core** — code generation, the contract pipeline, the agent surface, the job fleet, RBAC,
  audit.
- **Core reachable only through published interfaces.** Enforced by the compiler: a unit's module path
  sits outside the backend module, so `internal/**` is unreachable by construction. This one holds
  against hostile code too — it is the compiler's, not a convention.
- **No RLS exemption.** Extension runtime code is bound by row-level security exactly as core code is,
  and holds no exemption core code lacks (§4.3). What it does hold is the ability to rebind the value the
  policies key on (§2.0).
- **Nothing requested silently.** A capability is a *request*, statically derived from the unit's
  contracts and recorded in `manifest.generated.json` with a digest covering unit, kind, contract,
  operation, route, method and fragment hash. An operator can see, diff and review the full set of
  capabilities an installation is about to serve. **Delivered and verifiable by reading
  `extensions/crm-demo/manifest.generated.json`** — every row carries all seven fields, and `route`
  carries the *contract* spelling (§3).
- **A mutating operation names something a role document can withhold.** `Verb.Validate` refuses any
  `write`- or `draft`-scoped declaration that names no RBAC object, at generation and at boot, for
  handler-bearing and contract-only verbs alike (`pkg/extension/verb.go:validateGovernance`).
  **This guarantee was not in the design and exists because its absence shipped:** crm-demo's
  store-signing-key declared neither object nor action, so the serving adapter's object check never
  ran and the operation was admitted on scope ∧ seat ∧ tier ∧ quota alone — which, for a
  cookie-session human, is any authenticated seat. The acceptance re-run (finding R1) had a read-only
  seat replace the installation's signing key on both the REST route and the agent transport. The rule
  closes the class for every future unit rather than patching the one unit.
- **The empty tree reproduces the vanilla build byte-for-byte.** Not a claim about extensions but
  about their absence, and the branch's strongest property — see §2.3.

### 2.2 What is NOT guaranteed, and was in an earlier draft of this design

- **"Nothing granted silently — `approvals.lock` resolves it fail-closed."** **Not delivered.**
  `approvals.lock` is digested for staleness and **never parsed**. Every composed unit's handler-bearing
  tools are served at their declared tier with no per-capability operator resolution; the composed set is
  the trust boundary instead (`compose/extensiontools.go`'s TRUST MODEL comment is the accurate
  statement). The *request* half above is real, so the lock can bind later without churn — but the
  present tense must not be used for it, in this document, in the PR, or in the ADR.
- **Exfiltration prevention.** Nothing prevents a unit from exfiltrating data it was legitimately
  granted, and per §2.0 nothing prevents a hostile one from reaching data it was not.
- **Removal leaves no trace.** Removal *disables* cleanly — routes 404, inventory omits the unit,
  migrations skip it, the composition reproduces. It does not **purge**: the unit's tables and rows, its
  `extension_secret` rows and keyvault ciphertext, and its grants inside `role.permissions` all survive.
  There is no purge primitive until #628 gives the tables an owner to `DROP OWNED BY`. `down` cannot
  revert a removed unit's migrations either — the unit is gone, so its SQL is gone with it.
- **Removal is one place.** **It is two**, and that is the recorded cost of §4.5's ruling: the unit
  directory, *and* the unit's screen plus its line in `frontend/src/screens/ext/index.tsx`. Leaving the
  entry behind fails `make fe-typecheck-composed` — the gate doing its job, on a removal that looks
  complete. The acceptance re-run (finding F5) found the documented recipe also needed the formatter
  step: deleting the last registry entry leaves `= {\n};` and `check-fe` fails on formatting alone. It
  briefly *was* three places — a `gen-composition` fixture hard-coded crm-demo's path — which was fixed
  there rather than documented, because removing a unit must not require editing core tests.

### 2.3 What held — and is machine-held, not asserted

Recorded as deliberately as the failures above, because a design document that logs only its errors
misleads in the other direction. Each of these is a property some gate would fail on, not a claim:

- **Grow additively, never in place.** Every capability landed as a new field on `extension.Extension`
  (`Secrets`, `Jobs`, `Migrations`). `de`, `yogi` and the `crm-hello` fixture compiled through all
  fifteen slices unchanged. The one signature break — `ToolHandler` gaining `rt` — raises a `pkg-freeze`
  advisory rather than passing silently, and will hard-fail from the first v1 tag.
- **A declaration is inert data.** `New()` still returns a plain value. `gen-composition` refuses a
  `Handle` that is not a plain function identifier (including the `mustDial(nil)` call form —
  mutation-verified), package-level `init()`, call-bearing var initializers, and **any Go package below
  the unit root**, which was a real bypass: one file at `extensions/foo/internal/live/live.go` plus one
  blank import escaped the AST walk entirely. Both holes were found by review of Task 4 and both fixes
  were mutation-verified as *the gate passing*, not as incidental breakage.
- **Validate-then-apply.** The same `Verb.Validate` runs at generation and at boot, so gen-time
  acceptance cannot drift from boot-time validation; `buildExtensionTools` validates the whole composed
  set — duplicate verbs, cross-unit tool-name collisions, tier, scope, description, version — before any
  registry is built, and RBAC vocabulary registration happens only after every verb validates.
- **The empty tree reproduces the vanilla build byte-for-byte.** `extensions_gen.go`,
  `extensions.gen.ts` and all four merged contracts are byte-identical to their committed vanilla
  copies with `extensions/` emptied; `check-composition` is green; CI's `extension-reference` job proves
  it on every backend PR by moving the tracked units aside and `cmp`-ing. The two-lane tsconfig story
  keeps the committed vanilla `schema.d.ts` and its drift gate intact, and the hand-written core-screen
  registry sits deliberately outside the generated tree so it cannot perturb the gate. This survived
  thirteen tasks of accretion **and** three new generated artifacts.
- **Core is unreachable except through published interfaces.** The compiler's, not a convention: a unit
  is its own Go module (`module github.com/gradionhq/margince/extensions/crm-demo`) outside the backend
  module, so `internal/**` cannot be imported. This is the one wall in the tier that holds against
  hostile code.

## 3. The namespace

| Surface | Form |
|---|---|
| Unit directory / Go module | `extensions/<name>/` |
| DB schema | shared `ext` |
| DB tables | `ext_<name>_<table>`, owned by `margince_owner` (see §4.3) |
| DB role | `ext_<name>` — **gate-time only**; no runtime role exists (§4.3, #628) |
| Migration namespace | `ext_<name>`, tracked in `schema_migrations_ext_<name>` |
| HTTP routes | `/ext/<name>/…` **in the contract**, served at `/v1/ext/<name>/…` |
| River job kinds | `ext_<name>_<job>` (dispatcher) + `ext_<name>_<job>_ws` (workspace child) |
| Secrets | `extension_secret.extension_name = <name>` |
| RBAC objects | `ext_<name>_<object>` |
| Frontend routes | `#/ext/<name>` (the screen is a **core** file — §4.5) |
| Manifest / `approvals.lock` key | `<name>` |

**The route namespace carries no `/v1`, and that was a bug before it was a rule.** A contract path is
relative to the document's own `servers` url, which already ends in `/v1`; core writes `/me`, so an
extension writes `/ext/<name>/…`. The server puts the base path back when it mounts
(`extension.Verb.ServedPath()`). Writing `/v1/ext/…` in the fragment published
`https://host/v1/v1/ext/…` to every generated client, which is what the first cut did. The fix is
deliberately shaped so the old spelling is a *loud refusal*: `routeGrammar` is anchored at `^/ext/`, and
`Verb.Route` holds the contract spelling verbatim so it is checkable by string equality against the
merged document's own `paths` key rather than by trusting a transform. Generator-side prepending would
have needed a second rule to notice an author who already wrote the prefix — and forgetting that rule
reproduces the bug. The old spelling is now a permanent committed test case in both `TestFragmentRefusals`
and `TestVerbValidateRefusals`.

**The manifest digests the contract spelling, not the served path** — deliberately, and for two reasons:
digesting the served path would churn every extension's descriptor on a `/v1`→`/v2` base-path bump though
no unit changed anything, re-opening every operator resolution; and one conversion function
(`declaredPattern`) serves both directions of the parity sweep, so the two cannot disagree.

**Identifier budget.** The 32-char name cap bounds a unit's share of Postgres's 63-byte limit. With
`ext_` that is `4 + 32 + 1 = 37`, leaving **26** for a table suffix (the pre-branch prose documented 28,
correct for the old `x_`). The migration slice validates every complete derived identifier, tracking
table included, and the 64-byte boundary is pinned with mutation evidence — loosening `> budget` to
`> budget+1` fails only the new test, proving the original 63/67 pair was blind to it.
**The collision is at the join, not the name:** unit `a-b` table `c` and unit `a` table `b_c` both derive
`ext_a_b_c`. `gen-composition` enforces this across the composed set; a per-unit gate cannot. Two
residuals, both caught late rather than early: the collision check collects *tables* only, so two units'
index or sequence names sharing Postgres's relation namespace collide at apply time (loudly, but during
one unit's install), and `declaredTables` is textual — measured against `ext."<ns>_it's"` it invents a
phantom table and misses the real one, which is why the per-unit catalog gate (§4.3) is the closing gate
and not the collection step.

**Digits are legal and the prefix is why.** `nameGrammar` accepts a leading digit — `1foo` is a valid
unit name — and `Namespace()` is safe anyway because a derived namespace always begins `ext_`. The
earlier reasoning (that the grammar excluded it) was wrong and is corrected in the code's own doc.
A separate latent defect surfaced with it: `dbmigrate`'s tracking-table charset was `[a-z_]` and really
would have rejected `ext_foo_1` at runtime — pre-existing and invisible only because every namespace in
the tree was digit-free.

**The `x_` → `ext_` rename landed**, touching `pkg/extension/extension.go`,
`docs/explanation/extensibility.md` and `docs/how-to/add-an-extension.md`. It did **not** touch the
fork's `x_` *column* namespace, which is live code (`overlay/provider.go`, `workspace.x_sor_mode`).
Disambiguating from that namespace is an argument for `ext_`; the ADR records it. Note that the rename
touching those doc lines was mistaken for updating them: Fable's review found both canonical docs still
describing the pre-branch tier ("not yet landed", "placeholders", "backend-only") in files this branch
had edited. Both were rewritten to the landed state, with the `//go:embed` trap as a fenced warning.

## 4. Capabilities

### 4.0 Contract-first, but extensions register rather than join

Core has three **closed, compile-time sets**: `ServerInterface` (one interface for every endpoint,
`internal/contracts/api_gen.go`), `declaredJobArgs` (a type union in `package compose`), and the
agent-policy/RBAC generated tables. None can reference an extension module — the composed workspace uses
the *same* backend tree (`emit.go:composedWork` adds `../../backend`), so only the composition module can.

**Extensions therefore register through seams; they do not enter the closed sets.**

| Surface | Mechanism |
|---|---|
| Endpoints | mounted on the unit's own router under `/v1/ext/<name>`; never in `ServerInterface` |
| Jobs | registered through a job-registration seam; not in `declaredJobArgs` |
| Tools | registered into the agent registry, as today |
| RBAC objects | registered through a published vocabulary seam into identity |

A unit still declares its surface in the same shapes core uses. **The delivered layout is simpler than
the design's:** there is no `api/api.yaml` alongside an `api/api-overlay.yaml` — there is **one overlay
document per core contract, named for the contract it extends**. `extensions/<name>/api/` is a flat set
of files drawn from `{crm.yaml, jobs.yaml, ai-tasks.yaml, public-events.yaml}`, each optional, each an
OpenAPI Overlay 1.0.0 document. The filename *is* the mapping, so no in-document `extends:` can disagree
with it, and a file naming no core contract is refused. `make gen` merges them into
`build/composition/api/`, and the merged artifacts drive **publication, client types, docs and the
manifest**. They do not regenerate core's closed sets.

**The composer evaluates a subset it can evaluate totally:** `update` actions on absolute, child-only
JSONPath targets. `remove` is deliberately not a field. `overlay:` is version-checked rather than
ignored, so a unit written against a future dialect fails here instead of composing a contract that
silently omits half of what it asked for; `info.title`/`info.version` are required because an overlay
edits a published contract and may not be anonymous; and a fragment declaring no actions is refused
rather than accepted as a no-op.

**Additive-only, stated at the right depth.** A fragment may add a node inside exactly four containers —
`components.schemas`, `paths`, `kinds`, `tasks` — and may then reach *inside* a node **only if it created
that node in this same merge**. The design's first instinct was a depth rule; that was **correctly
rejected during review**, because `$.paths.<path>` is two steps deep while
`$.components.schemas.<name>` is three, so no depth constant expresses the boundary. The container list
is strictly stronger: a target outside every container is refused outright, which closes `$.webhooks` and
a bare `$.paths` without a second rule. The premise is verified rather than assumed — `owners` can only
hold a node `addNode` successfully created, and `addNode` refuses an existing key, so "core node" and
"unit-declared node" are provably disjoint. Two overlays on one JSONPath is a build error.

**`queues` is the one container excluded on an argument rather than for want of one.** `gen-jobs`
requires every kind's `queue:` to name a `queues:` entry, so an author adding job kinds arrives at the
list immediately and deserves the reason: a River queue is a bound on the process's worker pool, shared
with core work. An extension declaring one would not be adding a capability beside a core node, it would
be allocating a share of the installation's concurrency from a directory — and composing it would drag
`compose/jobqueues.go` and the census that holds declared bounds equal to built ones in with it. So an
extension job rides a pool the installation already declared, and the job composer checks that it does.
Every *other* omission is "not composed yet, ask" rather than "never composable", and the refusal
message distinguishes the two, because the two call for different things from the reader.

**What this costs, stated plainly.** Core's strongest guarantee is a compile error: a declared operation
with no handler fails the build (`var _ crmcontracts.ServerInterface = Server{}`). Extensions cannot have
that, because they are not in the interface. They get the **runtime** equivalent instead — a parity gate
proving every declared extension verb, route and job kind has a registration, and every registration has
a declaration. Weaker than a compile error, and the ADR records it as the price of Option B. It is also
weaker than the design implies in one respect worth naming: the sweep is a **test-time** gate
(`extparity_test.go` against the live composed set), not a boot gate, and direction 2 cannot catch a
`mux.Handle` whose pattern is never appended — which is a thing the code itself does.

**Routes have three states, not two, and the design's two were not enough.** A contract-only verb — one the
fragment declares with no `Handle` — was still getting a mounted route answering an opaque **500** plus a
per-call "unhandled error" log, and the parity pair *requires* the mounting, so it structurally cannot
catch it. The states are now `404` (nothing declared), **`501`** (declared, no behavior — fired as the
handler's first statement, before the body is read or the registry reached) and served. `MountedRoute
{Pattern, Verb, Implemented}` distinguishes all three.

**Route ownership was keyed on the wrong thing, and Codex's review caught it.** Implementation was decided
by `served[v.Tool]` — the *global* tool verb — while the behavior-to-contract join correctly used
`(unit, tool)`. So unit B could publish a contract-only route reusing unit A's `x-mcp-tool` verb, be marked
implemented, and dispatch **A's handler** under B's published operation, executing A's tier, scope, RBAC and
schema. Fixed by keying the served set on `(unit, tool)`; mutation-verified.

**Where the route mounts is a security decision, and it had zero tests.** `extensionEdge` places the
extension mux nested rather than on the operational mux; mount it on the operational mux instead and every
extension route serves **unauthenticated**. Coverage went 0% → 93.8%, with the pattern-resolution assertion
judged a sound *structural* proxy rather than a weaker stand-in: `ServeMux`'s longest-match rule makes
"resolves through `/v1/`" logically equivalent to "passes through `authH.Middleware`", and the bypass
mutation proves the test discriminates.

**Extension-route error shapes are still wrong for caller mistakes**, and this is filed rather than fixed.
The route wrapper validates JSON syntax and then assumes `Invoke` errors are already product `httperr`
values; extension handlers return raw errors, so `httperr.Write` turns them into opaque 500s. Filed as
**#657**, rescoped after review from one class to three — one of which is a *legitimate runtime state* that
`ErrInvalidArgument` would misdescribe. The agent path already answers well.

**The manifest derives from the merged contracts**, not the Go AST — delivered. The pre-branch reader read
the declaration's AST, which sees only literals and never handler bodies. Contract-derived is both more
robust and more honest: what an operator approves is what the contract publishes. The switch was cheap
precisely because `approvals.lock` was, and remains, an unconsumed stub that is digested for staleness and
**never parsed** (§2.2). The AST reader survives as a blocking Go↔contract parity check rather than as the
manifest's source — with **one recorded exception**, `Secrets`, which has no contract home (§4.2).

**Descriptor digests widened accordingly, and did.** The pre-branch capability digest covered only `id`,
`operation`, `scopes`, `tier`, which would let an approval survive a path, method, schema or cadence
change. It now covers unit name, capability kind, contract source identity, operation/job/task id, route
and method where applicable, and the hash of the security-relevant contract fragment — all seven visible
per row in `extensions/crm-demo/manifest.generated.json`.

**Tier vocabulary aligned.** The contract says `auto_execute`/`confirmation_required`; the seam said
`green`/`yellow`. They unified on the contract spelling while `approvals.lock` was still a stub and no
digest was load-bearing. The boot mapping needed **no** change to absorb it: `mcpTier` switches on Go
identifiers onto a separate `RiskTier` iota, not on literals — a brief-level assumption that was wrong in
the implementation's favour.

**One refusal arrived from `main` mid-branch, not from this design:** a blank `Version` on a served tool,
because the new result envelope reports it as `schema_version` and `agents.Registry.Register` now panics
on a version-less tool. The branch had independently moved `Version` from `Tool` to `Verb`; the merge
kept `main`'s refusal verbatim in intent and message, re-sourced to `verb.Version`. So the tier's
fail-closed boot guards number **four**, not three.

### 4.1 Runtime handles arrive at invocation

`New()` is unchanged and stays inert. Capabilities needing a live handle receive one as a parameter to
their handler:

```go
type ToolHandler func(ctx context.Context, rt extension.Runtime, in json.RawMessage) (json.RawMessage, error)
type JobHandler  func(ctx context.Context, rt extension.Runtime) error
```

Both signatures shipped as written. Core constructs the `Runtime` when it invokes a handler and knows
which unit it is invoking, so a unit cannot obtain another unit's runtime; there is no re-scoping method
on the published type, and a reflection sweep pins that structurally.

**One mechanism the design did not anticipate, because the design's ordering was wrong.**
`RegisterExtensions` runs *before* the pool exists in `cmd/api`, so nothing in the registration path can
hand a handler a pool or a vault. The delivered route is `compose.BindExtensionRuntime(pool, vault)` — a
process-wide boot binding, read per call, mirroring the existing composed-tools stash, with two callers
(`cmd/api/keyvault.go`, `cmd/worker/boot.go`). This does not weaken the inert-declaration claim: what is
bound is a pool and a vault, not a `Runtime`; every unwired path reaches `errExtensionRuntimeUnwired`,
and no worker lane can invoke a governed tool without a registry. The implementer's stated premise was
itself half wrong and the reviewer corrected it: registration precedes the pool in `cmd/api` but
*follows* it in `cmd/worker` — the api lane alone justifies the binding. A later slice found the binding
**genuinely missing on the job path**: `startRunnerLane` bound behind the `AgentLoop == nil` guard, so a
model-less worker never bound while still running the job lane.

**The tenant is re-bound from the invocation context on all seven entry points**, not just `Tx`. The
first cut derived the pin from the *handler's* context; a review elevated this from Minor because it is
what makes the property structural rather than incidental. The fix (`scoped()`) went further than asked
and correctly so — the six `Secrets` verbs resolve their tenant from `ctx` too, so all seven are wrapped.
`scoped()` preserves the handler context's deadline, cancellation and values while overwriting only the
workspace key, and no cross-call staleness window exists.

**The precise claim: no core-supplied `Runtime` exists before invocation.** That is narrower than "nothing
live exists", and the difference is real. `composition.Extensions()` calls every `New()` at
`cmd/api/main.go:66`, before `RegisterExtensions` validates at `:67` — and package-level `init()` and var
initializers run at *import*, earlier still. Trusted Go can open a socket there and no static gate closes
every spelling of it.

Two gates narrow it as far as it goes, and the claim is not overstated beyond them:

- **`Handle` must be a plain function identifier** — never a call, never a selector. `nil`,
  `extension.ToolHandler(nil)` and `(nil)` stay legal as documented inert spellings
  (`unitmanifest_test.go:354`). Selectors stay banned because the AST cannot distinguish an inert `pkg.Fn`
  from a liveness-reopening `recv.Method` without type info.
- **Package-level `init()` and call-bearing var initializers are rejected** by the same generator gate.

**`Runtime`'s contract:**

- **Database access is workspace-pinned, never raw** — the `WithWorkspaceTx` idiom, not the pool.
- **Secrets** are reached through the §4.2 port, scoped to the invoking unit.
- **Lifetime is call-scoped.** Retaining `rt` past the handler's return — a package var, a spawned
  goroutine — is invalid and **fails closed** (`ErrRuntimeExpired`) rather than working by accident.
  Delivered; the documented race window in `usable()` is honest and unclosed, and unlike #627/#628 it
  carries no tracking issue.

`Runtime.Tx`/`Rows`/`Row` are stdlib-only by necessity, not by taste: `backend/pkg` is held pure by
depguard and `TestPublishedSurfaceIsPure`, so `pgx.Tx` and `ids.UserID` cannot appear there. The first
implementation attempt deferred the tx seam and the `Secrets` declaration on exactly that ground; the
human **overruled both reductions** and the stdlib-only seam shipped in the same round.

**Narrowing `Tool` to `{Name, Handle}` removes the in-process source of the boot refusals**
(confirmation-required-served, egress-served, blank-description, and `main`'s blank-version), the tier's
only fail-closed guards on served authority. `gen-composition` re-emits tier, scope, description and
version into `extensions_gen.go` as literals, so boot keeps enforcing them without file I/O — and those
literals **cannot go stale by construction**, because `composedFiles` derives the verbs and
`extensions_gen.go` from the same merged bytes in one call, and the only committed copy is the vanilla
stub held byte-equal by `stubMatchesVanilla`. Contract-vs-literal drift is caught three independent ways.

Two of the design's own interface sketches for this narrowing were **unimplementable as written and the
replacements were forced, not preferred**: `MountExtensionRoutes` cannot see a route after the narrowing
(an `Extension` reaches `Tool{Name, Handle}` and stops) and a `ServeMux` cannot be enumerated; and
`RegisterRbacObjects` cannot be *named* in `compose`, since policy is identity-internal.

### 4.2 Secrets

An `extension_secret` mapping table in the **core** migration lane:

```
extension_secret(extension_name, workspace_id, user_id NULL, key, vault_ref, created_at, updated_at)
```

The `custom` lane is the *fork's* namespace (`migrations/custom/README.md:6`, ADR-0017); this is core-owned
governance infrastructure. Named `extension_secret`, not `ext_secret`, so a unit legitimately named
`secret` cannot collide with it in the role namespace.

Ciphertext lives in `platform/keyvault` under its minted ref; the table stores only the ref. **The port
takes the unit identity from the `Runtime`, never from an argument** — concretely, `extsecrets.For(unit,
pool, vault)` closes over the unit name at the one place that knows which unit is being invoked, and every
statement carries it through `whereScope`. There is no method on the published port through which a unit
could name another unit.

The delivered table uses a **composite** foreign key rather than the design's two independent FKs, because
`TestFK_tenantLocalReferencesAreComposite` requires it — and the reviewer confirmed independently that the
composite form *does* enforce the tenant tie, so the brief's claim to the contrary was simply wrong.
The store's membership check is kept anyway, for error quality.

**Reads are audited alongside writes** (`extension.secret_read` beside `_stored`/`_rotated`/`_deleted`, in
`system_log` — a secret changing hands moves no domain row, so there is no `audit_log` entry to attach it
to). For an ordinary table that would be noise; here the question an operator asks after a unit
misbehaves is "what did it get at?", and a ledger recording only stores answers it with the one thing that
did not happen. A failed read is audited too — that gap was found in review.

Three findings worth carrying, all from the same fix round: `TestSecretsAreWorkspaceScoped` originally
passed **for an unrelated reason** (keyvault's `Ref.scopedTo`, not the store or RLS); the delete path's
namespace wall was unpinned, which silently destroys another unit's ciphertext, and the fix is
mutation-verified; and a tree-wide trap was recorded on the way — an owner connection has `BYPASSRLS`, so
**any RLS assertion made over one is vacuous, and `FORCE` does not override it.**

- **User scope is a column**, not a keyvault parameter — keyvault scopes refs to a workspace only, in the
  ref's GCM AAD, and that is not retrofittable.
- **Rotation deletes the old ref.** `keyvault.Put` is INSERT-only and orphans prior ciphertext.
- **No export path exists.** No endpoint returns a secret or any part of one — not a masked tail, which is
  still a disclosure. A unit's own Go may read its keys to use them; the published surface never emits one.
- **Secrets stay declaration-derived** — `Secrets: []extension.SecretsRequest{{Key: …, Scope: …}}` read
  from the AST — a deliberate, documented exception to §4.0's contract-derived manifest. Giving secrets a
  contract home is deferred until a real need appears; the exception is recorded in the ADR rather than
  left implicit. Note the shape: a request carries **`{Key, Scope}`**, not scope alone, and the manifest
  reader fails closed on a `Secrets` field it cannot derive rather than dropping it.
- **The port's wall is bypassable three lines below where it is documented, and #628 is why.** Every
  handler runs as `margince_app`, which holds DML on `extension_secret` itself — so via `Runtime.Tx`
  unit A can read, rewrite or delete unit B's rows and `vault_ref`s within a workspace. The port's
  carefully-verified namespace wall is real *at the port* and absent in the database. Fable's review
  named this the worst combination on the branch; it sits inside §2.0's conceded boundary, and the
  runtime docs were widened to say so rather than naming only the missing core-table wall.

### 4.3 Migrations, ownership, and RLS

> **This is the largest gap between this design and the build, and the correction is subtractive.**
> The design describes a per-unit **runtime** database role as the DDL boundary and the purge primitive.
> **No such role exists.** `cmd/migrate` opens one `margince_owner` connection with **no `SET ROLE`**, so
> extension tables are created and owned by `margince_owner` like every core table. The restricted
> `ext_<name>` role exists **only inside the pre-merge catalog gate** (`backend/tools/extmigrategate`),
> which mints it against a throwaway database. Everything below that reads as a runtime property of
> ownership is gate-time only. Consequences, stated plainly rather than left to be inferred:
> altering another unit's table does **not** fail in Postgres; `DROP OWNED BY` has nothing to bite, so
> §2.2's non-purge is structural and not an omission; and the runtime unit wall is FORCE RLS plus the
> tenant GUC, nothing else. **Issue #628** tracks minting the role, and it is the single change that
> would turn any of this from convention into enforcement.
>
> The gap was found twice independently. Codex's whole-branch review found the shipped reference
> migration *claiming* the stronger property in a comment operators would copy; Fable's found the same
> claim in the table-owner sentence. Both were corrected in place, and `0204_ext_schema.up.sql` now says
> where the role exists and where it does not — including in the in-database `COMMENT`, which an operator
> reads with `\dn+` and has no way to check against the repository.

Tables live in the shared `ext` schema, prefixed `ext_<name>_`. **Each unit is its own migration
namespace**, not part of the `custom` lane as the design says — one namespace per unit that ships a
migrations layer, each tracked in its own `schema_migrations_ext_<name>`, ordered by unit name (not a
correctness requirement, since no unit's schema may depend on another's, but two runs of one composition
must produce the same migration log). Applied by whoever runs `cmd/migrate` — `margince_owner` today.
`margince_app` gets `USAGE` on `ext` and nothing more from the core lane; the DML grants on a unit's tables
come from that unit's own migration.

**Roles do not mix across processes — a deployment invariant:**

```
app + worker processes → runtime app role (non-owner, no BYPASSRLS, no superuser)
migration process      → owner role
```

Every **extension-bearing** process is covered, not just the API: `worker-entrypoint.sh:12` sources all
vars and never scrubs `MARGINCE_OWNER_DSN`, and the worker registers extensions at `main.go:138`. The
split is resolved at real deployment; this design owns stating the invariant and making violation loud.

*Consequence to accept:* `--schema-dsn` exists so the app can run customfields runtime DDL as owner. Under
this invariant the app cannot hold it, so those two operations move behind a process that legitimately does,
or answer their generated `501` — which `WithSchemaPool`'s doc already calls the honest posture for a role
that runs no runtime DDL. **Not decided here: filed as #651**, because it is a product-posture call about a
core module rather than a tier detail, and the tier does not depend on the outcome. The pointer is recorded
in `WithSchemaPool`'s doc where the decision will land.

*Also residual, and stated rather than papered over:* the owner DSN is one member of a class —
`MARGINCE_KEYVAULT_ROOT_KEY`, blobstore keys, BYOK model keys are all one `os.Getenv` away in an
extension-bearing process. Process separation is the mechanism; §2's non-claim is the honest boundary.

**Enforcement in code: delivered.** `compose.AssertRuntimeRole` runs at `cmd/api/main.go:86` and
`cmd/worker/main.go:75`, and again as a named `runtime-role` readiness check on the worker
(`cmd/worker/observe.go:196`) — `rolsuper = false`, `rolbypassrls = false`. This is the code half of the
invariant, it covers both extension-bearing processes, and it is worth having with or without extensions.

**Migrations ship as embedded bytes, not as a path read** — `Migrations fs.FS` on the declaration, plus
`//go:embed migrations` in the unit. **The design's shape would have applied zero extension migrations in
production**, and this is the sharpest thing the build caught the design at: reading SQL from
`extensions/<name>/migrations` works in dev and CI, but `Dockerfile.api` stage 2 copies only two binaries
into an alpine image — the image has no repo. `fs.FS` rather than `embed.FS` on the frozen surface, so a
test can substitute `fstest.MapFS` and no concrete type is named. `io/fs` is stdlib, so both purity gates
hold.

⚠️ **The single most dangerous mistake available in this tier**: shipping `migrations/` without setting
the `Migrations` field. `check-ext-migrations` and the derived-identifier collision check both key off the
**on-disk directory**, while `cmd/migrate` applies out of the **embedded filesystem**. A unit that
forgot the field would pass every gate green — the SQL blessed, the catalog checked — and its table would
never be created. Nothing requires the field. Both canonical docs now carry this as a fenced warning.

**What RLS guarantees.** Extension runtime code cannot alter or disable RLS and cannot read a core table
it holds no grant on. **What it does not:** the tenant predicate is
`current_setting('app.workspace_id', true)` (`0014_rls.up.sql:33`) and `set_config` is unprivileged, so any
code holding a connection — extension or core — can re-bind it. RLS prevents *accidental* and *unscoped*
cross-tenant access, not *deliberate* re-binding. That sits inside §2's conceded boundary.

**The migration gate is positive catalog validation, applied as `ext_<name>`.** A deny-list is unclosable:
a table with no `workspace_id`, or one with `CREATE POLICY … USING (true)`, breaks tenancy while violating
nothing on a list — and nothing in the repo inspects policy predicates today.

The gate applies the unit's migrations to a throwaway database **as a minted `ext_<name>` role** —
NOSUPERUSER, NOBYPASSRLS, CREATE/USAGE on `ext` only, **no grants on `public`** (reusing the role-minting
helper at `migrationrole_integration_test.go:51`). This converts most detection problems into Postgres
refusals, and it matters because `cmd/migrate` today opens one owner connection with **zero `SET ROLE`**,
and that owner is a superuser in dev/CI — exactly where the gate runs.

**This gate is the strongest artifact on the branch** and it delivered as designed — re-verified green at
HEAD by Fable's review, and accepted crm-demo's migrations first time. One correction to the design's
grant story: **"no grants on `public`" is unachievable** alongside the required FK to `workspace(id)`, so
the minimum viable form is `GRANT REFERENCES (id) ON public.workspace`. The accepted residual is an
existence oracle — the role learns from an FK violation whether a workspace UUID exists — which is
inherent to the FK. Bonus: column-scoping the grant incidentally covers a missing `confkey` check by
privilege rather than by assertion.

It then inspects the catalog and requires, positively: every extension tenant table has
`workspace_id uuid NOT NULL REFERENCES workspace(id) ON DELETE CASCADE`; `ENABLE` **and** `FORCE` RLS;
exactly the core tenant policy shape, asserted on `polqual`/`polwithcheck`/`polpermissive`/`polroles` with
exactly one policy; ownership by `ext_<name>`; `relkind` `f`/`m`/partition children enumerated rather than
skipped; and no policy, grant, function, view, trigger or default-privilege grant outside an explicit
allowlist. Down-migrations are validated too. **DML on core relations is refused outright** — catalog
inspection cannot see a cross-tenant copy performed *during* migration.

The exact-string tenant-predicate pin **stands as a recorded decision**: it is what makes the check total
rather than enumerative, and any "the canonical predicate must be a conjunct" rule is defeated by
`(canonical AND true) OR something`. The pre-decided escape hatch, if ever needed: admit one additional
**RESTRICTIVE** policy, which can only narrow — never loosen the permissive one. A consequence for unit
authors: **a unit's policy predicate must match the pinned `NULLIF(current_setting(…), '')` form character
for character.**

Two gate defects worth remembering, both found by review rather than by tests. A **second** FK to
`public.workspace` from another column passed unexamined with `NO ACTION`, reintroducing exactly the harm
the gate's own comment names — mutation-verified, and the escape hatches (composite FK, `NOT VALID`,
deferrable timing) were each probed and closed. And the RLS plan probe's first fix **introduced a
false-fail**: an exact `Filter: ` match, reproduced live on PG16, fails for a table with an index on
`workspace_id`, which plans as an Index Scan even empty and unanalyzed — i.e. exactly the post-migration
state the gate sees, and exactly the natural thing to do under RLS. The fix forces the `Filter` rendering
with `SET LOCAL enable_indexscan/enable_bitmapscan = off` inside a rolled-back transaction.

**Namespace grammar.** `dbmigrate` namespaces were `[a-z_]` only, but unit names admit digits and hyphens —
`foo-1` was legal and unmappable. The hyphen→underscore mapping plus the join-collision rule (§3) landed;
see §3 on why the digit case was safe for a different reason than the design gave.

**One residual the design never named:** a composed binary can be `READY` against a database whose
extension migrations were never applied, publish the routes and jobs, and fail at runtime on undefined
tables (Codex finding 4). The readiness check **cannot be written today** — `margince_app` holds no grant
on `schema_migrations_ext_*`, so it would need the runtime role widened, which is a reviewed decision
rather than a fix-round line. Filed as **#658** instead of built.

### 4.4 Jobs

Extension jobs ride the existing River runner through a **registration seam**, not the closed
`declaredJobArgs` union (§4.0).

**A scheduled extension job is two kinds, not one.** `gen-jobs`' `validateCadence` forbids a cadence on a
workspace-role kind, so it is a **dispatcher** (`ext_<name>_<job>`, cadenced) plus a **workspace child**
(`ext_<name>_<job>_ws`, one per workspace). Per-job kinds keep timeout, uniqueness, cadence, census,
metrics and health all keyed on the identifier operators actually see — a single generic kind would
collapse every one of them, since `governedWorker.Timeout` ignores the job (`govern.go:26`) and
`sweepInsertOpts` is `ByState`-only (`jobs.go:33`). Delivered as `JobDeclaration.DispatcherKind()` /
`ChildKind()`, with `JobKindSuffix = "_ws"` spelled once because the generator that emits the pair and the
runner that registers it must name the same suffix. Deliberately **no** `Route`/`ServedPath`-style split
here: a kind has no base path to put back — what `api/jobs.yaml` writes is verbatim what River persists
in `river_job.kind` — so a second spelling would be a second fact that could disagree with the first.

**`queues` is not composable (§4.0), so a job rides a pool the installation already declared** and the
composer refuses an undeclared queue. The census then caught a defect nobody had hit, because **the job
census had never been run over a composed set**: the dispatcher's spec declared `opts_owner: args` while
`extJobDispatcherArgs` has no `InsertOpts()`, and it republished the unit's queue while `sweepInsertOpts()`
names none, so the row always landed on River's default and the metrics were mislabelled. Fixed to mirror
core exactly — `OptsCaller` + `river.QueueDefault` on the dispatcher, with the unit's queue bound on the
**child** via `OptsFanOut`.

**Fan-out is over all live workspaces.** Enablement is directory presence and therefore global; there is no
per-workspace enablement store, and inventing one as a side effect of this slice would design it badly.
So the dispatcher enqueues one child per live workspace — N×M rows per tick, stated plainly rather than
implied.

- **Children use the `ByArgs: true` builder** (`dispatch.go:95`), not `sweepInsertOpts` — otherwise the
  fan-out silently breaks. **The design's prediction of *how* was wrong, in the fan-out's favour:** with
  `sweepInsertOpts` the children collapse to **zero**, not one. The atomic `InsertMany` hits "ON CONFLICT
  DO UPDATE cannot affect row a second time" and the dispatcher fails on every tick forever. Verified
  against river@v0.42.0: `kind` is written independently of `ByArgs`, and when any field carries
  `river:"unique"` only tagged fields enter the args hash — so the single tag on `Workspace` is correct
  and sufficient.
- **`workspace_id` is required in child args and indexed.** `river_job` has no workspace column and no
  RLS, and both job-health statements already scan (`jobhealth.go:88`). The index over
  `args->>'workspace_id'` landed as a **Go statement in `cmd/migrate` after `jobs.Migrate`**, not as a
  migration file — River owns its own schema and its ledger, so a core migration file touching
  `river_job` would be the wrong lane.
- **The runner binds the workspace** via `WithWorkspaceTx` before the handler runs, so a handler never sees
  a global scope and multi-tenancy is the shape of the capability rather than an author's discipline.
- **Principal:** the handler persists the initiating principal reference and re-derives authority at
  execution; missing, revoked or stale authority fails closed.
- **Auto-execute only, non-egress.** A confirm-first job is a confirmation nobody can give — there is no
  caller — and a timer holding `send`/`enrich` is autonomous outbound authority. The tool surface already
  refuses both shapes at boot.
- Panic recovery, attempt cap and overlap behavior are specified explicitly. **The local recover turned
  out to buy containment as well as attribution**, which was not the reason it was written: on an
  unrecovered panic River's own executor writes `fmt.Sprintf("%v", PanicVal)` — the raw panic text —
  straight into `river_job.errors`, which is fleet-visible with **no RLS**, bypassing `jobs.FaultContext`
  entirely. A unit's panic message could otherwise carry tenant data into a table every operator reads.
- **A tick is a second path to unit code that never touches `extensionTool.Handle`, and it holds no RBAC
  object check.** Recorded as a known asymmetry rather than a gap: an object check there would be
  meaningless, because `deriveAuthority` mints an agent principal with `Scopes` only and no `Permissions`,
  so `auth.Require` would deny every tick unconditionally.
- **A fresh installation has no agent seat, and the design never priced what that means for a
  ship-enabled cadenced job (#656).** `crm-demo` ships enabled, its heartbeat ticks at 60s, and the first
  cut deliberately enqueued a child with a zero principal so the tick would "fail with a message that says
  so" — three failures a minute per workspace, forever, on every fresh install of the only product this
  repo builds. Honesty in a comment does not decide a shipping posture. Fable's review escalated it and
  the resolution is **skip-and-gauge**: `margince_extension_job_seatless_workspaces` on the worker's
  `/metrics`, absent from a process that never dispatched. Chosen over seeding a seat (a product decision)
  and over shipping the storm.

**Mixed-build posture.** A vanilla-built process scraping a database composed processes wrote would fire
`margince_job_unrecognised_kind` for every `ext_*` kind — on every rolling deploy and rollback. Delivered:
`undeclaredKindCounts` splits undeclared kinds by **namespace** (`jobs.IsExtensionKind`), not by the
composed table, because on a vanilla build that table is empty and it is precisely the vanilla process
scraping a composed database this split exists for.

### 4.5 Contract and frontend

**Contract.** A unit's endpoints are declared in `api/crm.yaml` — one overlay document named for the
contract it extends, additive-only, applied in extension-name order, with two overlays on one JSONPath a
build error. Paths carry no `/v1` (§3, §4.0). With zero fragments the composed contract stays a
**byte-copy**, and the merge is deterministic in key order.

**Generator merge-safety landed, and one of the design's three gaps was not real.** `gen-aitasks`
genuinely lacked `gen-jobs`' second-document rejection and its strict `KnownFields(true)` re-decode, and
both were ported. But the **duplicate-key premise was factually wrong**: yaml.v3 already refuses duplicate
mapping keys unconditionally (`uniqueKeys: true`), at every depth and inside custom unmarshallers, so the
test for it passed before any implementation was written — pinned as a property rather than reimplemented
as a second copy of `gen-jobs`' walker. The divergence itself was traced and is worth recording as
**accident, not design**: `gen-aitasks`' unmarshallers landed 2026-07-28 (#278), `decodeMapping` was
invented a week later in #446 which created `gen-jobs` from scratch and never propagated back. Nothing
documented a reason, and the gap was actively harmful in both arms — a pluralised `kinds:` silently left a
site at `one_shot`, changing its certification posture, and `conditionals:` left `Conditional` false so the
caller is never asked.

`make gen` now runs `gen-composition` **first**. Worth knowing that the ordering constraint was **correct
but not load-bearing** when it landed: nothing read `build/composition/api/*.yaml` until the routes slice,
which was its first consumer. It is a real gate now on the route half — renaming a fragment path makes the
composed typecheck fail.

**RBAC is composed on both sides, and that is a new seam.** `useCan` types on `RbacObject` generated from
the base contract (`capability.ts:23`, `frontend/package.json:15`), and `coreObjects` is a literal at
`policy.go:24` — inside `internal/`, which extensions cannot import. So core-side registration means a
**published vocabulary-composition seam into identity**, not a config edit. That literal is AST-pinned by
three tests and feeds the `/me` grants snapshot (`policy.go:190, 261`), so extension objects must flow
there too, or a page gates on an object the client never learns the user holds.

**One hard limit the design did not see: an extension RBAC object cannot join the contract's `RbacObject`
enum.** `$.components.schemas.RbacObject` is a **core** node, so §4.0's ownership rule forbids reaching
inside it and no container addition fixes that — which is correct, because additive-only is the property
the ownership rule buys, and teaching the composer an enum-append action would spend it. The runtime side
was always fine (`/me`'s object map is string-keyed, proven against marshalled JSON), but the client aliased
`RbacObject` from the schema, so `useCan("ext_crm_demo_note", "read")` would not typecheck while the data
sat in the response. The fix is a **client-side widening** at `capability.ts` and the four hook signatures,
probed empirically with a four-assertion `@ts-expect-error` file: a misspelled core object, a bare string,
and a core typo inside a `GrantSpec` are all still refused. `schema.d.ts` needed nothing.

**A related core-login hazard the design never contemplated, found by the acceptance run (F4): removing a
unit bricked login for every user.** `policy.Parse` refused any role document naming an unknown object, and
a removed unit's grants stay in `role.permissions` (§2.2) — so a removal took out identity resolution for
everyone holding the grant. Parse now **drops** an unknown object with a warning instead of refusing the
document, which is the right asymmetry (deny-by-default; `row_scope` stays fatal), and it is read-only, so a
returning unit's grants revive. Two residuals recorded rather than fixed: the warning fires on every login
of every affected user forever with no cleanup path, and with no `/roles` endpoint the "typo refused at the
write path" that Parse's own doc promises has no write path to live at — so a typo'd grant in the only
mechanism that exists (hand SQL) now no-ops silently instead of failing loudly.

> **SUPERSEDED by §4.6.** The ruling below stands as the record of what this slice decided and why —
> it was correct for a slice that had no answer to the supply-chain question. §4.6 takes that question
> head-on and answers it, so `extensions/<name>/frontend/` becomes a real capability layer and
> `unbuiltCapabilityLayers` empties. Read this section for the reasoning that held until it did; read
> §4.6 for what replaces it, including the costs §4.6 accepts that this section declined to.

**Frontend: `defineExtension` was not built, and that is a recorded ruling, not an oversight.**
`extensions/<name>/frontend/` is **still refused on sight** by `gen-composition`'s scan — it is the one
remaining member of `unbuiltCapabilityLayers`, and `scanUnit`'s "no Go module" refusal therefore never
relaxed either. Lifting it means bundling unit-authored TSX into the SPA bundle: a supply-chain decision
with no per-unit isolation, no CSP story, and an unanswered question about how the DS-purity, biome and
coverage gates apply to code the core team did not write. Two agents reached that ruling independently.
What shipped instead:

- **A contract-derived descriptor registry.** `gen-composition` emits
  `{name, verbs: [{operationId, route, method, title, version, rbacObject}]}` read out of the **merged
  contracts** — the same source the routes, the agent tools and the manifests derive from, so a screen
  cannot advertise an operation the server does not serve. `App.tsx` falls through to a generic
  published-operations card for any composed unit without a bespoke screen (`de`, `yogi`, `crm-hello`).
- **crm-demo's screen lives in the core tree** (`frontend/src/screens/ext/crmdemo.tsx`), dispatched by unit
  name once the descriptor resolves. This was forced, not preferred: a generic screen over that descriptor
  shape cannot express "not connected"→paste key→"connected", HMAC signing, a note list with Add/Delete, an
  unprompted heartbeat row, or hiding Add on a read-only seat — **five of the acceptance's eight steps.**
- **Consequence: removing a unit is a removal in two places** (§2.2). Unavoidable while the screen is a
  core file, and it fails loudly (`fe-typecheck-composed`) rather than silently.
- **`#/ext/<name>` is not reachable from the nav.** `NAV_GROUPS` is a canonical 10-item list whose order is
  pinned by test, so a composed unit is reachable only by typing the hash. Correct for this slice: no unit
  has a surface worth a rail slot, and deciding where a variable number of installation-defined entries sit
  in a fixed list is its own design question.
- **`crm-demo` therefore exercises five of six tier surfaces from inside the unit**, not six. Any claim
  otherwise — including one made in a commit message on this branch — is wrong.

**Types mirror the `GOWORK` two-lane pattern.** A committed **vanilla** `schema.d.ts` remains the empty-tree
output and keeps its drift gate; composed artifacts are selected by tsconfig alias. This preserves the
byte-identity property a single committed-from-composed file would destroy. There are **three** aliases, not
one: `@composition/extensions` (the descriptor registry), `@composition/schema` (the merged contract's
types) and `@composition/screens` (the screen registry). The middle one needed a **second** composition root,
`build/composition-frontend/` — gitignored, Node-produced, and verified to sit outside the tree
`verifyOutputs`/`verifyNoExtraFiles` operate on, so the byte-identity gate is untouched by it. One
deliberate typecheck gap is accepted and named: the demo screen's own test file, because a test imports the
screen and `tsc` follows imports past exclusions.

**Cost, accepted explicitly, and it bit.** The CI `frontend` job has no Go toolchain and `Dockerfile.web`
copies only the base YAMLs. The first cut made `check-fe` depend on a composed typecheck that hard-exits
without `node_modules` while the job never ran `pnpm install` — **the frontend job failed on every run**,
and the local green run never exercised that path. Its paths filter also omitted `extensions/**`,
`composition/**` and `gen-composition/**`, so a PR changing the only input that alters the composed registry
did not run the frontend job at all. Both fixed; the general lesson holds, that a lane which skips
composition must fail loudly rather than typecheck against a stale contract.

### 4.6 Frontend, the sixth surface — a unit ships its own package

> **Status: designed, not yet built.** Unlike every section above it, this one was written *before* its
> code rather than reconciled against it, so read it as intent. It is reconciled in place when the
> slice lands, and until then §4.5's "as built" statements elsewhere in this document — §2.2's removal
> cost, §4's surface table, §5's refusal row — remain the accurate description of the tree.

§4.5 declined to bundle unit-authored TSX because it had no answer to the supply-chain question. This
section answers it: **a unit's frontend is a pnpm workspace package**, with its own `package.json` and
its own dependencies, resolved and built by the same toolchain that builds the SPA. That is the
deliberate choice between three shapes, and the other two are recorded because the reasoning matters
more than the outcome:

- **Source-only** — unit TSX compiled against core's dependencies, no unit `package.json`. Smallest
  change, no supply-chain surface, but a unit can never bring a library, and the tier's stated purpose
  is a bounded add-on somebody else writes.
- **Workspace package (chosen).** A unit brings its own dependencies. The cost is stated below rather
  than discovered.
- **Runtime loading** (module federation, a per-unit bundle fetched at run time). Rejected: it would
  trade away the property this tier's frontend already has and the backend cannot offer — a screen for a
  unit whose contract fragment did not merge **fails `tsc`**, because its routes are not in the merged
  contract's `paths`. That compile-time route guarantee is worth more than the isolation federation
  would buy, and federation's isolation is weak anyway (one origin, one bundle, one `localStorage`).

**What the mechanism is, and how little of it is new.** The two-lane alias pattern §4.5 built already
carries three artifacts; `@composition/screens` is the only one still hand-written in core. It becomes
generated like the other two, and the layer leaves `unbuiltCapabilityLayers` exactly as `migrations/`
did. Vite already parameterises `server.fs.allow` for a root outside `frontend/`. So the new parts are:
a workspace that spans `extensions/*/frontend`, a generated screen registry, and the gates below.

**The published frontend surface is `frontend/package.json`'s `exports` map**, and that is the precise
analogue of `//margince:extension-surface` over `backend/pkg/**`. A unit imports `@margince/frontend/…`
and nothing else of the core's; a deep import, a relative escape into `../../frontend/src`, or an
unmarked path is refused by a gate, because unlike Go there is no module boundary doing it for free.

**React is a peer dependency, deduped.** Two React instances in one bundle break hooks at run time with
an error that names nothing useful, so `react`/`react-dom` are `peerDependencies` of a unit package and
`resolve.dedupe` pins one copy. This is the single most likely way a unit author breaks the SPA, and it
is configuration rather than review.

**Costs this section accepts, having named them.** They are real, and none is mitigated by anything in
this design:

1. **A unit's transitive npm dependencies ship in the SPA bundle**, on the same origin, with the same
   `localStorage` and the same session as the core. There is no per-unit sandbox in a bundle, and this
   design does not build one. The composed set was already the trust boundary on the backend (§2.4);
   this extends the same posture to a place where the blast radius is larger and the review surface —
   a dependency tree nobody on the core team wrote — is wider. A unit is added deliberately, and that
   remains the whole of the protection.
2. **The lockfile is upstream-owned and a unit writes to it.** Adding a unit with dependencies changes
   the root `pnpm-lock.yaml`, so "a unit edits no upstream file" — true of the backend — is **false**
   for a frontend-bearing unit. Stated here rather than left for someone to discover in a diff.
3. **CSP is unchanged and unhelped.** Same bundle, same origin, built at build time: nothing about this
   makes a unit's code more constrained at run time than core's is.

**What it buys, concretely.** Removal becomes **one place** — `git rm -r extensions/<name>` — closing
the two-place wart §2.2 records, which the acceptance run found and which three documents currently
have to warn about. And the sixth surface finally comes from inside the unit, so `crm-demo` exercises
six of six rather than five.

**The digest collision, and its resolution.** `digestTree` refuses every non-regular file under a unit,
deliberately (§5) — and pnpm gives each workspace package a `node_modules` of symlinks, so the two
collide head-on the moment a unit has a dependency. `node_modules` is excluded from the digest by name,
alongside the manifest that is already excluded, and for the same class of reason: it is resolved
output, not unit source, and what pins it is the lockfile. The symlink refusal itself is unchanged
everywhere else.

## 5. Gates

Each slice deletes its refusal in `gen-composition/scan.go` as it lands — except `frontend`, which is
still refused (§4.5), so `scanUnit`'s "no Go module" refusal never relaxed either.
`unbuiltCapabilityLayers` is one shared list driving both `scanUnit`'s refusal and
`refuseNonRootGoPackages`' walk exemption, so each lift was a one-line edit.

| Gate | Outcome |
|---|---|
| `make check-composition` | ✅ byte-identity extended to newly real artifacts, with explicit empty-tree tests |
| `coreDigest` | ✅ and better than designed — it iterates `composedContractBases`, the same var the emitter iterates, so a fifth base extends both **by construction**. Demonstrated both ways: before the fix, editing `backend/api/jobs.yaml` gave `-verify-inputs` exit 0 while full `-verify` failed on the output hash |
| `composedFiles` / `verifyNoExtraFiles` | ✅ — and the design's premise was wrong: `verifyNoExtraFiles` has **no list**, it derives the expected set from the regenerated outputs, so every new artifact joined by construction. What was missing was any *test* of the gate and any record of the two-root boundary |
| `extensions_arch_test.go` | ✅ `cmd/migrate` in the composition wiring allowlist |
| `make migrate` | ✅ `GOWORK_COMPOSED` — and **five** GOWORK holes existed, not one. The last was `scripts/dev.sh`'s bare `go run ./cmd/migrate`, so `make dev` migrated from the vanilla stub and then built a composed api against it five lines later. All five found (37 hits triaged), all five closed, with the ordering fixed so `gen-composition` precedes `migrate` |
| `astreader.go` / `unitmanifest.go` | ✅ manifest derives from merged contracts; AST reader is a blocking Go↔contract parity check; digests widened (§4.0) |
| runtime parity gates | ✅ with the three-state and residual caveats in §4.0 |
| `agenttoolparity_test.go` | ✅ extension verbs excluded from the core compile-time gate; construction updated for the narrowed `Tool` |
| `gen-aitasks` | ✅ second-document + strict-unknown-field rejection ported. Duplicate-key was **already held by yaml.v3** — pinned as a property, not reimplemented (§4.5) |
| jobs gate family | ✅ `jobkindgate`, `jobcensus`, `jobtimeoutwiring`, `jobregistrationban` admit registered extension kinds. `jobtimeoutwiring` needed no change — composed kinds go through `addComposedWorker`, outside its walk by necessity |
| RBAC AST-pin tests | ✅ `coreObjects` composable; plus a **new** enforcement test family (`extrbac`, `extrbacenforce`, `extcomposedrbac`) |
| runtime-role assertion | ✅ boot on both processes + worker `/readyz` (§4.3) |
| `testdb/reset.go` | ✅ reset list covers `public` **and** `ext` |

**Gates that exist because the build found something the design never named:**

| Gate | Why it exists |
|---|---|
| `.gitignore` fitness test (`backend/extensionsignored_test.go`) | **Every gate was green while `extensions/crm-demo/` was invisible to git.** `.gitignore` carries a per-unit un-ignore list; composition, migrate, the catalog gate and `check-q` all read the working tree. The how-to already warned about this in prose and it was still missed — which is exactly why it is now a test, using `--no-index` (without it a tracked file is never reported) |
| `check-ext-migrations` in the CI **`integration`** job | Not `deterministic-gates`. That job is hermetic and container-free; arming it would make the repo's fastest merge gate start a compose stack on every backend PR from the first migration-bearing unit onward. Gate locality costs once; the compose start costs forever. The gate also migrates its **own** throwaway database rather than assuming a clean clone, since a composed template now holds `ext.ext_<name>_*` |
| core-lane migration-role fitness test | `make check` does not run the integration lane, so **a core migration needing a privilege the restricted role lacks passes every gate a task author runs.** The `ext` schema migration was the first that could hit it, and did — it broke four migration tests on `permission denied for database`, because `CREATE SCHEMA` needs database-level `CREATE`. Now a class guard, running the full lane as a restricted non-owner role with two vacuity guards |
| `go vet -tags integration ./...` in the `vet` target | **`make check` never compiled the integration lane** — build, vet, lint and test all ran untagged, while `internal/compose` alone holds hundreds of `//go:build integration` files. Any new untagged test in such a package could collide with a tagged helper and the whole merge gate stayed green. DB-free type-check of the tagged lane |
| `fe-typecheck-composed` in `make check-fe` | The composed frontend lane, with three explicit missing-artifact assertions (§4.5) so a skipped composition fails loudly instead of typechecking against the committed contract |
| `Verb.Validate`'s required-RBAC-object rule | §2.1 — refused at *declaration*, so a contract-only verb is covered too |
| `extroutes_conformance_test.go` | Drives a mounted route and checks the response body against the declared 200 schema. Exists because the acceptance run found **every read the screen performed returned `undefined`** (F1): the REST envelope did not match what the contract published |
| `extrouteownership_test.go`, `extroutes_edge_test.go` | The `(unit, tool)` keying and the auth-edge placement (§4.0) |
| job census over a **composed** set | It had never been run over one, and failed the first time (§4.4) |
| `stubMatchesVanilla` on `extensions_gen.go` | Keeps the re-emitted governance literals from going stale by construction (§4.1) |

`digestTree` refuses symlinks, so a `node_modules` symlink farm under `extensions/<name>/frontend/` hard-
fails generation — dependencies stay out of the unit tree.

**Two gate-hygiene notes worth keeping.** `gen-composition`'s `scanExtensions` reads only
`<root>/extensions`, never `fixtures/extensions` — so fixture manifests are held by their own
byte-equality test, not by `make composition`. And to prove "this fix round changed only comments", diff
against the fix's **immediate parent**, not an earlier ancestor: a wider span picks up unrelated commits
and falsely shows whole files as new.

## 6. The demo unit

`extensions/crm-demo` ships enabled and is the concrete consumer for the surfaces above, satisfying
principle #7. `fixtures/extensions/crm-hello` is untouched — it stays the minimal CI fixture. Detail and the
click-through acceptance are in `CRM-DEMO-SCOPE.md`, with the corrections below taking precedence over it.

**Five of six surfaces come from inside the unit.** `migrations/` (`ext.ext_crm_demo_note`, workspace-scoped
under forced RLS), `api/` (six governed operations under `/ext/crm-demo/`), secrets (an HMAC signing key,
proven **by use** — signing is the whole demonstration, and no operation returns the key, masked or
otherwise), a job (a heartbeat tick that names its own workspace, so the dispatcher's fan-out is visible
rather than silently demonstrating the single-tenant case), and tools (the same six operations reaching the
agent). **The sixth — the screen — is a core file** (§4.5).

**`GET` and `DELETE` are not declarable, so the three record operations are three POSTs on three paths.**
`Verb.validateMethod` admits `post`/`put`/`patch` only, and that is the seam's rule rather than a style
choice: a served extension operation *is* a governed tool invocation and its arguments are the request
body. `CRM-DEMO-SCOPE.md`'s `GET`/`POST`/`DELETE` line is wrong.

**The unit declares two RBAC objects, not one.** `ext_crm_demo_note` gates the record operations;
`ext_crm_demo_signing_key` gates the secrets operations — and the second exists because the acceptance
re-run demonstrated its absence (§2.1, R1). It gates on **`update`** for the store, not `create`: there is
one key per workspace, the slot always exists, and `create`-but-not-`update` still hands out the first
overwrite. Two agents reached that independently. Note also that the SPA's own gating was **not** in place
before that round: the claim that the screen had gated on `ext_crm_demo_signing_key` earlier is false —
`git log -S` finds the string nowhere before it — and the declaration and the client gate landed together.

**No default role seeds either object**, so every seat sees the not-granted state until an admin grants
them by raw SQL (there is no `/roles` endpoint). The screen says so in words rather than showing an empty
list, because the superseded compile-time guard can no longer distinguish "no grant" from "the overlay did
not merge". Any acceptance of the read-only-seat step must grant both objects first or it means nothing.

**One scope item was deliberately not built:** the intentionally-failing tick. The containment it would
demonstrate already exists and is tested core-side, and a shipped-enabled failure switch on a first-party
unit is the wrong trade.

**Two other units ship enabled** and are worth knowing about as the *cheap* end of the tier: `de` (a
jurisdiction pack, no tier surface of its own) and `yogi` (one read-only agent tool declared entirely in a
routes-only fragment — `paths` alone sufficed, since its schemas are inline on the operation). Both render
the generic descriptor card, which is what makes that fallback a tested path rather than a theory.

## 7. Delivery

| PR | Contents |
|---|---|
| **0** | The ADR, on `margince-foundation`. Nothing merges ahead of it. |
| **1a** | `Runtime` type + contract; the `Handle`-identifier and no-`init()` gates; secrets (`extension_secret` core migration, port, `SecretsRequest`); tier-vocabulary alignment; the `x_`→`ext_` rename; the runtime-role boot assertion. |
| **1b** | Migrations slice: `ext` schema, namespace mapping + join-collision rule, role and grant topology, the apply-as-`ext_<name>` catalog gate, `cmd/migrate` required **and** `make migrate` given `GOWORK_COMPOSED`. |
| **1c** | Contract + frontend: overlay merge, `gen-aitasks` merge-safety, two-phase `make gen`, the RBAC vocabulary seam (plus `/me` snapshot and AST-pin tests), contract-derived manifest + widened digests, `Tool` narrowing with literals re-emitted, endpoint router seam, vanilla `schema.d.ts` + composed types + tsconfig alias, Go stage in CI and `Dockerfile.web`. The largest PR. |
| **1d** | Jobs: dispatcher + workspace-child kinds via the registration seam, `ByArgs` children, the `args->>'workspace_id'` index, principal re-derivation, panic and overlap bounds, mixed-build gauge posture. |
| **2** | `extensions/zalo-personal`. |

**What actually shipped: one PR, not four.** The whole tier landed on
`feat/extension-tier-capabilities` as PR
[#659](https://github.com/gradionhq/margince-poc-v1/pull/659) — fifteen task slices internally, staged A–E
with a verified-green `make check-q` at each stage boundary, but a single reviewable branch. The 1a–1d split
above is useful only as a reading order for §4. **PR 2 (`zalo-personal`) was not built.**

**PR 0 did not land first.** The ADR is foundation PR #1128, still open at the time of writing; the human
holds it and reviews it. The recorded resolution is that if #1128 is still open when PR1 is green and
reviewed, PR1 merges ahead of the ADR and the report says so plainly. See §8 — the ADR also needs
renumbering before it can merge at all.

Per-slice acceptance was phrased against what each slice unlocked, and the full demo screen was not
reachable until the jobs slice.

## 8. The ADR

**The number problem is real and unresolved.** Verified against `margince-foundation`'s `origin/main`:
**ADR-0069** is taken (`ADR-0069-configured-embed-width-and-deployment-reindex.md`), and so are
**A115** (embed width), **A116** (outbound webhook payloads) and **A117** (overlay→native cutover) — all
unrelated, all ratified. The extensions ADR still sits at 0069 on the unpushed
`spec/extensions-adr-0069` branch and **cannot merge there.** Free at the time of writing: **ADR-0076**
(a genuine gap in the sequence) or anything from **ADR-0093** up; DECISIONS codes from **A143** up.

**Amendment 1 is written and committed but unpushed** — `aea6df95`, "ADR-0069 Amendment 1 — reconcile with
the tier build", on that same local branch. It already carries the substance of this reconciliation
(§A1.1–§A1.9: the `ext_` token, the route spelling, the contract-derived manifest, the not-delivered
approvals resolution, the unbuilt frontend, and the runtime-role gap as its largest item), so the ADR work
is not the problem — the number is. Note this document is a **working design document**, not a ratified
record: corrections belong in place here, and amendment conventions belong to the ADR.

It must record: the `ext_` namespace token, carrying ADR-0017's ownership-signal pointer forward; §2's
guarantees and non-claims **in the honest tense** — repeating "nothing granted silently" in the present is
false, and the accurate statement is `compose/extensiontools.go`'s TRUST MODEL paragraph; **Option B** —
registration seams over closed-set membership, and the compile-error→runtime-parity downgrade that buys;
the secrets exception to contract-derived manifests; the role-separation deployment invariant; and the
`ext` schema. ⚠️ **It must not record per-extension runtime roles, three privilege lanes, or `DROP OWNED BY`
purge as delivered** — none of the three exists (§4.3). Nor was any provenance-stamped audit append in the
delivered design; secret access is audited in `system_log` (§4.2), which is a different thing.

## 9. Open

Nothing blocking the tier's own machinery. What is open is filed, not deferred silently:

| Issue | What |
|---|---|
| **#627** | `Runtime.Tx` bypasses the overlay-mode datasource routing. Debt, with an honest comment at the point of risk |
| **#628** | No per-unit runtime database role — the containment wall is RLS plus the tenant GUC, not grants. **The umbrella issue for §4.3's gap, §2.2's non-purge, and §4.2's bypassable port wall**, and the single change that would move any of them from convention to enforcement |
| **#651** | `customfields`' two runtime-DDL operations need a `--schema-dsn` posture under the role-separation invariant. Shipped as a filed issue rather than code, by explicit decision: it is a product-posture call about a **core** module, and the tier does not depend on the outcome (`AssertRuntimeRole` is independent and already shipped). The pointer is recorded in `WithSchemaPool`'s doc |
| **#656** | No product path creates an agent seat, so no extension scheduled job can run on a fresh installation. Mitigated to skip-and-gauge (§4.4); the seat itself is a product decision |
| **#657** | An extension route answers 500 on malformed arguments **and** on legitimate runtime states; the surface needs a small set of published refusal classes. Rescoped after review from one class to three (§4.0) |
| **#658** | A composed binary is READY against a database whose extension migrations were never applied. Unwritable today without widening the runtime role (§4.3) |
| **#670** | `sbom` has been red on `main` since #605 — `jackc/pgerrcode`'s PostgreSQL licence is not on the compliance allowlist. **Not this branch's**, but it will show red on this PR |

Closed by the build and no longer open questions: the two-phase `make gen` ordering, the `GOWORK_COMPOSED`
holes (five, all closed), the tier-vocabulary alignment, the `x_`→`ext_` rename, the descriptor-digest
widening, and the route base-path spelling.

Beyond these, roughly twenty deferred **minors** are recorded in the ledger with their reasoning —
independent test, doc and file-length items. Fable's whole-branch review triaged them as a set and found no
compounding beyond the #656 × ship-enabled combination already resolved above.
