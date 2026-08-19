# ADR-0049 — A jurisdiction pack checks in the standards it must obey

**Status:** Active — the German pack holds its statutory retention floors as
checked-in typed values with the statute named. The **e-invoice field list, the
accounting-export layout and the account-mapping tables are not built**; see
below.
**Decided:** 2026-06-26

## The decision

When a jurisdiction pack must conform to an external legal or industry standard,
the pack checks a pinned copy of that standard into the repository — or a
validation table derived from it — and records where it came from and which
version it is. Code in the pack is tested against the checked-in artifact, never
against prose saying "as the standard requires". The build never fetches a
standard over the network. When the upstream standard changes, the checked-in
copy is bumped in a deliberate change that re-runs the affected work, never
edited in place. The material lives only in the pack; core never carries it.

## Why

An implementer cannot invent which invoice fields a tax authority requires or
how long a record class must be kept. "Per the standard" gives an implementer
nothing to build against, gives continuous integration nothing to check, and
records no version, so a drift stays invisible until a customer's filing is
rejected. A checked-in table turns the obligation into something a test can
assert and an auditor can read. Fetching the standard at build time would trade
that for a network dependency and still leave no in-repo record of what was
used.

## What it binds in this repository

- `extensions/de/de.go` is the one built instance. Its retention classes are
  typed values in source — commercial correspondence six years, accounting
  records eight — with the statute and the amendment year named in the comment
  above them, and with the reason a third class was deliberately left out.
- The values are declared as calendar-year periods anchored at year end, not as
  day counts, because that is how the statute measures them. The shape of the
  data follows the standard rather than being converted into something more
  convenient.
- `extensions/de/de_test.go` asserts the pack against those declared values.
- `extensions/de/go.mod` makes the pack its own Go module, so a build that omits
  it carries none of this material. `scripts/check-no-jurisdiction.sh` stops the
  same strings from appearing in core.
- `extensions/de/manifest.generated.json` records what the unit contributes.

## Not built yet

The source names three artifacts to vendor. One shipped in a reduced form and
two have no code at all. `extensions/de/` holds four files and no directory of
standards.

- **The mandatory field list for European e-invoicing and its German profile.**
  Owed once the pack emits invoices; nothing emits one today.
- **The accounting-export field layout and the two standard German
  chart-of-accounts mappings.** Owed once the pack exports to an accounting
  system.
- **The record-class taxonomy behind the retention floors.** Partly discharged:
  two classes are in typed code with their statute named. The full
  classification, and the per-class action of hold or archive or purge, is not.

The form these should take is settled — a checked-in table with its source and
version recorded — but their content and file layout are not designed.

One obligation the source raised and did not discharge is still open: some of
these standards restrict redistribution of their text and tables. Before any
verbatim copy is checked in, its terms have to be confirmed, and a derived
validation table is the fallback where verbatim copying is not permitted. That
question has not been answered.

## History

Adopted from the retired specification, decided 2026-06-26. Rewritten in plain
language 2026-08-19.

The source places the vendored material in a `standards/` directory inside the
German pack, which does not exist. When the first such artifact lands it should
go there, inside `extensions/de/`.
