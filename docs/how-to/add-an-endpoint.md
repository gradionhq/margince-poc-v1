# Add or change an API endpoint

A task checklist for adding a new operation to the HTTP API (or changing an existing one). The API is
contract-first: you edit the contract, regenerate, then implement — never hand-write routing or types.
For *why* it works this way, see [explanation/contract-first.md](../explanation/contract-first.md); for
the store mechanics step 3 relies on, see
[explanation/write-backbone.md](../explanation/write-backbone.md).

## Steps

1. **Edit the contract** — `backend/api/crm.yaml`. Add the path + operation + request/response schemas.
   A **mutating** operation (POST/PUT/PATCH/DELETE) MUST carry one of:
   - `x-mcp-tool: { verb, record_type, tier: auto_execute|confirmation_required|dynamic }` — exposes it as a governed agent
     tool at that autonomy tier; or
   - `x-agent-access: human-only` (rejects agent principals — e.g. approvals, consent) or
     `auth-bootstrap` (login/session machinery).

   The generator **fails** on a mutating op with neither, so an un-tiered endpoint cannot ship.

2. **Regenerate** — `make gen`. This rewrites the generated files (never hand-edit them):
   `internal/contracts/api_gen.go` (types + `ServerInterface`), `internal/compose/stubs_gen.go` (a new
   **501** stub), and `internal/compose/agentpolicy_gen.go` (the admission row). The build now compiles
   and the endpoint answers `501` until you implement it.

3. **Implement the handler in the owning module** — add the method matching the generated signature to
   that module's `Handlers` (`internal/modules/<name>/`). Do the work through the module's
   `*Store`/`*Service`, following the store shape: `WithWorkspaceTx`, the auth gate at entry, and
   `storekit.Audit`+`Emit` for any mutation (see
   [explanation/backend-onboarding.md → how a store reads and writes](../explanation/backend-onboarding.md#how-a-store-reads-and-writes-the-shape)).
   Because `compose.Server` embeds each module's handler set one level deep, your method **shadows the
   501 stub automatically** — there is no routing to wire.

4. **Wire in `compose` only if needed** — if the module's handler set isn't embedded in `Server` yet,
   or the operation needs another module's data, see [add-a-module.md](add-a-module.md) (embedding a
   handler set, injecting a cross-module adapter) and
   [explanation/composition-layer.md](../explanation/composition-layer.md). Most endpoints on an
   existing module need no compose change — the embedded handler set already shadows the stub.

5. **Add a migration if the schema changed** — see [apply-migrations.md](apply-migrations.md), and
   record any new table in the owning module's `doc.go` "Tables owned".

6. **Verify** — `make check`. `build` + the `var _ ServerInterface = Server{}` assertion prove the
   operation exists on the contract surface — but a generated 501 stub also satisfies the interface, so
   **an endpoint test asserting a non-501 response is what proves your handler is wired** (not still the
   stub). `drift` proves the generated files match the contract, and the fitness tests run. Add
   `make test-integration` for the real-Postgres lane and `make frontend-check` if `frontend/` changed.

7. **Commit the contract and generated output together** — `crm.yaml` **and** every regenerated
   `*_gen.go` in the same commit (plus `frontend/src/api/schema.d.ts` if it changed). A hand edit or a
   missed `make gen` fails the drift gate.

## Notes

- **Changing an existing operation** is the same loop, but watch the contract-breaking gate: root
  `make check` runs `oasdiff` against `origin/main` — a *breaking* change (removed op, narrowed type)
  fails; additive passes. A deliberate re-sync uses `CONTRACT_STABILITY=pre-live`.
- **Reads are gated too.** Anything that returns a record carries the row-scope gate
  (`auth.EnsureVisible`), including replay/conflict/error paths — see
  [explanation/authorization.md](../explanation/authorization.md).
