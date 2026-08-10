# Add an extension (a stable-tier unit)

For shipping a bounded add-on — a **jurisdiction pack**, a **governed agent tool**, an **HTTP surface**,
its **own tables**, its **own secrets** or a **scheduled job** — as a named, versioned unit under
`extensions/<name>/`, without editing any upstream-owned file. For *why* the seam is a compile-time
declaration and what the surface guarantees, read
[explanation/extensibility.md](../explanation/extensibility.md) first. For a country pack
specifically, the live capability is retention floors; the running example below builds one.

An extension is its own Go module reaching the core through only the marker-allowlisted
`backend/pkg/**` surface. **Presence under `extensions/` is the enablement** — there is no flag to
flip. `extensions/crm-demo` is the **reference unit** and exercises every capability below — copy it when your
unit owns data or serves routes. `extensions/de` (a jurisdiction pack), `extensions/yogi` (one served
agent tool) and `fixtures/extensions/crm-hello` (the walking-skeleton) are the smaller shapes.

A unit owns **all six** surfaces, frontend included: `extensions/<name>/frontend/` is a pnpm workspace
package whose default export the SPA mounts at `#/ext/<name>`. A unit that ships none still gets a
route and a generic descriptor card automatically.

Extension paths — the units, the `backend/pkg/**` seam, the composition stub and generator — carry
a [CODEOWNERS](../../.github/CODEOWNERS) entry, so a PR touching them automatically requests the
tier owner's review.

## Scaffold the unit

1. **Create the module directory** `extensions/<name>/` — the directory name is the canonical unit
   name and must match the `Name` you declare. It obeys the grammar `^[a-z0-9]+(-[a-z0-9]+)*$`,
   ≤32 chars (lower-case segments joined by single hyphens); the name keys SQL identifiers and URL
   paths, so anything else is refused at boot.

2. **Add its `go.mod`** — its own module, path `github.com/gradionhq/margince/extensions/<name>`:
   ```text
   module github.com/gradionhq/margince/extensions/<name>

   go 1.26.5
   ```

3. **Write the declaration** `extensions/<name>/<name>.go`, starting with the BUSL SPDX header (every
   hand-written `*.go` file carries it). Export `New() extension.Extension` returning an **inert
   value** — no handle into the core, nothing registered in an `init()`. When the name is hyphenated,
   only the Go **package identifier** drops the hyphen: `crm-hello` uses `package crmhello`, but its
   directory, its module path, and `Extension.Name` all keep the hyphen — a hyphen is illegal in a Go
   identifier, not in a module path:
   ```go
   // SPDX-License-Identifier: BUSL-1.1
   // SPDX-FileCopyrightText: 2026 Gradion

   package fr

   import (
   	"github.com/gradionhq/margince/backend/pkg/extension"
   	"github.com/gradionhq/margince/backend/pkg/extension/jurisdiction"
   )

   func New() extension.Extension {
   	return extension.Extension{
   		Name:          "fr",
   		Version:       "1.0.0",
   		Jurisdictions: []jurisdiction.Pack{pack{}},
   	}
   }

   type pack struct{}

   func (pack) Code() jurisdiction.Code { return "fr" }

   func (pack) Retention() jurisdiction.Retention { return retention{} }

   type retention struct{}

   func (retention) Classes() []jurisdiction.RetentionClass {
   	// Illustrative values only — a real pack's statutory floors and anchors
   	// must be legally verified (French correspondance commerciale ≈ 5 years,
   	// not the German figure).
   	return []jurisdiction.RetentionClass{
   		{Name: jurisdiction.CommercialCorrespondence, Keep: jurisdiction.Period{Years: 5}, Anchor: jurisdiction.AnchorOccurrence},
   	}
   }
   ```
   **Import only `backend/pkg/**` packages carrying `//margince:extension-surface`** — `pkg/extension`
   and `pkg/extension/jurisdiction` today. Any import of `internal/**`, `cmd/**`, an unmarked `pkg`
   package, the composition module, or a sibling extension fails the arch test (the compiler already
   makes `internal/**` unreachable — the test holds the rest).

## Stay inside the declared vocabularies

A jurisdiction pack supplies **policy, never behaviour** — the core retention engine consults it. So
the values you declare must be ones a core engine already understands:

- **`Code`** is a lower-case ISO 3166-1 alpha-2 code, unique across the composed set. A code the `de`
  pack (or any other enabled unit) already holds aborts the boot.
- **`RetentionClassName`** comes from the **closed set** — `commercial_correspondence`,
  `accounting_records`. You supply a *floor* for a known class; you do not invent a class (adding a
  new class kind is a deferred capability that hasn't landed yet). A name outside the set is refused.
- **`Period`** is a calendar span (`{Years: 6}`), never a day count, and every component is
  non-negative — a floor reaches *back*, never forward. Implausibly long spans are refused too
  (`Period.Validate` caps a component at ~1000 years), so a typo can't anchor a cutoff in the far past.
- **`Anchor`** is `occurrence` (the zero value) or `calendar_year_end`. Pick `calendar_year_end` only
  when the statute counts from the year's end (as German §147(4) AO does).

Get the statutory content right — it's legal content, not a default. Pin it with a test (below).

## Declare a governed agent tool (optional)

A unit may also contribute **agent tools** — named verbs the MCP surface serves alongside the core
ones. `extensions/yogi` is the first-party worked example; copy its shape:

**Governance lives in the contract, not in Go.** An `extension.Tool` is a **verb and a function** and
nothing else — the tier, the Passport scope, the RBAC object, the title, the prose, the version and both
schemas all come from the contract operation that declares the verb (see the next section):

```go
Tools: []extension.Tool{{
	Name:   "yogi_quote", // lower snake_case; must equal an x-mcp-tool verb in THIS unit's api/ fragment
	Handle: quote,        // omit for a contract-only request: declared, published, answers 501
}}
```

The handler signature carries the capability handle:

```go
func quote(ctx context.Context, rt extension.Runtime, in json.RawMessage) (json.RawMessage, error)
```

`rt` is the **only** thing the core hands a unit, it is minted per invocation, and it is invalid the
moment the handler returns (`extension.ErrRuntimeExpired`). Today it offers `rt.Secrets()` and `rt.Tx()`.

What the surface will and will not serve:

- **`Handle` decides whether the tool runs.** Omit it and the declaration is a manifest request and
  nothing more — the route is still mounted and still published, and it answers a named **501**. Supply
  it and the tool is registered at boot into the same registry and admission gate the core tools ride,
  so its tier and scope are enforced on every call. The verb must be declared by **your own** unit's
  contract fragment: naming another unit's served verb does not borrow its handler, it gets you a 501.
- **A served tool is 🟢 only.** `TierConfirmationRequired` is refused for a handler-bearing tool: this
  surface cannot stage an approval, so a confirm-first extension tool would be refused on every call.
- **A served tool may not DECLARE an outbound cap.** `ScopeSend` and `ScopeEnrich` are refused for a
  handler-bearing tool, because outbound work is confirm-first everywhere else in the product and a
  🟢 outbound verb would reach a destination nobody approved. This binds the declaration, not the
  handler: a handler is ordinary Go and could open a socket regardless, which is why the composed set
  is itself the trust boundary (see
  [explanation/extensibility.md](../explanation/extensibility.md)) and a unit is added deliberately.
- **`Title` is optional but not free-form-blank.** A whitespace-only or space-framed title is refused
  at generation; a unit that declares none is listed under its verb. Declared as `x-mcp-tool.title`.
- **`RequestedScope` is required.** The vocabulary is the closed passport set (`read`, `draft`,
  `write`, `send`, `enrich`); a **served** tool may request only `read`, `draft` or `write`, since the
  two outbound caps are refused above. It is the cap a caller's passport must hold, so declare the one
  the act actually spends.

**Validate arguments yourself — the declared input schema is client-facing documentation, and nothing
on this seam checks a request body against it before your handler runs.** Copy
`extensions/crm-demo/notes.go`'s `decode`; do not write your own from
`Decoder.DisallowUnknownFields` alone, which this guide used to recommend and which leaves four holes
that each let a document the published schema forbids decide what your handler stores:

| What encoding/json does | What the contract says |
|---|---|
| matches field names **case-insensitively**, so `BODY` sets `Body` | `additionalProperties: false` |
| accepts a **repeated** member and keeps the last | one member, once |
| accepts `null` and leaves the struct zeroed | an object is required |
| decodes **one value and stops**, discarding the rest | one document |

Two of those decide *which value* a mutation writes, past a reviewer reading the first one. And
validate anything the database will cast: an id declared as a bare string reaches PostgreSQL's `::uuid`
and answers 500, so declare the shape (`format: uuid` plus a pattern) **and** check it before the
transaction. Count characters with `utf8.RuneCountInString`, never `len` — JSON Schema's `maxLength`
counts characters, so a byte count refuses text in any non-ASCII script at a length the schema you
published says will fit.

Note the known gap: a handler cannot yet return a *classified* caller-error, so a refusal surfaces as a
500 on the REST route — tracked as **#657**. That is the reason to refuse in your own code, where the
message is at least yours, rather than letting the database do it.

## Publish an HTTP surface and its governed tools

An operation is declared in a **contract fragment** under `extensions/<name>/api/`. The **filename names
the core contract it extends** — `api/crm.yaml` extends `backend/api/crm.yaml`, `api/jobs.yaml` extends
the job contract. `gen-composition` merges them into `build/composition/api/`, and the merged document is
what the operator manifest, the generated client types, the mounted routes and the docs all read.

Copy `extensions/crm-demo/api/crm.yaml`. The rules that will otherwise bite:

- **Paths are relative to the document's own `servers` url**, which already ends in `/v1`. Write
  `/ext/<name>/notes/list`, never `/v1/ext/...` — the server puts the base path back when it mounts the
  route, and spelling it twice publishes `/v1/v1/ext/...` to every generated client (the composer refuses
  it).
- **Every path must sit under `/ext/<your-unit>/`.** Another unit's namespace, a core path, or a path
  template (`{id}`) are all refused.
- **POST/PUT/PATCH only.** A served extension operation *is* a governed tool invocation and its arguments
  are the request body, so GET and DELETE — which carry none — are refused. "list", "add" and "remove"
  are three POSTs on three paths.
- **`x-mcp-tool` is where governance lives**: `verb`, `version`, `title`, `tier`, `scope`, `description`.
  The `verb` must equal the `Name` of one of your unit's `Tools` entries for the operation to be served;
  `description` is required (it is the text a model selects the tool by) and so is `version`.
- **`x-rbac-object` / `x-rbac-action`** declare the object grant the caller must hold. The object is
  registered into the RBAC vocabulary `/me` serves and must be named `ext_<name>_*`. Declare both or
  neither.
- **Schemas are inline — no `$ref`, at any depth.** The composer does not resolve references, and the
  request/response schemas it reads are emitted verbatim as the MCP tool's input and output schemas: a
  client has no document to resolve a reference against, so an unresolved one would be advertised to a
  model as the argument shape. A property *named* `$ref`, and a `$ref` inside `example`, `default`,
  `const` or `enum`, are instance data rather than references and are fine.
- **The 200 body is your own schema.** The agent path wraps results in a governed envelope; the REST
  route unwraps it, so what a client receives is exactly what your `responses.200` declares. Do not
  declare the envelope — the registry wraps your schema for the agent surface too, so declaring it
  would describe the wrapper to a model as if it were the answer.
- **A fragment adds nodes; it never redefines one.** Two units may not target one JSONPath, a target
  must land under `$.paths`, `$.components.schemas`, `$.kinds` or `$.tasks`, and the node added
  directly under one of those must be a **mapping** — a scalar at `$.paths['/ext/u/thing']` publishes a
  path item that is a string. A YAML alias anywhere in an `update` is refused: it resolves inside your
  fragment, and the merged document has no anchor to match it.

## Own tables — `migrations/`

Ship `extensions/<name>/migrations/NNNN_name.up.sql` and a matching `.down.sql`, then **embed them**:

```go
//go:embed migrations
var migrations embed.FS

func New() extension.Extension {
	return extension.Extension{
		Name:       "<name>",
		Version:    "1.0.0",
		Migrations: migrations, // ← WITHOUT THIS LINE THE SQL NEVER RUNS
	}
}
```

> ### ⚠️ The field is what runs — and the generator now holds you to it
>
> The directory and the field are **two different facts**. `make check-ext-migrations` and the
> identifier-collision check read the **on-disk directory**; `cmd/migrate` applies the **embedded
> filesystem**. A unit that shipped the SQL and forgot the field used to have its SQL validated,
> blessed and reported as correct while an empty FS was applied — `make check` green, the migrate step
> saying "schema is at head", and the table never created. It failed at its first query, in
> production, with an undefined-table error. It happened once on this tier.
>
> **`gen-composition` refuses that now**, in three shapes:
>
> - a unit that ships `migrations/` and declares no `Migrations` field;
> - a `Migrations` field that does not name a package-level var;
> - a var whose `//go:embed` directive does not cover `migrations/` — including
>   `//go:embedmigrations`, which is missing the separator Go requires and so is an ordinary comment
>   leaving the FS **empty**, and including a directive pointed at some other layer.
>
> What it does **not** prove is that the bytes reaching `cmd/migrate` are the bytes the gate applied:
> an embed may cover more than `migrations/`, and an `fs.FS` assembled at run time is beyond a static
> reader. So still add the `//go:embed` line and the `Migrations:` field **in the same commit**, and
> confirm with `make migrate` + `\dt ext.*` that your table exists.

What the SQL must do, enforced by `make check-ext-migrations` (which applies your migrations as a minted
restricted role against a throwaway database and re-reads the catalog):

- Create tables only in the `ext` schema, named `ext_<name>_<table>` — the schema is shared by every
  installed unit, so the prefix is what keeps two of them apart.
- Carry `workspace_id uuid NOT NULL REFERENCES workspace(id) ON DELETE CASCADE`.
- `ENABLE` **and** `FORCE ROW LEVEL SECURITY`, with exactly one permissive policy keyed on
  `current_setting('app.workspace_id', true)` in both `USING` and `WITH CHECK`. `FORCE` is not optional:
  the runtime owner is `margince_owner`, and `ENABLE` alone exempts a table's owner from its own policies.
- `GRANT SELECT, INSERT, UPDATE, DELETE ... TO margince_app` — **exactly those four**, on every tenant
  table. Not more: `TRUNCATE` empties every workspace's rows without consulting the policy's `USING`
  clause, and `REFERENCES` and `TRIGGER` are refused too. Not fewer, and not none: the gate used to ask
  only "nothing outside the list", which granting *nothing* satisfies perfectly — and the table then
  answers `permission denied` at the first handler call, having passed every check.
- Touch nothing in `public`. The one core dependency a unit may take is the `workspace(id)` foreign key
  above; the gate grants `REFERENCES` on that column alone and refuses a role that can point anywhere
  else, because a foreign key onto a core table takes a lock on core writes and can refuse a core
  delete forever after.

**A new migration is a new file — including for an index.** `dbmigrate` keys on the version, so a line
added to an already-applied `0001` runs on exactly the installations that did not need it (a fresh one)
and never on the ones that do. `extensions/crm-demo/migrations/0003_note_workspace_index.up.sql` is the
worked example: the index it adds belongs to the table `0001` created, and it is still its own file.

**Index the tenant column.** A unit's table is cross-tenant, so every read the policy admits still has
to find this workspace's rows among every other workspace's — and, less obviously, deleting one
workspace sequentially scans the whole table once per row it removes unless an index *begins* with the
referencing column. `(workspace_id, <your list order>)` covers both.

## Own secrets

Declare what you will use, then reach it through the Runtime:

```go
Secrets: []extension.SecretsRequest{{Key: "signing", Scope: extension.SecretScopeWorkspace}},
```

```go
key, err := rt.Secrets().Get(ctx, "signing") // errors.Is(err, extension.ErrSecretNotFound) when absent
```

Declaring grants and stores nothing — it is a request recorded in the manifest. Keys are your unit's own
bare names, namespaced for you; there is no method that takes another unit's name.

## Own a screen — `frontend/`

Ship `extensions/<name>/frontend/package.json` and the module it names. The package is a workspace
member, so it may bring its own dependencies; `pnpm install` from the repo root links it.

```json
{
  "name": "@margince-ext/<name>",
  "private": true,
  "type": "module",
  "main": "screen.tsx",
  "peerDependencies": {
    "@margince/frontend": "workspace:*",
    "@tanstack/react-query": "^5.101.4",
    "react": "^19.2.0"
  }
}
```

Four rules, each refused at generation because each fails somewhere worse otherwise:

- **`@margince-ext/<name>`, matching the directory.** One workspace holds every enabled unit, so a
  shared name is two members claiming one identity and pnpm resolves whichever it saw last.
- **`private: true`.** A workspace member that is not private is one `pnpm publish -r` from a registry.
- **`main` names a module that exists**, and its **default export** is the screen.
- **React, react-dom and `@tanstack/react-query` are PEERS, never dependencies.** Each keeps state the
  host owns — React's hook dispatcher, react-query's QueryClient context — and a second copy is a
  second, empty one. This is the rule that fails at *run time* if you get it wrong: hooks throw with a
  message naming neither the unit nor the cause, or the first `useQuery` reports no QueryClient on a
  page that plainly has one.

**Import the core only through `@margince/frontend/<subpath>`** — `design-system`, `api`, `app`, as
published by `frontend/package.json`'s `exports` map. That map is this side's
`//margince:extension-surface`: the Go tier gets its boundary from the compiler, a bundler gives none,
so `frontend/scripts/check-ext-imports.sh` is the boundary. It refuses a relative path escaping your
unit, an unpublished subpath, and any bare specifier your own `package.json` does not declare —
`devDependencies` count for test files only, so a screen cannot pull a test runner into the bundle.

The four design-system gates (`ds-purity`, `icon-lint`, `ds-spacing`, `native-controls`) sweep your
unit exactly as they sweep core.

**Ship your copy with your screen.** Put one flat JSON object per locale in
`frontend/i18n/<locale>.json`, keyed `ext<CamelUnit>.` — `extCrmDemo.notes.add`. The composer merges
them into the one catalogue, so `useT()` resolves your keys and core's through the same lookup. Supply
**every** locale the installation ships (en, de, vi) or generation refuses: a reader of the missing one
gets a blank screen. Keys outside your namespace are refused too — a unit does not rewrite core copy.

## Own scheduled jobs

Declare **two kinds** in `api/jobs.yaml`: a cadenced `dispatcher` that fans out over the live fleet, and a
`workspace` child (`<dispatcher>_ws`) that does one tenant's work. A single kind that both ticks and
carries a tenant is refused — it has no honest answer for whose data the tick touched. Use
`queue: default`; `queues` is not a container a fragment may extend.

Which half declares what is a rule, not a convention, and the composer refuses the other spellings:

- **`role` is `dispatcher` or `workspace`, exactly.** A mistyped third value used to match neither
  arm of the pairing and quietly *un-declare* the kind: no registration, no manifest entry, no error.
- **Governance is the dispatcher's.** `tier` and `scope` go on the dispatcher and nowhere else — the
  pair resolves as one governed job, so a second copy on the child is a line an author writes, a
  reviewer reads, an operator resolves against, and the runtime never applies.
- **`cadence` is the dispatcher's; `max_attempts` is the child's.** A cadence on a workspace kind and
  an attempt cap on a dispatcher are both refused; a dispatcher's retry *is* its next tick.
- **Both halves share one queue**, and the child's kind is the dispatcher's name plus `_ws` — a
  workspace kind no dispatcher fans out to is a worker no clock ever reaches.

```go
Jobs: []extension.Job{{Name: "heartbeat", Handle: heartbeat}},
```

A job handler takes `(ctx, rt)` and no arguments — a tick has no caller. It cannot be confirm-first and
it cannot request an outbound scope; both are refused at boot.

> **Know before you ship a cadence:** nothing in the product creates an agent seat yet (**#656**), and a
> tick needs one to name its initiator. A workspace with no agent seat is **skipped**, and the count is
> reported as `margince_extension_job_seatless_workspaces` on the worker's `/metrics`. So on a fresh
> installation your job will not run at all until an operator inserts a seat — pick a cadence that is
> honest about that, and do not treat a silent job as a broken one without checking the gauge.

## Write the unit's own test

Each unit is its own Go module, so the backend's `./...` never reaches it — it carries its own tests,
run by `make test-extensions` on the composed workspace. Its Go files sit under the same
craftsmanship and license-header gates as `backend/` — `make craft-static` sweeps `extensions/`, and
the pre-push hook checks the extension files a push changes. Pin the statutory content so a changed span
or class name is a deliberate, reviewed edit (copy the shape from `extensions/de/de_test.go`):

```go
func TestNewDeclaresTheFloors(t *testing.T) {
	e := New()
	if e.Name != "fr" {
		t.Fatalf("Name = %q, want fr", e.Name)
	}
	// … assert the pack code, class names, and calendar spans.
}
```

A test with no assertion is noise (T11) — assert the actual floors, not just that `New()` returns.

## Compose and verify

Because presence is enablement, the moment the directory exists it's in the enabled set — you only
have to regenerate the composition and run the gates:

1. **`make composition`** — regenerates `build/composition/` from `extensions/`; your unit now appears
   in the generated `Extensions()`, and a `manifest.generated.json` lands next to your unit — the
   statically derived record of the **risk tiers** it requests (the 🟢/🟡 operations and scopes an
   operator must approve under §7; a jurisdiction-only unit requests none, so its list is empty).
   Commit it with the unit; the drift gate fails a stale or hand-edited one. Derivation reads your
   `New()` from the AST, so the returned `extension.Extension` literal and the fields it derives
   (`Name`, `Version`, `Tools`) must be literal values or the published `extension` constants
   (`extension.TierAutoExecute`, `extension.ScopeRead`, …) — a computed value, or a field the
   generator does not recognize, fails generation with the file and line rather than silently
   dropping a request. (Every build/test lane depends on this target, so `make check` runs it for
   you; run it directly when you want to inspect the output.)
2. **`make check`** — builds the composed workspace, runs the extension-tier fitness tests
   (import-boundary, marker placement, composition wiring), `make test-extensions` (your unit's own
   tests), and `make check-composition` (a clean regeneration must reproduce `composition.json`
   byte-for-byte).
3. **Boot a role** — `make dev`, then confirm the boot doesn't abort: a duplicate code, an unknown
   class, or a bad period is caught in `RegisterExtensions`' validate phase *before* any surface
   serves, and names the offending unit.

   `make dev` runs the **composed** stack on both sides: it materializes `build/composition/`, builds
   the api and worker against the composed `GOWORK`, and starts Vite with
   `MARGINCE_COMPOSITION_FRONTEND` pointing at the composed frontend registry — so a unit's routes,
   its agent tools *and* `#/ext/<name>` are all live on the one port `make dev` prints. (It did not
   set that variable until Task 14's UAT found the gap: only `Dockerfile.web` did, so the SPA
   resolved the empty-tree registry and every unit route answered "no extension named …" while the
   api served it perfectly.)

   A **scheduled job** needs one more thing that no product path creates yet: the workspace's agent
   seat. Without it every tick fails at the authority derivation — see
   [margince-poc-v1#656](https://github.com/gradionhq/margince-poc-v1/issues/656), which carries the
   reproduction and the one-statement workaround.

Push only once `make check` is **green** — not red, not still running. The vanilla stub check keeps
passing because it's keyed on the *empty* `extensions/` tree; your unit only changes the composed
output, never the committed `composition/` stub.

## Ship it

**A new unit's directory is gitignored.** `.gitignore` ignores `/extensions/*` except an explicit
allowlist (`!/extensions/de`, …), so a first-party unit you mean to ship in the vanilla tree **must
add its own exception** — `!/extensions/<name>` — or the PR opens with no extension files, and files
you add to the unit later are silently ignored too. (`git add -f` stages the files once but leaves the
directory ignored, so it is not a substitute for the exception.) A purely local, per-installation unit
is *meant* to stay ignored: its presence in the working tree already enables it for that install.

**Removing a unit is a removal in ONE place** — the unit's own directory. Use `git rm`, not `mv` or
`rm`: `make drift` compares the working tree against the INDEX, so an unstaged deletion of the
committed `manifest.generated.json` fails the gate on a removal that is otherwise correct.

Then commit **the complete unit directory** — every source and test file plus its module metadata
(`go.mod`, and `go.sum` if it carries third-party dependencies) — together with the `.gitignore`
exception. Do **not** commit `build/composition/` — it is generated and ignored — and leave the
tracked `composition/` stub unchanged unless you are deliberately changing the vanilla baseline. Sign
off every commit (`git commit -s`), then the usual PR loop ([CONTRIBUTING.md](../../CONTRIBUTING.md));
merge only when the gates are green.

**Removal is the unit's directory and nothing else**, and this is the whole recipe — run end to end
against `crm-demo`, with the gate green afterwards:

```bash
git rm -r extensions/<name>
rm -rf extensions/<name>   # the IGNORED install output git rm leaves behind
pnpm install               # prune the workspace member from the lockfile
make check-q
```

The `rm -rf` is not tidiness: `git rm` takes the tracked files and leaves `node_modules`, so the
directory survives holding nothing a human wrote — and presence under `extensions/` IS enablement, so
the composer still sees a unit. It says exactly that if you forget.

`git rm`, never `mv`: a moved directory is still a directory under `extensions/`.

No core file is edited, and no core TEST needs touching. Removal
*disables* cleanly — routes 404, the inventory omits the unit, migrations skip it — but it does **not
purge**: the unit's tables and rows, its `extension_secret` rows and any grants of its RBAC objects
inside `role.permissions` all survive. There is no purge primitive yet (#628).
