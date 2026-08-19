# ADR-0116 — The restriction has a database schema, and the evidence for it outlives the deal it points at

**Status:** Active
**Decided:** 2026-08-17

## The decision

The statutory class stamp and the restriction state are columns on `activity`,
and the CHECK constraints on them are the real decision. All three restriction
columns are set together or none of them are. The deadline must be later than
the moment the restriction was written. The class stamp carries its own
timestamp. Only a stamped row may be restricted.

A database trigger enforces two rules no handler could. The class stamp is
write-once: no statement may clear it or change it to a different value once
set. A restricted row refuses every write except one that clears the
restriction, and clearing it must erase the content in the same statement.
The trigger returns the old row on a permitted delete, because the new row is
null there and returning it would block every delete of every row. The
retention engine's ordinary selectors exclude restricted rows, so a nightly pass
never attempts a write the trigger refuses.

The evidence lives in its own table, `activity_retention_evidence`. Per
qualifying transaction it records the deal reference, the deal's name copied at
the moment of qualification, the basis, and for a controller pin the deciding
person and their stated reason. The deal reference nulls rather than cascades on
deal delete, and the name is a frozen copy. A record qualifying through several
deals gets several rows. A pin may name no deal at all; a derived basis must
name one.

A separate `redacted_fields` array on the activity and on the delivery row names
the columns an erasure emptied. The audit vocabulary gains `restrict`,
`release`, `pin` and `expire`. A refusal surfaces as a distinct sentinel with an
HTTP 423, not a conflict.

## Why

ADR-0114 decided a behaviour and named no storage for it. Three of the four
things it needed were ordinary columns anyone would have guessed correctly. The
fourth was not.

The natural way to answer "why is this record held?" is to join the activity's
links to the deals at read time. That answers wrongly and does so silently. The
link table cascades on deal delete. A relink deletes the link it replaces. A won
deal can be reopened, and a deal can be renamed. So a controller asking six
months later can get an empty answer about a record that is genuinely and
correctly held — and an empty answer is worse than no feature, because the one
audience for this list is a supervisory authority asking the controller to
substantiate a retention claim.

Each CHECK constraint blocks a way the feature fails quietly rather than loudly.
A row with a restriction but no deadline is never selected by the expiry sweep:
hidden from every read path, immutable, and never erased. A permanently
invisible record, produced by one forgotten assignment. A window that closed
before it opened erases immediately, which looks exactly like the feature
working.

The refusal is a 423 rather than a 409 because a 409 tells a caller the row
moved
under them and invites a retry, and nothing about this refusal clears until a
date.

## What it binds in this repository

- `backend/migrations/core/0288_activity_retention_class.up.sql` adds the six
  columns and the four CHECK constraints —
  `activity_retention_class_known`, `activity_retention_class_stamped`,
  `activity_restriction_complete`, `activity_restriction_window` and
  `activity_restriction_needs_class` — plus the partial index
  `idx_activity_restricted_until` that matches the sweep's own predicate.
- `backend/migrations/core/0289_activity_restriction_guard.up.sql` holds the
  `BEFORE UPDATE OR DELETE` trigger and creates `activity_retention_evidence`
  with `ON DELETE SET NULL` on the deal reference, the frozen `deal_name`, the
  unique index over activity, deal, name and basis, and the constraint that a
  controller pin carries both a deciding name and a reason.
- `backend/migrations/core/0290_restriction_lift_must_erase.up.sql` makes the
  lift carry the erasure, closing the hole where a bare update clearing the
  restriction would leave the correspondence readable.
- `backend/migrations/core/0287_audit_restriction_verbs.up.sql` puts `restrict`
  and `pin` in the `audit_log_action_check` constraint beside `release` (added
  by `0243`) and `expire` (added by `0253`), and the same values are declared on
  the `AuditLogEntry.action` enum in `backend/api/crm.yaml`.
- `ErrRetentionHold` is in the fixed sentinel registry at
  `backend/internal/shared/apperrors/apperrors.go`.
- `backend/internal/modules/privacy/restrictedlist.go` reads the evidence table
  to name every qualifying transaction;
  `backend/internal/modules/privacy/restrictionoverride.go` handles release and
  pin.
- `comms_outbound` — the delivery row that stores recipients, subject and body a
  second time — is created in
  `backend/migrations/core/0136_comms_outbound.up.sql` and is redacted alongside
  its activity.
- `TestARestrictedRowRefusesEveryOrdinaryWrite` and
  `TestAControllerPinsCorrespondenceTheDerivationCannotSee` in
  `backend/internal/compose/integration/erasure_restriction_integration_test.go`
  exercise the trigger and the pin path.

## History

Adopted from the retired specification, decided 2026-08-17. Rewritten in plain
language 2026-08-19. It supplies the persistence ADR-0114 decided the behaviour
of and named no storage for; ADR-0114 itself stands unchanged.

The duplication of the deal name is deliberate rather than a normalization
mistake, and the reason is written into the migration comment so a later reader
does not tidy it away.
