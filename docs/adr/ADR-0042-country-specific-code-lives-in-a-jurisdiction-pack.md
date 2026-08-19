# ADR-0042 — Country-specific code lives in a jurisdiction pack, never in core

**Status:** Active — the seam, the German pack and the core-neutrality gate
are built, carrying statutory retention floors. The e-invoice, conformity,
trust-artifact and export contributions are **not built**; see below.
**Decided:** 2026-06-24

## The decision

Country-specific behaviour lives in its own compiled unit behind a small
interface, and core code never names a country. Core declares the seam, holds a
registry of the packs a binary was compiled with, and asks the registry for the
contributions that apply. A pack registers itself; core cannot import a pack and
a pack cannot import another pack. Which packs a binary carries is decided when
it is built, not by a runtime toggle. Interface translations are not a pack
concern — a person's display language is a per-user setting, unrelated to which
country's law an installation operates under.

## Why

Without this boundary German tax and retention logic compiles into every
installation, including ones that will never file a German invoice. It also
scatters country rules through the modules, so nobody can read one place and see
all of a country's obligations. Adding a second country would smear the same
problem twice. A compile-time boundary is the strongest one available: the code
is not in core's module graph, so an accidental import fails to build rather
than passing review.

## What it binds in this repository

- `backend/internal/shared/ports/jurisdiction/jurisdiction.go` is the core-side
  seam. It holds the registry — `Register`, `For`, `Applicable` — and aliases
  the published contract types so a core call site and a pack agree on one type.
- `backend/pkg/extension/jurisdiction` is the published contract a pack
  implements: `Pack`, `Retention`, `RetentionClass`, `Period`, `Anchor`, and the
  closed class vocabulary `CommercialCorrespondence` and `AccountingRecords`.
- `extensions/de/` is the German pack, its own Go module with its own `go.mod`.
  `extensions/de/de.go` declares the pack and its statutory retention classes:
  commercial correspondence six years, accounting records eight, each anchored
  at calendar-year end. Its presence under `extensions/` is what enables it.
- `scripts/check-no-jurisdiction.sh` is the neutrality gate. It fails the build
  when a hand-written file under `backend/internal` names one of the German or
  European regulatory standards on its list, or writes a quoted two-letter
  country code beside a country-ish word. It strips comments before matching, so
  core may explain the statute behind a generic mechanism while a hard-coded
  country string in real code still fails.
- `backend/internal/modules/privacy/retention_floor.go` is the core consumer. It
  walks `jurisdiction.Applicable()` and takes the strictest floor any
  compiled-in pack declares. A workspace policy may keep longer, never destroy
  earlier.
- The seam carries no locale bundle, as `jurisdiction.go` states.

## Not built yet

The source decided five contribution interfaces on this seam. Only retention
shipped. The four that are owed:

- Emitting and validating a country's e-invoice format.
- Declaring which conformity artifacts a country requires, and the shape of the
  declaration document.
- Listing a country's buyer-facing compliance artifacts.
- A country-scoped variant of the audit export.

Their shape is a sketch, not a settled design. Each returns to the seam when the
work that needs it lands, and its interface is decided then against a real
caller rather than now.

## History

Adopted from the retired specification, decided 2026-06-24. Rewritten in plain
language 2026-08-19.

The source put packs in top-level directories of their own. Packs are now
stable-tier extensions under `extensions/` (ADR-0120), wired by `make
composition`. The boundary rule is unchanged.

The source treats the European signature regime and the European conformity
declaration as German for a first version, with a regional pack as their later
home. Neither is built, so that question is still open.

Extension tables are the one place `FORCE ROW LEVEL SECURITY` still applies —
for example `extensions/notes/migrations/0001_note.up.sql`. Core tenant
isolation no longer uses row-level security at all; migration
`0217_retire_row_level_security` removed it, and core reads are scoped by the
per-statement predicate that `database.WithWorkspaceTx` sets.
