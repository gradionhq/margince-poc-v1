# Extensibility — the extension tier

How a bounded add-on lands in this product **without editing a single upstream-owned file**. This is
the *extension tier*: one named, versioned unit under `extensions/<name>/`, its own Go module,
reaching the core through one narrow published surface and composed in at build time. The vanilla tree
already ships three — `extensions/de`, the German jurisdiction pack; `extensions/yogi`, the reference
unit that serves one governed agent tool; and `extensions/crm-demo`, the reference extension that
exercises every capability the tier has (its own table under RLS, six governed operations, its own
RBAC object, a stored signing key it signs with and never emits, and a scheduled heartbeat).

This page is for a contributor who wants the whole idea first, then the detail. Start here; to
actually *build* a unit, jump to [how-to/add-an-extension.md](../how-to/add-an-extension.md).

## The whole idea in one picture

```text
extensions/<name>/                 one Go module per unit — its PRESENCE is the enablement
   │  New() extension.Extension     an inert declaration: plain data, no handle into the core
   ▼
make composition ─▶ build/composition/     generated wiring (ignored); an empty extensions/
   │                                        tree reproduces the committed stub byte-for-byte
   │  Extensions() []extension.Extension
   ▼
GOWORK=build/composition/go.work   ONE import path, two implementations on disk; the
   │                                workspace decides which one the compiler links
   ▼
cmd/{api,worker} main ─▶ compose.RegisterExtensions(set)
   │                              │
   │                   ① validate the WHOLE set   ─▶  ② apply → core registries
   │                      (bad unit → boot aborts)      (nothing applies until all valid)
   ▼
core consumes the capability        e.g. the retention engine reads a jurisdiction pack's floors
```

Read top to bottom, that is the entire lifecycle: a unit *declares* a value, the generator *composes*
the enabled set, the build *binds* one composition module, each role binary *reconciles* it into the
core at boot, and the core *consumes* it. Everything below is detail on one of those five steps.

## Purpose — why a whole tier for this

Some behaviour is real but doesn't belong in everyone's build: a country's statutory retention rules,
a customer-specific add-on. The tier exists so that behaviour can be **added, versioned, and reasoned
about as its own unit** without forking the core or touching upstream files — and without giving up
the guarantee that the composed product is wired exactly like the core.

**The rejected alternative names the design.** The obvious way to ship this is a runtime plugin — a
`.so` loaded at startup, an RPC sidecar, a hook registry a unit mutates in `init()`. All three are
refused for one reason: they introduce an authority the compiler cannot check — a dynamically loaded
or separately deployed unit whose reach into the product nothing in the build proves. An extension
here is a **compile-time unit** instead. Its module path sits *outside* the backend module, so the
compiler itself makes `internal/**` unreachable — the unit *cannot* import into the core even if it
tries — and fitness tests hold the rest of the surface. This is an **import/API boundary, not a
sandbox**: a unit's `New()` and its capability methods still run as ordinary trusted Go in the role
process. Paying for extensibility with a rebuild is the trade: a composed product is provably wired the
same way the core is, or the build fails.

## Principles

Four rules hold the whole tier together:

1. **Presence is enablement.** A unit is enabled because a directory for it exists under
   `extensions/`. No flag, no config list, no registry file to append to. The enabled set is a *fact
   about the tree*, not a value someone can forget to update.
2. **A declaration is inert data.** `New()` returns a plain value holding no handle into the running
   server. It registers nothing itself; only the boot reconciliation, after the whole set validated,
   applies anything. Nothing is wired *through the declaration*, so a unit cannot reach the core or
   another unit that way — an import/API boundary, not a runtime sandbox.
3. **Grow additively, never in place.** Capabilities are *fields* on the declaration. A new kind of
   capability is a new field; a changed contract is a versioned successor, never an edited signature.
   Existing units keep compiling untouched.
4. **One narrow backend surface, and it's enforced.** A unit reaches the backend *only* through the
   marker-allowlisted `backend/pkg/**` packages; the gate also rejects the composition module and
   sibling units. Its other dependencies (stdlib, third-party) are its own business. Fitness tests,
   not good intentions, hold this.

## The parts

Five moving pieces, matching the five steps in the picture.

### 1. The declaration — what a unit exports

A unit exports one function returning one value:

```go
func New() extension.Extension {
	return extension.Extension{
		Name:          "de",
		Version:       "1.0.0",
		Jurisdictions: []jurisdiction.Pack{pack{}},
	}
}
```

That value is the entire contract. `Name` is the canonical unit name (it must equal the directory
name, obeys `^[a-z0-9]+(-[a-z0-9]+)*$`, ≤32 chars) and keys the unit's namespace everywhere it will
touch — `ext_<name>_<table>` tables, `/v1/ext/<name>/` paths, the `ext_<name>` database role. `Version` is
recorded in the boot inventory and carries no authority. **Capabilities are the remaining fields** —
`Jurisdictions` (passive policy) and `Tools` (the first *governed* kind: a risk-tier request in the
manifest, and — when the declaration carries a handler — a tool the agent surface actually serves,
see §5).

### 2. The published surface — and the marker that gates it

A unit may import only `backend/pkg/**`, and only the packages that opt in. Two exist today:

- **`pkg/extension`** — the declaration types (`Extension`, and the self-validating `Name`/`Version`).
- **`pkg/extension/jurisdiction`** — the jurisdiction-pack contract (`Pack`, `Retention`,
  `RetentionClass`, the closed class/anchor vocabularies, the calendar `Period`).

Membership in `backend/pkg` **grants nothing on its own**. A package is extension surface only when
its package clause carries the directive `//margince:extension-surface`. The allowlist is *derived
from the tree* — a fitness test walks `pkg/`, collects every marked package, and treats exactly that
set as importable — so the published API can never drift from what the gate enforces. The constrained
**primitive types** on the surface self-validate (`Name.Validate`, `Period.Validate`,
`RetentionClassName.Validate`, …) with the same checks boot reconciliation runs, so an author who
tests against them catches a malformed field at test time. There is no aggregate `Extension.Validate`,
though: whole-declaration and cross-unit checks (a duplicate name, a code a core pack already holds)
still first run at boot.

### 3. The composition build — `gen-composition`

The core never imports an extension module. Instead `tools/gen-composition` (run by
`make composition`, on which every build and test lane depends) scans `extensions/` and materializes
`build/composition/` — the one ignored root for installation-dependent output: the composed `go.work`,
a **composition Go module** whose generated `Extensions()` returns the enabled set, and a manifest
binding input digests to reproducible output hashes. With an *empty* `extensions/` tree the generator
reproduces the committed `composition/` stub **byte-identically**, so a bare `go build` and a composed
build provably wire the same thing. `make check-composition` is the drift gate that proves it.

The generator also derives each unit's **`manifest.generated.json`** next to the unit:
its identity and the **risk tiers** it requests — every operation the extension adds that runs
at a 🟢/🟡 tier or asks for a scope, the things an operator must resolve under §7 — read STATICALLY
from the declaration's AST, so review tooling and the coming approval flow learn what a unit needs
without compiling or executing its code. The first governed kind is the **agent tool**
(`extension.Tool`: a verb, a requested tier, one requested scope); a tool declaration derives into one
risk-tier request carrying the §5 security descriptor (id, operation, scopes, tier) and its
digest. Declaring a tool records the request in the manifest; whether it is also *served* depends on
the declaration carrying a handler (§5). Passive policy
that an extension merely supplies requests no risk tier and does not appear: a jurisdiction pack exposes no
governed operation (the core consults its policy at boot — it is never an agent that acts) and asks
for no tier, so a jurisdiction-only unit (like `de`) carries an empty risk-tiers list — there is
nothing to approve. The returned `extension.Extension` literal and the fields the manifest derives
(`Name`, `Version`, `Tools`) must be literal values; an unrecognized field fails generation with its
position rather than producing a manifest that silently omits a request. The manifest is committed
with the unit and drift-gated like the contract; its digest rides in `composition.json` per unit.

### 4. The build-time binding — which `composition` module the compiler links

The generator writes the composed wiring, but nothing yet says the *binary* should use it. That is a
separate step, and it is worth understanding on its own: the composition module exists **twice on
disk under one import path**.

| | Module path | `Extensions()` returns |
|---|---|---|
| `composition/` — committed vanilla stub | `github.com/gradionhq/margince/composition` | `nil` |
| `build/composition/backend/` — generated, git-ignored | `github.com/gradionhq/margince/composition` | the enabled set |

Core code carries one unconditional `composition.Extensions()` call — no build tags, no `if enabled`.
**Which function body that call links to is decided by the active Go workspace**, through two
resolution mechanisms whose precedence is the whole trick:

- **`replace` in `backend/go.mod`** points the import path at `../composition`, the stub. It is
  committed, so it is what a bare `go build` — or `gopls` in an editor — resolves. That is deliberate:
  tooling always sees vanilla.
- **`use` in the generated `build/composition/go.work`** lists the generated module's directory. A
  `use` entry does not substitute one path for another; it declares that directory as the local source
  for whatever module path its own `go.mod` names. **A workspace member outranks a member module's
  `replace`**, so inside this workspace the generated module wins and the `replace` is never reached.

Hence the switch in `backend/Makefile`, which every build, test, and run lane carries:

```make
COMPOSITION_DIR := $(abspath ../build/composition)
GOWORK_COMPOSED := GOWORK=$(COMPOSITION_DIR)/go.work
```

Two consequences worth internalizing. First, `gen-composition` itself runs under the **root**
`go.work` instead: it lives in the separate `backend/tools` module and must resolve *before* the
composition exists, so it cannot depend on a workspace that its own output creates. Second, a lane
that forgets to export `GOWORK` does not fail — it silently produces a **vanilla** binary that boots
fine and looks identical. The byte-identity gate is what makes that safe rather than alarming for the
empty set, but for a non-empty `extensions/` tree it is a real failure mode: check `GOWORK` before
concluding an extension "did not load".

The confusing part when reading the tree is that the generated module sits in a directory named
`backend/` while declaring the `composition` module path. Go only ever reads the `module` line; the
directory name is a sibling-of-outputs convention (`build/composition/` also holds `api/` and
`frontend/`), never a Go-meaningful name.

### 5. The boot reconciliation — validate the set, then apply

Each role binary wires the composed set at exactly one place — its `main.go`:

```go
extensions := composition.Extensions()
if err := compose.RegisterExtensions(extensions); err != nil { … }
```

`RegisterExtensions` runs **two separated phases**, and the separation is the invariant. First it
*validates the whole set* — every name, version, and capability, checked against both the declared set
and the live core registries (a duplicate name, a jurisdiction code a core pack already holds, a
retention class outside the closed vocabulary — any of these aborts the boot *before anything
applies*). Only once the set is known good does it *apply* — registering each capability into its core
registry. Why two phases: register-as-you-go could fail halfway and leave a half-composed server.
Validate-then-apply makes "partially registered extension" a state the system cannot reach.

## What an extension can do today — and how that grows

**Today: two capability kinds.** A **jurisdiction pack** supplies *country-specific policy the core
consults; it is never an actor* — it exposes no governed operation, so it never appears in the unit
manifest. An **agent tool** (`extension.Tool`) is the first *governed* kind: it derives a risk-tier
request into `manifest.generated.json` for operator resolution (§7), and a tool declaring a `Handle`
**is served** — `buildExtensionTools` adapts it to the core `mcp.Tool` seam and boot registers it into
the same `agents.Registry`, admission gate, and tool listing the core tools ride (`extensions/yogi` is
the reference unit that exercises that path end to end). Two limits hold there: a handler-less
declaration stays a manifest request and serves nothing, and a *served* 🟡 confirm-first tool is
refused at boot rather than registered, because this data-only adapter cannot implement the registry's
staging seam — its approvals could never be staged, so the capability would be dead on every call. A
served tool spending an outbound `send`/`enrich` cap is refused for the same reason read from the other
end: it could only ever run 🟢, and every core verb that leaves the workspace is confirm-first, so
serving one would be outbound authority with nobody to ask. That binds the DECLARATION — a handler is
ordinary Go and could reach the network whatever cap it asks for; what the refusal buys is that a unit
cannot ASK for outbound authority and be granted it silently. And
until per-capability operator resolution lands, **the composed set is itself the trust boundary**: a
handler-bearing tool is served at its declared tier because someone added the unit to the tree
deliberately.

`Title` is the one field on the declaration that grants nothing: it is what `tools/list` DISPLAYS in
place of the verb. Optional, and kept out of the manifest's governance descriptor and its digest — but
validated at gen time all the same, because the core registry refuses a blank display name and would
otherwise do it by panicking the boot that composed the unit.

The core stays country-neutral — a fitness gate
(`check-no-jurisdiction.sh`) scans hand-written core source for jurisdiction-specific identifiers and
fails the build on a match. Germany does not live in the core; it lives in `extensions/de`, which
declares the GoBD/AO statutory **retention floors** — commercial correspondence 6 years, and
accounting vouchers (*Buchungsbelege* — §147 AO's 8-year class as amended 2025; the 10-year
books-and-records class is deliberately absent, since a CRM holds no such record) — each anchored at
calendar-year end, because §147(4) AO counts every period from the end of the record's calendar year.
The engine treats a floor as a *minimum*: a workspace may keep longer, never destroy earlier. Today
only the correspondence floor actually binds a record; the accounting-vouchers class is declared but
**inert**, because no in-product invoice yet derives into it. The seam re-exports the pack types as
aliases, so the core retention engine consults the *same* constants an extension declares.

**How new capabilities arrive.** A new capability kind is a new *field* on `extension.Extension` and a
new marked `pkg/**` package holding its contract — existing units keep compiling. The unit name is
validated to the full identifier budget, so a name chosen today stays valid for every surface.

**What a unit can own today.** All of the following have landed:

- **Its own tables** — `ext.ext_<name>_*`, from a `migrations/` directory of `NNNN_name.up.sql`/`.down.sql`
  pairs the unit embeds and `cmd/migrate` applies as its own namespace, tracked in
  `schema_migrations_ext_<name>`. Every unit table must carry FORCE row level security and a
  workspace-bound policy; `make check-ext-migrations` applies the unit's migrations as a *minted
  restricted role* against a throwaway database and re-reads the catalog to prove it. At runtime there is
  no such role — `cmd/migrate` runs one owner connection with no `SET ROLE`, so isolation rests on the
  RLS, not on ownership (#628).
- **Its own HTTP surface** — `/v1/ext/<name>/…`, declared as operations in an `api/` contract fragment
  that is merged into the composed `crm.yaml`. `build/composition/api/crm.yaml` is a real merge now, not
  a byte-copy of the core contract.
- **Its own governed tools** — an `x-mcp-tool` verb on a declared operation, served through the same
  admission gate a core tool passes, at the tier and scope the contract declares.
- **Its own scheduled jobs** — declared in a `jobs.yaml` fragment, dispatched as a fleet fan-out with a
  workspace child per live tenant.
- **Its own secret namespace** — reached through `Runtime.Secrets()`, keyed by the unit's own bare names.
- **Its own RBAC objects** — `ext_<name>_*`, registered into the vocabulary `/me` serves.

**What a unit still cannot own: a frontend.** The generator continues to **refuse** a unit that ships a
`frontend/` directory (`gen-composition/scan.go`). A unit gets a route and a generic descriptor card for
free; a *bespoke* screen for a unit is written in the core tree today, which means removing a unit is a
**two-place** operation — delete the unit directory *and* its core screen and registry entry in the same
commit, or `fe-typecheck-composed` fails. Lifting the refusal is a supply-chain decision (it would make
a unit's TSX part of the SPA bundle) and was deliberately kept out of the tier's first landing.
`frontend/extensions.gen.ts` is still emitted and is still a constant no `frontend/src` module imports.

One ordering constraint remains worth knowing: the SPA gates affordances with `useCan(object, action)`
over an `RbacObject` **generated from `crm.yaml`'s enums**, so a page gated on an extension-owned RBAC
object needs the contract overlay — which is why the API surface landed before any frontend work can.

**What the tier does NOT protect against, stated here because the surface reads like a boundary.** Units
are **reviewed, first-party or otherwise trusted code**, compiled into the same process. Every wall
described in this document is defence in depth against *mistakes* — it makes the accidental
cross-tenant query or the forgotten scope a loud failure — and none of it is a sandbox against a unit
that is trying. In-process Go can read the keyvault root key from the environment; `Runtime.Tx` can
rebind the `app.workspace_id` GUC the RLS policies key on; and every handler runs as the shared
`margince_app` role, which holds DML on core tables, on every *other* unit's tables, and on
`extension_secret`. Issue #628 (a per-unit database role) is the one change that would move any of this
from convention to enforcement, and even then the in-process reach remains. `backend/pkg/extension/runtime.go`
carries the same statement at the seam itself.

## The guardrails — held from the tree

The tier is defended by fitness tests and scripts, so the guarantees can't rot into stale prose:

| Guarantee | Held by |
|---|---|
| A unit imports only the marker-allowlisted `pkg/**` surface — never `internal/**`, `cmd/**`, an unmarked `pkg` package, the composition module, or a sibling unit | `backend/extensions_arch_test.go` |
| The surface marker exists only under `pkg/` — no silent allowlist widening elsewhere | `backend/extensions_arch_test.go` |
| The composed set is wired only at the role `main.go`s, and each required role actually wires it | `backend/extensions_arch_test.go` |
| Vanilla composition reproduces the committed stub byte-for-byte | `make check-composition` |
| The published surface doesn't break compatibility (advisory before the first release tag, enforcing after) | `scripts/check-pkg-freeze.sh` |
| The core stays jurisdiction-neutral | `scripts/check-no-jurisdiction.sh` |
| Every unit table carries FORCE RLS and a workspace-bound policy, and touches nothing in `public` | `make check-ext-migrations` (applies each unit's migrations as a minted restricted role) |
| Every unit table grants the runtime role exactly `SELECT, INSERT, UPDATE, DELETE` — a table granting *nothing* satisfied the old one-sided allowlist and then answered `permission denied` at the first call | `make check-ext-migrations` |
| A unit's shipped `migrations/` is actually embedded and applied — the directory and the field are two facts, and the gates read different ones | `backend/tools/gen-composition` (the `Migrations` field must name a var whose `//go:embed` covers the layer) |
| The runtime pool is not the migration owner: no superuser, no BYPASSRLS, and no ownership of the `ext` schema *or* anything in it | `compose.AssertRuntimeRole`, at boot and on `/readyz` |
| A declaration the composer cannot honour is refused rather than discarded — an unknown job role, governance declared on the wrong half of a pair, a `$ref` in an advertised schema, a multi-document base contract | `backend/tools/gen-composition` |
| Every declared extension operation is mounted, and every mounted route was declared | `backend/internal/compose/extparity_test.go` |
| A unit's served tool is dispatched only by that unit's own route — one unit cannot inherit another's handler by naming its verb | `backend/internal/compose/extparity_test.go` |
| A unit's SCREEN reaches the core only through the published surface (`frontend/package.json`'s `exports`), and npm only through what its own package declares | `frontend/scripts/check-ext-imports.sh`, itself tested by `check-ext-imports.test.sh` |
| A unit's screen is held to the same design system as core — tokens, icons, spacing, no native dropdown | the four `frontend/scripts/check-*.sh` gates, which sweep `extensions/*/frontend` as well as `frontend/src` |
| A unit cannot ship a second copy of state the host owns (React's hook dispatcher, react-query's QueryClient) | `gen-composition` refuses them as direct dependencies; `resolve.dedupe` catches a transitive one |
| A unit's copy is namespaced to that unit and cannot rewrite a core string | `gen-composition` (`mergeUnitLocales`), and core keys win the lookup |

The compiler does the heaviest lifting for free (an extension's module path is outside the backend
module, so `internal/**` is unreachable by construction); the tests hold the rest of the contract that
the compiler alone wouldn't catch, and every extension source dir is enrolled the moment it exists —
including the CI fixtures under `fixtures/extensions/` (`crm-hello`, the smallest unit that exercises
the whole path).

## Reference

### Where the code lives

| | |
|---|---|
| The declaration type (`Extension`, `Name`, `Version`) | `backend/pkg/extension/extension.go` |
| The jurisdiction-pack contract | `backend/pkg/extension/jurisdiction/jurisdiction.go` |
| The core-internal jurisdiction registry (aliases the published types) | `backend/internal/shared/ports/jurisdiction/jurisdiction.go` |
| Boot reconciliation (validate-then-apply) | `backend/internal/compose/extensions.go` |
| Served-tool adaptation into the core MCP seam | `backend/internal/compose/extensiontools.go` |
| Role-main wiring | `backend/cmd/{api,worker}/main.go` |
| The composition generator | `backend/tools/gen-composition/` |
| The committed vanilla stub (and its `replace` in `backend/go.mod`) | `composition/extensions_gen.go` |
| The `GOWORK` switch every build lane carries | `backend/Makefile` (`GOWORK_COMPOSED`) |
| The first-party German pack | `extensions/de/de.go` |
| The reference served-tool unit | `extensions/yogi/yogi.go` |
| The reference extension (every capability) | `extensions/crm-demo/crmdemo.go` |
| Its screen, which lives in CORE and not in the unit | `frontend/src/screens/ext/crmdemo.tsx` |
| The reference fixture | `fixtures/extensions/crm-hello/crmhello.go` |
| The negative migration fixtures | `fixtures/extensions/bad-unprefixed-table/`, `bad-overbudget-table/` |
| The secrets namespace-wall fixture | `fixtures/extensions/crm-nosy/crmnosy.go` |
| The extension-tier fitness tests | `backend/extensions_arch_test.go` |

### Related docs

- [how-to/add-an-extension.md](../how-to/add-an-extension.md) — build and ship a unit, step by step.
- [privacy-and-consent.md](privacy-and-consent.md) — the retention engine that consumes a pack.
- [composition-layer.md](composition-layer.md) — how `compose` boots and wires the composed set.
- [agent-surface.md](agent-surface.md) — the registry and admission gate a served extension tool joins.
- [frontend-architecture.md](frontend-architecture.md) — the SPA an extension frontend slice would extend.
- [reference/make-targets.md](../reference/make-targets.md) — `composition`, `check-composition`, `test-extensions`.
