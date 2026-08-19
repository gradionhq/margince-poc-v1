# ADR-0115 — An erasure completes even when the law obliges keeping a record, and a restore replays that outcome

**Status:** Active
**Decided:** 2026-08-17

## The decision

An erasure removes every datum it is not obliged to keep, and what it is
obliged to keep it restricts rather than leaves in use. The guarantee is
completeness, not annihilation. "Nothing findable" is scoped to the ordinary
reader: after an erasure, no ordinary read path returns the subject or anything
about them. A restricted record is present in exactly two places, both
deliberate — the subject's own access export, and the controller's
restricted-records list, which names the qualifying transaction and never the
correspondence.

The request is marked fulfilled when the restriction is written, not when it
expires. Everything erasable is erased, everything obliged is restricted, and
the request closes inside its one-month statutory clock. What remains
outstanding is not the request but a residual obligation, tracked on the
restricted record's own deadline and discharged by the sweep years later.

A retain-only installation still runs the expiry sweep. That posture governs
retention policies, not a suspended erasure request. An operator's contractual
duty to keep everything was never a ground to refuse a subject's erasure, only
a ground to defer it, and the statutory window already did the deferring.

A restore replays the recorded erasure outcome, not merely the suppression
list. Per restricted record it replays the restriction state and the redactions
applied, and the replay completes before the restored system accepts reads.

## Why

ADR-0114 made a commercial letter survive an erasure in restricted form. Three
guarantees written elsewhere said that could not happen, each phrased as an
absolute, because each was written when erasure had exactly two outcomes:
destroy the record, or refuse the request. A third outcome arrived — honour the
request in a diminished form — and an absolute phrased against a binary does not
survive a middle term.

Leaving the request open until the statutory window elapses was rejected. It
would park a one-month deadline open for six years, so every such request would
read as overdue on the queue that exists to show what is overdue. That turns
the compliance surface into a source of false alarms, which is how a real one
gets missed.

Without the restore replay, restoring a backup taken before the erasure returns
the record readable, un-redacted and mutable, undoing the erasure outcome and
the immutability guarantee in one step. A window measured in seconds is still a
window, which is why the ordering is stated rather than left to an implementer.

## What it binds in this repository

- `backend/internal/modules/privacy/erasure_restrict.go` writes the
  restriction; `erasuretimeline.go` destroys what carries no obligation. The
  two select by the same floor negated on one side, so a row is exactly one of
  erased or held.
- `backend/internal/modules/privacy/retentionrestricted.go` and
  `retentionposture.go` carry the expiry sweep and the posture rule.
- `backend/internal/modules/privacy/sar.go` and `sarmessages.go` keep the
  subject's own access export reaching records restricted about them;
  `sar_integration_test.go` covers it.
- `TestExpiredRestrictionCompletesTheSuspendedErasureUnderRetainOnly` in
  `backend/internal/compose/integration/erasure_restriction_integration_test.go`
  proves the sweep runs under retain-only.
- `TestARestrictedRowLeavesEveryOrdinaryReadPath` in the same file proves the
  scoping of "nothing findable".
- `backend/internal/modules/privacy/erasure_tombstone.go` and
  `backend/migrations/core/0010_consent_retention.up.sql`'s
  `erasure_suppression` table hold the suppression list the restore already
  replayed.

## History

Adopted from the retired specification, decided 2026-08-17. Rewritten in plain
language 2026-08-19. It amends ADR-0114 — not that decision, which stands, but
the guarantees in neighbouring areas that ADR-0114 silently contradicted.
ADR-0116 supplies the storage shape both decisions need.

The restore replay is the part of this record with the least code behind it
today. The erasure suppression list is replayed; the restriction and redaction
replay is a documented obligation on the disaster-recovery drill rather than a
routine in this tree.
