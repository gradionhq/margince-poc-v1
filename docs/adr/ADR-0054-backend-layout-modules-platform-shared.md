# ADR-0054 — The backend is one Go module laid out as shared, platform, modules, and composition

**Status:** Active — the layout, the dependency order, and the process-role
binaries are built. The fork-owned Go directory is **not built**; see below.
**Decided:** 2026-07-04

## The decision

All backend Go code lives in one module under `backend/`, split three ways.
`shared/` holds domain-neutral leaves — identifiers, the error registry, and the
interface packages that define every cross-capability seam. `platform/` holds
technical plumbing that owns no business meaning: the database pool, the
transaction that binds the tenant, the outbox relay, the HTTP chassis, and the
single admission gate. `modules/` holds the bounded business capabilities, flat
by default and splitting into subpackages only when a named trigger applies.
`compose/` is where every cross-module edge is injected, and it is the only
place a module learns about another. Imports run one way: shared, then platform,
then modules, then compose, then the process-role binaries.

## Why

The layout answers "where does this go?" mechanically instead of by argument.
A single undivided core module is where ownership goes to die: people, deals,
and activities accrete into one package and nothing can say who owns a table.
Putting the admission gate in `platform/` lets every module and extension be
governed by the same check, without identity code importing agent code.

## What it binds in this repository

- `backend/internal/` holds the four directories: `shared/`, `platform/`,
  `modules/`, and `compose/`. `modules/` contains the bounded capabilities,
  named for what they do rather than for a layer.
- `backend/cmd/` holds **three** deployable process roles: `api`, `worker`, and
  `migrate`. Each main is thin and calls into `compose`. A developer or
  continuous-integration harness binary is not a role and lives beside the
  package it serves or in `backend/tools/`, not here.
- `backend/internal/shared/ports/` is the seam layer — `datasource`, `authz`,
  `mcp`, `connector`, `workflow`, `model`, `retrieval`, `extraction`,
  `fieldcatalog`, `jurisdiction`. These interfaces evolve additively: a new
  version beside a capability probe, never a method changed in place.
- `backend/internal/platform/auth/admit.go` holds the admission gate, and
  `backend/internal/platform/database/storekit` holds the single spelling of the
  mutation write shape every module store calls.
- `backend/arch_test.go` proves the order from the tree itself:
  `TestSharedIsPure`, `TestPlatformOwnsNoDomain`, and
  `TestNoSiblingModuleImports`.
- The single-transaction exception is narrow. Merging two records and promoting
  a lead must be all-or-nothing across tables several modules own, so the
  primary module owns that SQL directly — with a clean import graph, documented
  tables, and integration coverage.

## What is owed

The fork-owned Go directory this record introduced does not exist. The decision
named `modules/<name>/custom/` as the place a fork writes its own Go code, with
the promise that upstream never writes there, so a merge from upstream never
conflicts in it. No such directory exists in this tree, and no gate asserts that
a release leaves fork-owned paths alone.

Half the seam did ship: `backend/migrations/custom/` is real and separate from
`backend/migrations/core/`, and `scripts/check-migration-versions.sh` catches a
collision between them. A fork's schema changes survive an upgrade; a fork's Go
code has no home. Without one, a customization means editing an upstream file,
which is the merge conflict this split was meant to remove.

What arrived instead is `extensions/`, where each unit is its own Go module
importing only the frozen `backend/pkg/` surface, held there by
`scripts/check-pkg-freeze.sh`. That covers a self-contained addition, not a fork
changing how an existing capability behaves. Whether `extensions/` should absorb
that case or the custom directory should still be built is **not decided**.

## History

Adopted from the retired specification, decided 2026-07-04. Rewritten in plain
language 2026-08-19.

The source records three amendments, all folded in above: separate directories
per process role replacing one multi-command binary and the single-transaction
exception (both 2026-07-04), the module growth policy with its named split
triggers (2026-07-07), and the clarification that only a deployable role earns a
directory under `cmd/` (2026-07-19).

The source counts four process roles. There are three: the standalone
tool-surface binary was retired when the hosted transport became the only
inbound one, and the api now serves that surface on its own origin.
Jurisdiction packs also moved. This record placed them in a top-level
`jurisdictions/` directory; they ship today as units under `extensions/`, of
which `de` is the first-party one.
