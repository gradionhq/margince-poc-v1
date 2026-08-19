# ADR-0060 — Operational events go in system_log; audit_log records only record changes

**Status:** Active
**Decided:** 2026-07-16

## The decision

An action that changes no record does not belong in `audit_log`. Logins,
bulk filtered exports and skipped captured messages go to a separate
append-only table, `system_log`, which stores an actor, an action verb, a
timestamp and a free-form detail object, and holds no entity reference and
no before/after images. `audit_log` is now record changes only, and its
`entity_id` column is `NOT NULL`. Some outbox events have no subject at
all — a message excluded from capture creates nothing to point at — so the
envelope admits an empty entity reference for a fixed list of pipeline
event types, and only for those.

## Why

`audit_log.entity_id` was nullable for exactly one reason: a handful of
actions that audit something but change nothing. That one exception made
every reader of the audit ledger handle a case that should not exist. Giving
the exceptions their own table lets the database reject a subject-less audit
row outright. The alternative that was tried first — inventing a fake entity
id so a skip event could satisfy the envelope — put a non-mutation into the
record-mutation ledger and named a record that never existed.

## What it binds in this repository

- `backend/migrations/core/0074_system_log.up.sql` creates `system_log`
  with `actor_type`, `actor_id`, `passport_id`, `on_behalf_of`, `action`,
  `detail` and `occurred_at`. A trigger raises on any update or delete, so a
  tamper attempt fails loudly rather than silently doing nothing.
- `backend/migrations/core/0075_audit_log_entity_required.up.sql` sets
  `audit_log.entity_id` to `NOT NULL` and drops `login` from the action
  check constraint. `export` stays, because the data-subject export names a
  real person record.
- `storekit.LogSystem` in
  `backend/internal/platform/database/storekit/storekit.go` is the only way
  to write a `system_log` row. It derives the actor from the authenticated
  principal and the workspace from the transaction, and returns the new row
  id.
- `storekit.EmitPipeline` in the same file stages an entity-less event. It
  refuses any event type that is not on the pipeline list, so a normal event
  cannot ship without its subject.
- `backend/internal/shared/kernel/events/catalog.go` holds that list;
  `capture.skipped` and its siblings are on it.
  `backend/internal/shared/kernel/events/envelope.go` carries the
  validation: a pipeline event may have an empty entity reference, and a
  half-filled reference — a type with no id, or an id with no type — is
  rejected either way.
- The envelope's `audit_log_id` trace field now means "the ledger row
  written in the same transaction". It carries an `audit_log` id for a
  record change and a `system_log` id for a pipeline event. The wire name
  did not change.
- Callers across the tree write through `LogSystem`: the login path in
  `backend/internal/modules/identity/service.go`, the filtered export in
  `backend/internal/compose/filteredexport.go`, capture sync failures in
  `backend/internal/modules/capture/syncstate.go`, and the near-match record
  in `backend/internal/modules/people/creatededupe.go`.
- `backend/internal/compose/integration/systemlog_integration_test.go`
  proves both halves against a real database:
  `TestLogSystem_writesConnectorRowWithDerivedActor` and
  `TestLogSystem_isAppendOnly`.

## History

Adopted from the retired specification, decided 2026-07-16. Rewritten in
plain language 2026-08-19.

The source says `system_log` is confined by a database row-security policy.
That is no longer how tenant separation works anywhere in the core schema:
core migration `0217_retire_row_level_security` dropped every one of those
policies, and each statement now writes the tenant predicate itself against
the workspace setting that `database.WithWorkspaceTx` binds. The
`scripts/check-rls-store-path.sh` gate holds that seam, and
`backend/rlsclaims_test.go` fails any file that still credits the retired
mechanism. Extension tables under the `ext` schema are the exception and do
still carry policies of their own.
