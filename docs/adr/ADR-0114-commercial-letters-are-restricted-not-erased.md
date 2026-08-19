# ADR-0114 — Commercial correspondence survives an erasure in restricted form, and only correspondence that earned it

**Status:** Active
**Decided:** 2026-08-16

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
destroyed. It leaves every ordinary read path: lists, timelines, search,
exports, embeddings, agent grounding and agent tools. It becomes immutable in
the database, below every role including admin. It carries the deadline at which
its obligation ends, pinned from the floor in force at that moment, so a later
configuration change never shortens an obligation already recorded. When the
deadline passes, the suspended erasure completes without anybody asking again.

Redaction is per item of data, not per document. The restricted record keeps
what makes it a commercial record — subject, body, the transaction it hangs off,
the attachments. It loses the provider's raw payload, the erased subject's own
address, and the channel identifiers that identify the subject. The same rule
reaches the outbound delivery row behind a sent activity, which stores the
recipients, subject and body a second time.

A controller can list what is held, why, and until when, without seeing the
correspondence. An administrator holding the retention-policy authority may
release a restricted record, which erases it, or pin a record the derivation
could not see. Both require a stated reason and both are audited.

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

## What it binds in this repository

- `backend/migrations/core/0288_activity_retention_class.up.sql` adds `retention_class`, `retention_class_at`, `restricted_at`, `restricted_reason`, `restricted_until` and `redacted_fields` to `activity`, with four CHECK constraints and the expiry sweep's partial index.
- `backend/migrations/core/0289_activity_restriction_guard.up.sql` makes the stamp write-once and the restriction immutable through a `BEFORE UPDATE OR DELETE` trigger, and creates `activity_retention_evidence`.
- `backend/migrations/core/0290_restriction_lift_must_erase.up.sql` closes the hole where a bare update clearing `restricted_at` would lift the obligation and leave the body readable — a lift must carry the erasure with it.
- `backend/migrations/core/0287_audit_restriction_verbs.up.sql` adds `restrict` and `pin` to the `audit_log` action constraint alongside the existing `release` and `expire`.
- `backend/internal/modules/privacy/retention_floor.go` holds the floor predicate in one place, shared by the retention selectors and the person-erase cascade.
- `backend/internal/modules/privacy/erasure_restrict.go` is the restriction step; `erasuretimeline.go` is the destroy step, and the two select by the same floor negated on one side, so a row is exactly one of erased or held.
- `backend/internal/modules/activities/retentionstamp.go` writes the stamp when it is earned.
- `backend/internal/modules/privacy/handlers_restriction.go` exposes the list, the release and the pin; `restrictedlist.go` and `restrictionoverride.go` back them.
- The German floors live in `extensions/de/de.go`: commercial correspondence six years, accounting records eight, both anchored at calendar-year end.
- `backend/internal/compose/integration/erasure_restriction_integration_test.go` proves the behaviour, including `TestErasureRestrictsAHandelsbriefInsteadOfDestroyingIt`, `TestARestrictedRowRefusesEveryOrdinaryWrite`, `TestARestrictedRowLeavesEveryOrdinaryReadPath`, `TestAControllerReleasesAHeldRecordByErasingIt`, `TestALegalHoldOutranksAControllerRelease` and `TestAControllerPinsCorrespondenceTheDerivationCannotSee`.
- `TestStatutoryFloorShieldsCorrespondenceFromDestruction` in `backend/internal/compose/integration/retention_jurisdiction_integration_test.go` pins the boundary: a 400-day-old email survives, a note of the same age is erased.

## History

Adopted from the retired specification, decided 2026-08-16. Rewritten in plain
language 2026-08-19. Three records follow from it: ADR-0115 restates the erasure
guarantee and the restore duty that this decision contradicted, ADR-0116
supplies the storage shape, and ADR-0117 closes the event contract.

One gap is named rather than hidden. Supplier and purchasing correspondence
qualifies under the statute but has no deal in this product to hang off, so no
automatic rule here finds it. The pin operation exists for that case. The
source decision also called for an erasure to list the correspondence it is
about to erase, so a controller could pin before the records are gone; that
preview is not built, and an installation whose supplier correspondence matters
materially should say so in its own retention concept rather than rely on the
product to infer it.
