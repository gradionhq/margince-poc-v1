# ADR-0015 — The OpenAPI contract generates the code, and any drift blocks the merge

**Status:** Active
**Decided:** 2026-06-04

## The decision

`backend/api/crm.yaml` is the authoritative description of the HTTP surface, and
the Go server types, the TypeScript client types, the agent tool policy and the
registry manifests are all generated from it. Nobody hand-edits a generated
file. Every central list — stubs, tool policy, job kinds, record shapes — is
regenerated rather than hand-merged, so a merge conflict in one is resolved by
re-running the generator. `make gen` regenerates everything; `make drift`
regenerates and then fails if anything changed, which is what makes stale
generated code impossible to merge.

## Why

Generated code that has quietly diverged from the contract is worse than no
contract, because every reader still trusts it. A hand-maintained aggregating
list — a route table, an import manifest — is a file every branch touches, so it
conflicts constantly and a bad resolution silently drops an entry the compiler
cannot see. Regenerating both problems away costs one build step and removes a
whole class of merge accident.

## What it binds in this repository

- `make gen` in `backend/Makefile` runs the whole chain:
  `tools/gen-composition`, `go generate ./internal/contracts`, `go generate
  ./pkg/extension/crm`, then `tools/gen-stubs`, `tools/gen-agentpolicy`,
  `tools/gen-recordfields`, `tools/gen-aitasks` and `tools/gen-jobs`.
- `make drift` runs `gen` and then `git diff --exit-code` over `*_gen.go`,
  `internal/contracts/`, `config/ai-routing.schema.json`, every
  `extensions/*/manifest.generated.json` and the agent-app admission vocabulary.
  It also fails on an uncommitted generated manifest, because `git diff` ignores
  untracked files.
- `backend/internal/contracts/api_gen.go` is the generated server interface and
  model set. `backend/internal/contracts/gen.go` pins the generator version
  (`oapi-codegen@v2.7.1`) so the output is reproducible;
  `backend/internal/contracts/oapi.yaml` is its config.
- `backend/tools/contract-overlay` downgrades the authoritative OpenAPI 3.1
  contract to 3.0.3 at generate time, because the Go generator's parser does not
  read 3.1. The overlay output lands in a gitignored build directory and is
  never committed back. The shared transform lives in
  `backend/tools/internal/oas30`.
- The frontend has its own drift leg: `make fe-drift` and `make
  contract-frontend-drift` both call `scripts/check-contract-frontend-drift.sh`,
  so a contract change owes the TypeScript regeneration too.
- `make contract-breaking-check` is the breaking-change detector on the
  contract; `make check-backend` runs it, along with `contract-frontend-drift`,
  as part of the merge gate.
- `backend/tools/` is its own Go module so the generators' dependencies stay out
  of the product module.

## History

Adopted from the retired specification, decided 2026-06-04. Rewritten in plain
language 2026-08-19. ADR-0054 re-homed the paths onto the current backend layout
without weakening any mechanism. The source also gated performance budgets in CI
through a load-test and benchmark harness; the current tree runs performance
work through `make bench-perf` and `make perfdoc` rather than the tooling the
source named.
