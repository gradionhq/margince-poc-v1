# 0005 — In-process outbox relay (River deferred)

**Status:** accepted (PoC scope) · 2026-07-03. *Updated 2026-07-04 with
the triad restructure ([0011](0011-triad-restructure.md)): the relay is
`internal/platform/events.Relay`; `cmd/api` runs it inline behind
`--inline-relay` (default true — the decision below stands for dev and
small installs), and `cmd/worker` runs the same relay standalone for
split deployments.*
**Spec refs:** events.md §4.2 (outbox relay as a River job), B-EP04.3/.4

## Decision

The `outbox_relay` runs as an in-process worker goroutine inside `crm
serve` (`internal/bus.Relay`), not as a River job. River itself
(B-EP04.3) is deferred.

## Why deviate

events.md §4.2 names River as the runner. River earns its keep when there
is a *population* of job kinds — capture pipelines, retention sweeps, the
Surface-B scheduler — needing persistence, retries, and scheduling as a
shared substrate. At the PoC's current WP0/WP1 spine the relay is the
only async worker, and it is a **continuous poller, not a queued job**:
it needs no job rows, no per-run retry state, and its durability already
lives in `event_outbox` itself (an unpublished row *is* the retry state).
Pulling in River today would add a dependency, its migration set, and a
second queue table to relay a queue table.

The seam is preserved: the relay's whole contract is "unpublished outbox
rows become stream entries, at-least-once". Re-hosting `Relay.Run` inside
a River periodic job when River lands (first real job family: capture,
WP2) changes the scheduler, not the semantics — same SQL, same XADD, same
crash-safety story.

## Consequences

- `cmd/crm serve` owns the relay's lifecycle (start with the server,
  drain on shutdown).
- Multiple app replicas are safe (`FOR UPDATE SKIP LOCKED` divides the
  backlog; the crash window between XADD and stamp is the documented
  at-least-once duplicate source, absorbed by consumer dedupe).
- Revisit at WP2: when `crm-capture` needs River anyway, move the relay
  onto it and delete the goroutine wiring.
