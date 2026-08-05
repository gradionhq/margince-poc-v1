# Add a module (and wire it into compose)

For adding a whole new **capability** (a module) or a **cross-module edge** — the cases that touch the
composition layer. For adding a single operation to an *existing* module, use
[add-an-endpoint.md](add-an-endpoint.md); for how compose assembles, read
[explanation/composition-layer.md](../explanation/composition-layer.md).

## Add a new module

1. **Create the flat package** `backend/internal/modules/<name>/` with a `doc.go` that states the one-line
   purpose and a **"Tables owned"** list (the ownership fitness test reads it). Pick one spine shape:
   *Handlers→Store* (CRUD) or *Handlers→Service* (engine) — see the existing modules in
   [reference/modules.md](../reference/modules.md).
2. **Add its migrations** ([apply-migrations.md](apply-migrations.md)) and list every new table in the
   `doc.go`.
3. **Add its contract operations** and regenerate ([add-an-endpoint.md](add-an-endpoint.md)) — the ops
   now answer 501 until wired.
4. **Embed the handler set in `Server`** (`backend/internal/compose/server.go`): add a type alias
   (`fooHandlers = foo.Handlers`), add the field to the `Server` struct, and construct it in
   `newServer` (`fooHandlers: foo.NewHandlers(pool)`). Method promotion then shadows the generated 501
   stubs automatically — no routing to write.
5. **Only import `shared` + `platform` + the generated contract** — never a sibling module. `arch-lint`
   fails a sibling import.
6. **`make check`** — the `var _ ServerInterface = Server{}` assertion proves signature coverage (a
   generated 501 stub satisfies it too, so add an endpoint test to prove your handler is actually
   wired, not still the stub), `arch-lint` proves the DAG holds, and the fitness tests (table
   ownership, RBAC gate, write shape) run.

## Add a cross-module edge (module A needs module B)

A module never imports a sibling. Inject the dependency in compose as an adapter:

1. **Declare a small consumer-side interface in module A** for exactly what it needs (e.g.
   `signals` declares a `StrengthSource` with the one method it calls), and take it as a constructor
   parameter (`signals.NewHandlers(pool, strength)`).
2. **Write the adapter in compose** that satisfies that interface, backed by module B's store:
   ```go
   // illustrative, not copy-paste Go — the real signature is signals.StrengthSource
   type signalStrength struct{ people *people.Store }
   func (a signalStrength) Strength(...) (...) { return a.people.Strength(...) } // delegate
   ```
3. **Inject it in `newServer`**: `signalsHandlers: signals.NewHandlers(pool, signalStrength{people: people.NewStore(pool)})`.
   Now A depends on the interface, B is reached only through the compose adapter, and neither imports
   the other. (Existing examples: activities←consent gate, consent←privacy eraser, imap←capture
   registry — see the edge map in [composition-layer.md](../explanation/composition-layer.md).)

## Wire optional infrastructure (blobstore, keyvault, a model)

If the capability needs infra a given process role may not have:

1. **Add an `Option`** (`With<Thing>`) in `server.go` that injects the dependency and rebuilds the
   affected handler set.
2. **Leave the endpoints as their generated 501 stub when the option is absent** — declare the gap by
   omission, never nil-deref at request time (the pattern the attachment endpoints use without
   `WithBlobstore`). Add a `/readyz` probe for the dependency when it *is* wired.
3. **Pass the option from the binary** (`cmd/api`/`cmd/worker`), reading the infra from env
   ([configuration.md](../reference/configuration.md)).

## Expose it to agents or background work (only if needed)

- **A system-of-record verb** the AI/MCP surface should reach → add it to the `Provider`
  (`backend/internal/compose/provider.go`).
- **An MCP tool** → register it in `backend/internal/compose/registry.go`.
- **A scheduled/background job** → declare the kind in `backend/api/jobs.yaml` first (a kind not
  declared there does not compile — the generated type set is closed, and a kind with no chosen
  timeout fails generation rather than running on River's silent default), `make gen`, then register
  the worker in `backend/internal/compose/jobs.go` and run it in `backend/cmd/worker`. A pass that touches every
  tenant is a *dispatcher* plus a *workspace worker*, never a fleet loop inside one job row. See
  [add-a-job.md](add-a-job.md).

## Verify

`make check` (build + arch-lint + fitness tests + drift) and `make test-integration` (the real-Postgres
lane, including RLS coverage for any new tenant table). Commit the contract, the regenerated `*_gen.go`,
the migrations, and the module together.
