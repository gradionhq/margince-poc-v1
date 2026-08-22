# Every mutation leaves a trace

**A change to a domain row, the audit record of that change, and the event
announcing it commit together or not at all.** One transaction, three rows.

The binding form is [*The write shape*](../../CLAUDE.md#the-write-shape-non-negotiable)
in the rulebook, spelled once in `platform/database/storekit` (`Audit` + `Emit`)
and called by every module store. This page is why it is shaped that way.

## What the single transaction buys

Three failures become impossible rather than unlikely:

- **A change nobody can account for.** If the audit row could fail
  independently, the interesting case — the write that succeeded under
  circumstances someone later asks about — is exactly the one most likely to be
  missing its record.
- **An event that describes a row that does not exist**, or a row that changed
  with no outbox record. Note what the transaction does and does not buy: it
  guarantees the *staging* is atomic — after the commit there is a durable
  outbox row or there is no change at all. Getting that row onto the bus is the
  relay's job, and it is at-least-once with retries, not a delivery guarantee.
  Atomic staging is what makes the dual-write problem go away; monitoring the
  relay is a separate obligation.
- **A caller-supplied actor.** `captured_by` is stamped from the authenticated
  principal, never from the request body. An audit trail whose actor field is an
  input is not a trail.

## The method

**Never write a domain row outside a module store's entry point.** The store
owns the transactional shape. A handler that reaches for SQL has skipped both
the audit and the gate.

**Publishing is ALWAYS through the outbox.** `platform/events.Relay` ships it;
no direct XADD from domain code. The bus is at-least-once, so consumers wrap
handlers in `events.Dedupe` — a consumer that assumes exactly-once is a bug
waiting for a redelivery.

**Trace the request end to end.** The HTTP layer mints one `correlation_id` per
request; `Audit()` returns the audit row id; `Emit()` links both. A trace that
starts at the event has lost the half that says who asked.

**Every store entry point is RBAC-gated**, and the two denials are different
facts:

| Denial | Sentinel | Wire | Why |
|---|---|---|---|
| object denied | `apperrors.ErrPermissionDenied` | 403 | the caller may not perform this verb |
| row out of scope | `apperrors.ErrNotFound` | 404 | existence-hiding — a 403 here would confirm the row exists |

**Anything that returns a record is a read** and carries the row-scope gate —
including replay, conflict and error paths. That last clause is where this rule
is usually broken: an error path that echoes the conflicting row has just served
it.

**A new seam owes the whole shape.** Audit-only mutations do exist and are
legitimate — installation configuration writes an audit row and no event,
because the closed event catalog defines no type for it and inventing one
build-side is forbidden. But they are *enumerated*: `backend/writeshape_test.go`
carries each one with the argument for why, and the gate refuses an audit-only
function that is not on the list — as well as a listed one that no longer
exists. So the rule for a new write path is: audit implies event, unless you can
write the paragraph that earns a place on that list.

## What this does not ask for

- **Not an audit row per read.** Reads are gated, not journalled.
- **Not that evidence tables join the shape.** `raw_capture` is evidence, not a
  domain row; it neither audits nor emits, and that is correct.
- **Not a bespoke event per column.** The envelope is the
  `shared/kernel/events` contract; a new event kind is a catalog change, not a
  free-form payload.
