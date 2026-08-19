# ADR-0114 — Commercial correspondence survives an erasure in restricted form

**Status:** Active. Two parts are named as not built: the pre-erasure preview a
controller would pin from, and the restriction replay after a backup restore.
**Decided:** 2026-08-16, extended 2026-08-17

## The decision

German statutory retention covers a commercial letter — correspondence about a
business transaction, including its preparation, conclusion, execution or
cancellation. An activity carries that floor when its kind is neither `task` nor
`note` **and** it is linked to a deal that reached a commercial conclusion: the
deal was won, or it carries an offer that has left draft state. A scheduling
mail or a marketing enquiry carries no obligation and is erased normally.

The classification is stamped onto the record when it is first earned and never
re-derived. Qualification is reversible in the product — a won deal can be
reopened, and relinking an activity deletes the link it replaces — so asking the
question at erasure time asks it of a record whose evidence may have moved.

When an erasure meets a stamped record, the record is **restricted** rather than
destroyed. It leaves every ordinary read path — lists, timelines, search,
ordinary exports, embeddings, agent grounding and agent tools. Two reads still
reach it on purpose: the subject's own access export, because a person is
entitled to know what is held about them, and the controller's
restricted-records list, because somebody has to be able to answer what is held
and until when. That list names the qualifying transaction and **never the
correspondence itself**: the obligation justifies storing the letter, and it
justifies nobody reading it. A controller can state what is held, why, and
until when without seeing any of it.

Every ordinary write is refused in the database, below every
role including admin — the refusal is a trigger, not a permission check, so no
role can be granted past it. Two audited writes pass: an authorised release,
which erases the record, and the expiry sweep completing the suspended erasure
when the deadline arrives. The deadline is pinned from the floor in force at the
moment of restriction, so a later configuration change never shortens an
obligation already recorded.

Redaction is per item of data, not per document. The restricted record keeps
what makes it a commercial record — subject, body, the transaction it hangs off,
the attachments. It loses the provider's raw payload, the erased subject's own
address, and the channel identifiers that identify the subject. The same rule
reaches the outbound delivery row behind a sent activity, which stores the
recipients, subject and body a second time.

An administrator holding the retention-policy authority may release a restricted
record, which erases it, or pin a record the derivation could not see. Both
require a stated reason and both are audited.

### The erasure still completes

An erasure removes every datum it is not obliged to keep, and restricts what it
is obliged to keep. The guarantee is completeness, not annihilation. "Nothing
findable" is scoped to the ordinary reader.

The request is marked fulfilled when the restriction is written, not when it
expires. Everything erasable is erased, everything obliged is restricted, and
the request closes inside its one-month statutory clock. What remains
outstanding is not the request but a residual obligation, tracked on the
restricted record's own deadline and discharged by the sweep years later.

A retain-only installation still runs the expiry sweep. That posture governs
retention policies, not a suspended erasure request. An operator's contractual
duty to keep everything was never a ground to refuse a subject's erasure, only
a ground to defer it, and the statutory window already did the deferring.

A restore replays the recorded erasure outcome, not merely the suppression list:
per restricted record it replays the restriction state and the redactions
applied, and the replay completes before the restored system accepts reads.

### The storage shape

The class stamp and the restriction state are columns on `activity`, and the
CHECK constraints on them are the real decision. All three restriction columns
are set together or none are. The deadline must be later than the moment the
restriction was written. The class stamp carries its own timestamp. Only a
stamped row may be restricted.

A database trigger enforces two rules no handler could. The class stamp is
write-once. A restricted row refuses every write except one that clears the
restriction, and clearing it must erase the content in the same statement. The
trigger returns the old row on a permitted delete, because the new row is null
there and returning it would block every delete of every row. The retention
engine's ordinary selectors exclude restricted rows, so a nightly pass never
attempts a write the trigger refuses.

The evidence lives in its own table, `activity_retention_evidence`. Per
qualifying transaction it records the deal reference, the deal's name copied at
the moment of qualification, the basis, and for a controller pin the deciding
person and their stated reason. The deal reference nulls rather than cascades on
deal delete, and the name is a frozen copy. A record qualifying through several
deals gets several rows. A pin may name no deal at all; a derived basis must
name one.

A `redacted_fields` array on the activity and on the delivery row names the
columns an erasure emptied. The audit vocabulary carries `restrict`, `release`,
`pin` and `expire`. A refusal surfaces as a distinct sentinel with HTTP 423.

## Why

The build before this decision shielded every activity that was not a task or
note, and left everything it shielded fully readable. Both halves were wrong, in
opposite directions.

Shielding by exclusion over-retains. Erasure is suspended only insofar as a
legal obligation makes the specific data necessary, and a rule that shields
everything by default is exactly the failure regulators name: applying the
legal-obligation exception without assessing each case.

Leaving the shielded record readable under-protects. The obligation justifies
storage. It does not justify a sales team continuing to read the correspondence
of somebody who asked to be erased.

The stamp exists because the failure is not symmetric. Over-retention is an
argument to have with a supervisory authority. Destruction is irreversible.

**On completeness.** Three guarantees written elsewhere said a record could not
survive an erasure, each phrased as an absolute, because each was written when
erasure had exactly two outcomes: destroy the record, or refuse the request. A
third outcome arrived — honour the request in a diminished form — and an
absolute phrased against a binary does not survive a middle term.

Leaving the request open until the statutory window elapses was rejected. It
would park a one-month deadline open for six years, so every such request would
read as overdue on the queue that exists to show what is overdue. That turns the
compliance surface into a source of false alarms, which is how a real one gets
missed.

Without the restore replay, restoring a backup taken before the erasure returns
the record readable, un-redacted and mutable, undoing the erasure outcome and
the immutability guarantee in one step.

**On the evidence table.** The natural way to answer "why is this record held?"
is to join the activity's links to the deals at read time. That answers wrongly
and does so silently. The link table cascades on deal delete. A relink deletes
the link it replaces. A won deal can be reopened, and a deal can be renamed. So
a controller asking six months later can get an empty answer about a record that
is genuinely and correctly held — and an empty answer is worse than no feature,
because the one audience for this list is a supervisory authority asking the
controller to substantiate a retention claim.

Each CHECK constraint blocks a way the feature fails quietly rather than loudly.
A row with a restriction but no deadline is never selected by the expiry sweep:
hidden from every read path, immutable, and never erased — a permanently
invisible record, produced by one forgotten assignment. A window that closed
before it opened erases immediately, which looks exactly like the feature
working.

The refusal is a 423 rather than a 409 because a 409 tells a caller the row moved
under them and invites a retry, and nothing about this refusal clears until a
date.

## What it binds in this repository

- `backend/migrations/core/0288_activity_retention_class.up.sql` adds
  `retention_class`, `retention_class_at`, `restricted_at`, `restricted_reason`,
  `restricted_until` and `redacted_fields` to `activity`, with the CHECK
  constraints `activity_retention_class_known`,
  `activity_retention_class_stamped`, `activity_restriction_complete`,
  `activity_restriction_window` and `activity_restriction_needs_class`, plus the
  partial index `idx_activity_restricted_until` matching the sweep's predicate.
- `backend/migrations/core/0289_activity_restriction_guard.up.sql` holds the
  `BEFORE UPDATE OR DELETE` trigger and creates `activity_retention_evidence`
  with `ON DELETE SET NULL` on the deal reference, the frozen `deal_name`, the
  unique index over activity, deal, name and basis, and the constraint that a
  controller pin carries both a deciding name and a reason.
- `backend/migrations/core/0290_restriction_lift_must_erase.up.sql` closes the
  hole where a bare update clearing `restricted_at` would lift the obligation
  and leave the body readable.
- `backend/migrations/core/0287_audit_restriction_verbs.up.sql` adds `restrict`
  and `pin` to `audit_log_action_check` beside `release` (added by `0243`) and
  `expire` (added by `0253`); the same values are declared on the
  `AuditLogEntry.action` enum in `backend/api/crm.yaml`.
- `backend/internal/modules/privacy/retention_floor.go` holds the floor
  predicate in one place, shared by the retention selectors and the person-erase
  cascade.
- `backend/internal/modules/privacy/erasure_restrict.go` is the restriction
  step; `erasuretimeline.go` is the destroy step. The two select by the same
  floor negated on one side, so a row is exactly one of erased or held.
- `backend/internal/modules/privacy/retentionrestricted.go` and
  `retentionposture.go` carry the expiry sweep and the posture rule.
- `backend/internal/modules/activities/retentionstamp.go` writes the stamp when
  it is earned.
- `backend/internal/modules/privacy/handlers_restriction.go` exposes the list,
  the release and the pin; `restrictedlist.go` reads the evidence table to name
  every qualifying transaction, and `restrictionoverride.go` handles release and
  pin.
- `backend/internal/modules/privacy/sar.go` and `sarmessages.go` keep the
  subject's own access export reaching records restricted about them;
  `sar_integration_test.go` covers it.
- `backend/internal/modules/privacy/erasure_tombstone.go` and the
  `erasure_suppression` table created in
  `backend/migrations/core/0010_consent_retention.up.sql` hold the suppression
  list the restore already replays.
- `ErrRetentionHold` is in the fixed sentinel registry at
  `backend/internal/shared/apperrors/apperrors.go`.
- `comms_outbound` — the delivery row storing recipients, subject and body a
  second time — is created in
  `backend/migrations/core/0136_comms_outbound.up.sql` and is redacted alongside
  its activity.
- The German floors live in `extensions/de/de.go`: commercial correspondence six
  years, accounting records eight, both anchored at calendar-year end.
- `backend/internal/compose/integration/erasure_restriction_integration_test.go`
  proves the behaviour, including
  `TestErasureRestrictsAHandelsbriefInsteadOfDestroyingIt`,
  `TestARestrictedRowRefusesEveryOrdinaryWrite`,
  `TestARestrictedRowLeavesEveryOrdinaryReadPath`,
  `TestAControllerReleasesAHeldRecordByErasingIt`,
  `TestALegalHoldOutranksAControllerRelease`,
  `TestAControllerPinsCorrespondenceTheDerivationCannotSee` and
  `TestExpiredRestrictionCompletesTheSuspendedErasureUnderRetainOnly`.
- `TestStatutoryFloorShieldsCorrespondenceFromDestruction` in
  `backend/internal/compose/integration/retention_jurisdiction_integration_test.go`
  pins the boundary: a 400-day-old email survives, a note of the same age is
  erased.

## What is not built

**No pre-erasure preview.** The source decision called for an erasure to list
the correspondence it is about to erase, so a controller could pin before the
records are gone. That preview does not exist.

**Supplier and purchasing correspondence is not found automatically.** It
qualifies under the statute but has no deal in this product to hang off, so no
rule here reaches it. The pin operation exists for that case. An installation
whose supplier correspondence matters materially should say so in its own
retention concept rather than rely on the product to infer it.

**The restore replay is a documented obligation, not a routine.** The erasure
suppression list is replayed on restore. The restriction state and the
redactions are not — that replay lives in the disaster-recovery drill rather
than in this tree.

## History

Adopted from the retired specification, decided 2026-08-16 and 2026-08-17.
Rewritten in plain language 2026-08-19.

This record was three: the behaviour, the erasure guarantee it forced elsewhere,
and the storage shape. They were merged 2026-08-19 because none of the three
could be changed without changing the other two, and three records describing
one decision invite a reader to satisfy one and miss the rest. ADR-0117 remains
separate — it governs the published event contract, which subscribers outside
this repository read.

The duplication of the deal name in the evidence table is deliberate rather
than a normalization mistake, and the reason is written into the migration
comment so a later reader does not tidy it away.
