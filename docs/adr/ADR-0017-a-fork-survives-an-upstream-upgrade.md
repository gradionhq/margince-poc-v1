# ADR-0017 — A customer's fork survives an upstream upgrade

**Status:** Active
**Decided:** 2026-06-04

## The decision

Fork-owned code and fork-owned schema live in directories upstream never writes
to, so an upgrade cannot collide with them. Migrations split into two namespaces
with separate tracking tables: `core/` is upstream's, `custom/` is the fork's,
and the runner applies core first, then custom. A Go interface a fork implements
is frozen — you add a capability by introducing a new interface and probing for
it at runtime, never by adding a method to the existing one. Structs grow new
fields and never repurpose old ones. Any upstream column rewrite or new
constraint spans at least two releases so the fork gets an announced transition
window.

## Why

The sharp risk in a forked product is not the merge that conflicts loudly; it is
the merge that succeeds and quietly drops something. Two branches picking the
same migration number produce two individually valid trees whose collision only
exists after the merge, and the migration loader then refuses the whole
namespace rather than applying half of it. Separating the namespaces removes
number collisions between upstream and a fork; separating the directories
removes file collisions in Go. Adding a method to an interface a fork already
implements breaks that fork's build, which is why seams only grow sideways.

## What it binds in this repository

- `backend/migrations/core/` and `backend/migrations/custom/` are the two
  namespaces. Core versions `0001`–`0284` keep their four-digit sequence; every
  core migration written since is named for the unix second it was written,
  which is unique without consulting any branch. Custom migrations carry a
  `YYYYMMDDHHMMSS` stamp — `20260716120000_overlay.up.sql` is the oldest.
- `TestEmbeddedMigrationNamespacesLoad` in
  `backend/migrations/migrations_test.go` proves both namespaces load.
  `TestCoreMigrationVersionsAreUnixSecondsAfterTheClosedSequence` in
  `backend/migrations/coreversions_test.go` holds the naming rule.
- `make migration-versions` runs `scripts/check-migration-versions.sh`, which
  checks a new migration against the base branch — the check no tree-local test
  can do. It is part of `make check-backend`.
- `make migrate-create NAME=<x>` stamps the name, so the rule is scaffolding
  rather than something to remember.
- `modules/<name>/custom/` is the Go peer of the custom migration namespace, the
  fork-owned source path upstream never writes to. The vanilla tree carries no
  such directory yet; the seam is declared in `CLAUDE.md` and `AGENTS.md`.
- `extensions/` is the other fork-facing seam: each unit is its own Go module
  importing only the allowlisted `backend/pkg/**` surface, checked by `make
  ext-imports` and `make pkg-freeze`.
- `make contract-breaking-check` detects a breaking change to the HTTP contract.
- Applied core migrations are never edited — an applied version does not re-run,
  so editing one changes what a fresh install gets while every deployed database
  keeps the old behaviour.

## History

Adopted from the retired specification, decided 2026-06-04. Rewritten in plain
language 2026-08-19. Five amendments shaped it: the custom-column drift guard
became an owned comparator rather than a schema-tool exclusion glob, migration
execution gained an online-DDL discipline, the upgrade conflict classifier got
its detection mechanism, the HTTP breaking-change gate was made advisory until
the contract has an external consumer, and core migration naming moved from a
sequence to a unix-second stamp after two collisions in two days. ADR-0054
extended the decision to Go source. The source also describes a `crm gen
upgrade` preflight command; this tree has no such command, and the guards it
named are carried by the migration and contract gates listed above.
