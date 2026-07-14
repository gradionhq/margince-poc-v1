# Contract-first

This repository is built **from** a specification that lives in a
separate sibling repo. Principle P3 is binding: when this
code and the spec disagree, the spec wins. The same posture applies one
level down, between the code and its own API contract.

## The contract is the source of truth

`backend/api/crm.yaml` (OpenAPI 3.1) is the authoritative API surface.
Nothing is exposed that isn't in it, and everything in it exists at
runtime from day one:

1. `make gen` downgrades the 3.1 contract to a 3.0 overlay
   (`tools/contract-overlay`) and runs oapi-codegen over it, producing
   the request/response types and the chi `ServerInterface`
   (`internal/contracts/` — generated, never hand-edited).
2. `tools/gen-stubs` derives one explicit **501 stub** per contract
   operation (`internal/compose/stubs_gen.go`). Module handlers shadow
   the operations they implement; an unimplemented operation answers a
   loud 501, never a silent 404.
3. `tools/gen-agentpolicy` derives the agent admission table from the
   contract's `x-mcp-tool` / `x-agent-access` annotations — and **fails
   generation** for any mutating operation carrying neither, so an
   un-tiered endpoint cannot ship.

## Drift is merge-blocking

`make drift` regenerates everything and fails on any diff (`git diff
--exit-code` over the generated files). That gate is part of
`make check`, so:

- hand-editing a generated file fails the build;
- changing the contract without regenerating fails the build;
- changing generator output (even by one byte) is visible in review.

## Changing the surface

The order is always **contract first, then code**: edit `crm.yaml`, regenerate (`make gen`), implement
the handler in the owning module shadowing the generated stub, and let `make check` prove the contract,
the generated artifacts, and the implementation agree. The step-by-step checklist is a how-to:
**[how-to/add-an-endpoint.md](../how-to/add-an-endpoint.md)**.
