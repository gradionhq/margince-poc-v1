# ADR-0070 — Webhook payloads are their own public contract, and fan-out fails closed

**Status:** Active

**Decided:** 2026-07-23

## The decision

The payload a webhook receiver gets is a separate contract from the REST API.
It lives in its own OpenAPI file holding schemas only, and both the Go structs
and the frontend types are generated from it. The REST contract's own
`webhooks:` block is not used.

The envelope on the wire carries only what an outside receiver may see: the
event id, its type and version, when it happened, a reference to the entity, a
coarse actor type, a correlation id for support tracing, and the typed payload.
The internal audit-log id, causation id, passport id, on-behalf-of principal and
workspace id are projected away rather than omitted when empty.

Delivery is signed on the Standard Webhooks scheme: `webhook-id`,
`webhook-timestamp` and `webhook-signature`, with the HMAC computed over the id,
the timestamp and the body joined by dots. The timestamp is fresh on every
attempt, which is the replay defence.

Payloads version additively. Within a major version only optional fields may be
added. A rename, a removal or a change of meaning ships as a new event type the
integrator opts into, while the old type keeps delivering until it is
deprecated.

Fan-out never delivers what the subscription owner could not read. A row-scoped
subject requires both the object-level read grant and the row scope — the same
two halves the record read path checks. An approval event is gated on whether
the owner could read the approval's target. A subject that matches no rule is
denied.

## Why

The delivery engine shipped before the payload contract existed, so it
marshalled the internal event envelope straight onto a public wire. That put
governance metadata in front of external receivers, and it meant any internal
envelope change was an unannounced breaking change for every integrator.

The signing scheme had the same shape of problem. It borrowed the `whsec_`
convention without matching the standard, so off-the-shelf verifier libraries
did not work, and it had no replay defence at all.

The fan-out gate had a real leak. Checking only the row scope, without the
object-level read grant, delivered to an owner who still held a leftover row
scope entry after their read permission was taken away.

Keeping the schemas in a separate file was forced by tooling: the code
generator silently prunes a `webhooks:` block, and turning pruning off in the
REST contract trips a pre-existing name collision.

## What it binds in this repository

- `backend/api/public-events.yaml` is the payload contract. Its own description
  names the five internal fields kept out, so a schema and code mismatch fails
  to compile instead of leaking.
- `backend/tools/gen-payloads` generates
  `backend/internal/contracts/publicevents_gen.go` from that file.
- `backend/internal/modules/webhooks/wireenvelope.go` builds the public
  envelope; `envelope_contract_test.go` and `payload_version_test.go` hold the
  projection and the additive-only rule. `signing.go` implements the scheme.
- `backend/internal/modules/webhooks/deliveryvisibility.go` is the fan-out gate.
  Its `workspaceLevelEntities` is an allow-list, and its comment states the
  default: a subject with no listed rule and no row-scope probe is denied.
- `backend/internal/modules/webhooks/approvalvisibility.go` gates approval
  events on the target's readability, applying the object grant once above every
  arm and the row rule per arm.
- Some subscribable events are deliberately not delivered yet, tracked as
  `deferredDeliveryEvents` in `deliveryvisibility.go` — a missing delivery
  rather than a fan-out to everyone, while the ownership question behind each
  is settled.

## History

Adopted from the retired specification, decided 2026-07-23. Rewritten in plain
language 2026-08-19.

This record superseded three earlier rules: the body-only signature scheme, the
promise that a breaking payload change would ship as a new major version, and a
24-hour grace window for rotating a signing secret. Rotation grace is deferred
rather than removed; a single-entry signature list is valid under the standard,
so adding a second entry later breaks nothing.
