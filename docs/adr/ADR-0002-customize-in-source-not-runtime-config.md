# ADR-0002 — Customers customize this product by editing source, not by clicking through a config engine

**Status:** Active
**Decided:** 2026-06-04

## The decision

A customer changes what this product does by changing its source code, not by
configuring a metadata engine at runtime. New objects, new relationships, new
validation and new behaviour are real Go code, real migrations and real contract
changes, reviewed in a pull request and deployed. We do not build custom-object
builders, no-code workflow designers, or a dynamic-schema interpreter that reads
table definitions out of rows at request time. The one runtime concession is a
typed custom field on an existing core object; anything that adds a table, a
join, or new behaviour stays a source change.

## Why

Every incumbent CRM answers customization with a config engine, and that engine
becomes permanent cost: joins that reporting cannot follow honestly, indexes
that cannot exist because the schema is data, and a config surface large enough
to be its own support burden. Keeping the schema static means the query planner
sees real columns and real indexes, so a report is fast and correct for the same
reason. Without this rule the product grows a second, worse programming language
that only its own admins can read.

## What it binds in this repository

- `extensions/` is the stable customization seam. Each unit is its own Go module
  importing only the allowlisted `backend/pkg/**` surface, and its presence in
  the directory is the enablement. The vanilla tree ships `de`,
  `dispact-connector`, `notes`, `yogi` and `zalo-oa`.
- `backend/migrations/custom/` is the fork-owned migration namespace, ordered by
  `YYYYMMDDHHMMSS` timestamp and tracked separately from
  `backend/migrations/core/`. Upstream never writes there.
- `make composition` generates the wiring under `build/composition/`; the
  committed vanilla stub at `composition/` keeps bare `go` commands resolving.
- `make check-ext-migrations` applies every extension unit's migrations against
  a real database so a fork-owned schema change is gated the same way core is.
- `backend/internal/modules/customfields/` is the bounded runtime concession: a
  typed field added to an existing core object, through `service.go`,
  `engine.go` and `lifecycle.go`. There is no runtime object builder anywhere in
  the tree.
- Reports read the static schema directly — `backend/internal/compose/report.go`
  compiles a typed plan against real columns, which only works because the
  schema is not itself data.

## History

Adopted from the retired specification, decided 2026-06-04. Rewritten in plain
language 2026-08-19. The source carries three amendments: agent-performed source
editing was repositioned as an internal delivery practice rather than a product
feature, a bounded runtime custom-field capability was carved out, and the
no-deploy half of that capability was deferred so that only the source path
ships. The source also names a proven limit: additive customization is safe,
while replacing an existing core behaviour has no upgrade-safe seam and is a
supported-but-discouraged core edit.
