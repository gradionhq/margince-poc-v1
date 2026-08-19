# ADR-0117 — The retention event's action is a closed set of three values

**Status:** Active
**Decided:** 2026-08-17

## The decision

The `action` field on the `retention.applied` event carries exactly three
values: `archive`, `anonymize` and `erase`. It is declared as a closed set
rather than a free string, so a generated client gets a typed value and a
subscriber that switches on it gets a compile error when the set changes,
instead of a silent default branch.

Tightening a field a subscriber already parses is normally a breaking change,
and it is allowed here only because no producer can emit outside the new set.
That is checkable rather than asserted. There are four places that emit the
event. Three pass a named constant whose value is `erase`. The fourth passes a
retention policy's own action column, which the database already constrains to
those three values and nothing else. The closed set describes what was always on
the wire; the open string was the drift.

A new retention action carrying a new obligation on the subscriber ships as its
own event type, not as a fourth value here. That is why `retention.restricted`
exists separately: the restricted record survives in storage and must not
survive in a projection, which is an obligation no existing action carries, so a
subscriber that has never heard of it must not silently receive it.

Where the event catalog and the public payload contract describe the same
published type, the contract wins on payload shape, and only on payload shape.
A disagreement is a defect in the catalog, fixed there, never resolved by
editing the contract to match a stale description of it. On every other
question — whether an event exists, what emits it, what consumes it, what it
means — the catalog remains the source.

## Why

The catalog and the payload contract described the same event differently, and
had done since before the restriction work. A reader got a different answer
depending on which document they opened, and the generated types followed the
contract, so the catalog was the one that was wrong.

An open string means validation admits anything. A subscriber can receive a
value the catalog says is impossible, and no generated type stops it. Leaving
the field open so future retention actions could be slipped in without a new
event type is exactly the failure this rules against.

## What it binds in this repository

- `retention_policy.action` is constrained to `archive`, `anonymize` and
  `erase` by the CHECK in
  `backend/migrations/core/0010_consent_retention.up.sql`. That constraint is
  what makes the tightening safe.
- The event is declared as `PublicEventRetentionApplied` in
  `backend/api/public-events.yaml`, with `x-entity-type: dynamic` because its
  four emit sites carry four different runtime subjects, and with `policy` and
  `reason` as a union across those sites.
- `backend/internal/modules/privacy/retention.go` and `retentionactions.go`
  drive the policy-configured sweep; `retentionai.go` is the embed-call sweep;
  the erasure path emits from `backend/internal/modules/privacy/erasure.go`.
- The payload shape is pinned by
  `TestRetentionAppliedPayload_ActionOnly`,
  `TestRetentionAppliedPayload_WithPolicy`,
  `TestRetentionAppliedPayload_WithReason` and
  `TestRetentionAppliedEmitUsesRuntimeEntityType` in
  `backend/internal/modules/privacy/retention_payload_test.go`.
- `retention.restricted` is its own published type in
  `backend/api/public-events.yaml`, generated into
  `backend/internal/contracts/publicevents_gen.go` as
  `PublicEventRetentionRestricted`.
- `TestIsDestructiveSpansTheClosedActionSet` in
  `backend/internal/modules/privacy/retention_test.go` holds the action set on
  the Go side.

## History

Adopted from the retired specification, decided 2026-08-17. Rewritten in plain
language 2026-08-19.

The decision is only half implemented, and the gap is stated rather than
glossed. `retention.restricted` exists in the contract and is emitted, and the
database constraint gives the three values their teeth. But the `action`
property in `backend/api/public-events.yaml` is still declared
`type: string`, with the three values written only in its description, so the
generated client still sees a free string. Closing the enum in the contract is
outstanding work.
