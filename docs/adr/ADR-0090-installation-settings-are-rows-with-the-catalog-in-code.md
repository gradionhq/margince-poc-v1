# ADR-0090 — Installation settings are rows in one table, with the catalog in typed code

**Status:** Active — the table, the typed catalog, the freeze probe and the
three gates all ship. The **overlay pair still lives as two columns on
`workspace`**, not as the composite setting the source decided; see below.
**Decided:** 2026-08-06

## The decision

An installation setting is a row in one table keyed by a string, and adding a
setting costs no schema change. What a setting *is* — its type, default,
validator, the permission object gating it, the audit verb it writes, and
whether it has become unchangeable — lives in typed Go, declared by the module
that owns the setting. Reads are typed, so no domain code ever decodes raw JSON.
Values that must change together are one setting holding a composite value,
because the row is the unit of an atomic write and therefore has to be the unit
of the rule that spans them. This is not a runtime configuration engine: the
catalog is closed, and a key nobody registered is a test failure rather than an
extension point.

## Why

Before this, a setting was a column on the `workspace` row, so adding one
boolean cost a schema migration plus a hand-written backfill of the permission
policy into every already-seeded role. That is a disproportionate price, and
every future toggle paid it. The fork also owned two columns on a core table,
which inverts the rule that upstream never writes in the fork's own area. Moving
the catalog into code keeps the per-setting audit verbs and per-setting
permission objects that the column form had, so nothing is traded away except
the migration.

## What it binds in this repository

- `backend/migrations/core/0190_setting.up.sql` creates `setting` with `key` as
  the primary key, a `jsonb` `value`, and `updated_at`. The key must match
  `<module>.<name>`, checked in the schema. The table has no workspace column
  and no row-level security, deliberately: one installation serves one
  organization, so an installation setting is per-installation by definition.
  With no row-level security here, the permission gate at the writer is the only
  control, which the migration comment says outright.
- The same migration carries `capture.auto_enrich` across from its old column,
  writing a row only when the installation had turned it off. An unset setting
  reads as its default, so seeding the default would say nothing.
- `backend/migrations/core/0191_installation_settings.up.sql` moves the
  installation's name, timezone and base currency off `workspace` behind a new
  `installation_settings` permission object, and backfills that object into the
  seeded system roles. Two of the three had no human-reachable way to be
  corrected before this.
- `backend/internal/platform/settings/entry.go` owns the mechanism and no
  domain. `Definition` exposes `Key`, `Object`, `AuditVerb`, `DefaultJSON`,
  `ValidateJSON` and `CanonicalJSON`; `store.go` reads and writes.
- Modules declare their own entries: `capture/settingsentry.go`,
  `identity/settingsentry.go`, `privacy/settingsentry.go`.
  `compose/settingswiring.go` assembles them into the one registry.
- `backend/internal/modules/deals/basecurrencyfreeze.go` is the freeze probe.
  Deals supplies it at composition, so the settings package refuses a base
  currency change without knowing what a deal is, and the refusal carries the
  owning module's own reason.
- `backend/settingscatalog_test.go` holds the three obligations: every setting
  is unique, well formed and governed; every key is prefixed by the module that
  owns it; and no key exists in both the registry and the deployment
  configuration. That last one enforces a promise nothing checked before.
- Setting writes go through the same transaction shape as any other mutation —
  row plus audit row plus outbox row — covered by `backend/writeshape_test.go`.

## Not built yet

The source decided that the fork's overlay pair — its system-of-record mode and
which incumbent system it mirrors — becomes one composite setting, so the rule
that the two change together is a validator on one row. They are still two
columns on `workspace`, added by the custom migration
`20260716120000_overlay.up.sql`, and `backend/tableownership_test.go` still
carries the written waiver letting the overlay module write them. The design is
settled in principle; the migration and the validator are not written.

## History

Adopted from the retired specification, decided 2026-08-06. Rewritten in plain
language 2026-08-19.

Ratified at the same time: the installation's own name is a setting, seeded at
bootstrap and authoritative in the row afterwards. The rejected alternative was
to treat it as deployment identity changeable only by redeploying, which would
mean renaming your own organization needs a deployment.
