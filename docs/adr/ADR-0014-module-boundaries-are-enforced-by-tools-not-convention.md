# ADR-0014 — Module boundaries are enforced by tools, not by convention

**Status:** Active — table ownership is gated. **Event-type and cache-key
ownership are not gated**; see below.
**Decided:** 2026-06-04

## The decision

The backend is one Go module whose capability boundaries are enforced by the
toolchain, not by anyone remembering them. A module never imports a sibling
module; when one capability needs another, the composition layer injects the
edge. Cross-module contracts live in dependency-free interface packages that
every module may import and no implementation crosses. Four mechanisms hold the
rule: the Go compiler's own `internal/` rule, `depguard` import rules inside
`golangci-lint`, a declared dependency graph checked by `go-arch-lint`, and
fitness tests in `backend/arch_test.go` that derive their package lists from the
tree. A module also writes only the database tables it declares it owns.

## Why

This codebase is built largely by agents working in parallel, and forks of it
are edited by people who never read this record. "Remember not to import across
modules" is exactly the discipline that fails silently under both conditions. An
import linter also protects only the code seam, so a second gate is needed for
the data seam — two modules writing the same table couple just as hard as two
modules importing each other, and no import rule sees it.

## What it binds in this repository

- `depguard` is enabled in `backend/.golangci.yml` (the linter list at line 22,
  the rules from line 84) with per-path allow and deny rules that carry reason
  strings, so a denied import fails the lint run with an explanation.
- `backend/.go-arch-lint.yml` declares the dependency graph in one file. The
  `arch-lint` target in `backend/Makefile` runs `go-arch-lint` over it and fails
  on any forbidden edge.
- `backend/arch_test.go` holds the boundary as tests:
  `TestPlatformOwnsNoDomain`, `TestNoSiblingModuleImports`, `TestSharedIsPure`,
  and `TestPublishedSurfaceIsPure`. They walk the package tree rather than a
  hand-maintained list, so a new package cannot escape by not being listed.
- `backend/tableownership_test.go` is the data-seam gate.
  `TestEveryPackageOnlyWritesTablesItOwns` holds each module to the tables its
  `doc.go` declares, and fails a write to a table the module does not own.
- The Go compiler's `internal/` rule is the fourth layer and needs no
  configuration; `backend/internal/` is unreachable from outside the module and
  `modules/<name>/internal/` from outside that module.

## What is owed

The source recorded a 2026-06-23 amendment adding a checked-in ownership
manifest that maps every table, cache key prefix, and event type to exactly one
owning module, with continuous integration failing on any of the three with zero
or more than one owner. Tables are covered today by
`backend/tableownership_test.go`. **Event types and cache key prefixes are not**
— no gate attributes an event type to the module that writes its outbox row, so
two modules can emit the same event type and nothing notices. Extending the
existing ownership test to read the event type from the outbox write site is the
obvious shape, but the design is not final and the work is not built.

## History

Adopted from the retired specification, ratified 2026-06-04. Rewritten in plain
language 2026-08-19.

ADR-0054 (2026-07-04) re-homed every mechanism here onto the current source
layout without weakening any of them; the names moved, the enforcement did not.
The source's 2026-06-23 amendment added the data-seam ownership gate, whose
built and unbuilt halves are separated above. ADR-0042 (2026-06-24) applied the
same rules to jurisdiction packs; those now ship as units under `extensions/`,
each its own Go module, and `scripts/check-pkg-freeze.sh` (run by
`make pkg-freeze`) holds them to the published `backend/pkg/` surface.
