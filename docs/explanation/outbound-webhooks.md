# Outbound webhooks — the governed egress surface & delivery engine

`internal/modules/webhooks` (E10/S-E10.6, A51, ADR — B-E10.13a-c + B-E10.15) is Margince's
**first-party** outbound integration surface: a workspace registers an HTTPS target + a subset of the
published event catalog, and a delivery worker fans matching domain events to it as HMAC-signed HTTP
POSTs — retried with exponential backoff, parked in a dead-letter store, and replayable on demand. It
is *first-party* (subscriptions live in the workspace), not a third-party app marketplace, and it is
**outbound only** — this is not an inbound receiver (features/04 §3).

For the one-paragraph version see [reference/modules.md](../reference/modules.md); to *register* one,
jump to [how-to/register-a-webhook.md](../how-to/register-a-webhook.md); for the write shape every
mutation commits through and the bus the delivery worker rides, see
[write-backbone.md](write-backbone.md). Read those first if you want the short version.

## The shape at a glance

Two halves: a **config surface** (CRUD on `webhook_subscription`, the RBAC-gated API) and a **delivery
engine** (a bus consumer + a retry sweeper that drive `webhook_delivery` through a state machine). They
meet at the event bus — a subscription is just a row until a matching event arrives.

```text
CONFIG SURFACE (api, RBAC-gated)              DELIVERY ENGINE (worker, or api under --inline-relay)

POST /webhook-subscriptions                   domain write → outbox → relay → Redis
  → seal signing secret (AES-256-GCM)           → cg:webhooks → Deliverer.HandleEvent
  → return plaintext ONCE                              │
        │                                       matchingSubscriptions(event.type)   ← active, type ∈ event_types
   webhook_subscription row                            │
   (target_url, event_types, sealed secret)     ownerCanSee(owner, event.subject)   ← BYO-EVT-4 owner-scope gate
        │                                              │                              (fail-closed; skips, never strands)
        └──────────────┬───────────────────────  enqueueForSubscriptions(visible)   ← idempotent on (ws, sub, event)
                       ▼                                │
              webhook_delivery row              deliverOnce: sign (HMAC-SHA256) + POST
                                                        │
                    ┌───────────────────────────────────┴───────────────────────────┐
                    ▼                          ▼                                       ▼
              2xx → delivered        non-2xx / transport error              6 attempts spent
                                     → retrying (backoff 1,2,4,8,16s)       → dead_lettered
                                            │                                       │
                                     RunRetrySweep (ticker)              POST …/replay (human, audited)
                                     claims due rows, re-attempts        resets budget, re-attempts
```

**Why the two halves are separate.** The config surface can run with the read paths alive even when no
signing key is configured (`§7`); the delivery engine only exists where a key is present. Splitting
them means a subscription can be listed and inspected on any api process, while *delivery* is a
capability a process opts into by holding the deployment key.

---

## 1. The subscription — the config surface

A `webhook_subscription` is integration config, not record data: managing it is governed by the
`webhook_subscription` RBAC object (admin/ops-owned config posture, like quotas), and every entry point
in `store.go` gates on it (`auth.Require` on create/read/update/delete). The store is the classic
**Handlers→Store** CRUD spine — the store owns the transactional write shape and the RBAC gate.

| Field | Rule |
|---|---|
| `target_url` | **HTTPS-only** — `http://` is rejected at create (a cleartext callback is never a safe fan-out target), enforced in three places: the contract `pattern: ^https://`, the store's `strings.HasPrefix`, and a DB `CHECK`. |
| `event_types` | A **non-empty subset of the published catalog** (`events.Types()`, events.md §5). An unknown type is a 422, never a silently-never-delivered rule. Entity-less pipeline events (`capture.*`) are rejected — they name no subject to scope the fan-out by (`§4`). It is a true *set* (`uniqueItems`). |
| `owner_id` | **Server-derived** from the authenticated principal (the acting human, or the human an agent acts on behalf of) — never a request field. A principal with no human identity cannot own integration config. This owner is what the fan-out is scoped to (`§4`). |
| `state` | `active` / `paused` — pausing stops delivery (and holds retries) without archiving. |
| `signing_secret_ref` | The **sealed** secret (`§2`) — never the plaintext, never in any read view. |

Updates run under an optimistic-concurrency guard (`If-Match` → `version`), audit the before/after
image, and reject an empty patch at runtime (422) — the contract advertises `minProperties: 1` and the
REST path honors it rather than committing a no-op mutation. Archive is a soft delete; an archived,
absent, or out-of-workspace subscription reads as `404` everywhere (existence-hiding), and delivery
stops at archive.

**Agent access is 🟡.** A human on a session registers directly. An *agent* principal's create/update
is a 🟡 governed tool (`x-mcp-tool` tier yellow) — registering or widening outbound egress is staged
for human approval (ADR-0036, UC-E10-04 E4) and redeemed with an `X-Approval-Token`. Rotate, replay,
and all reads are human-only.

## 2. The signing secret — sealed at rest, shown once

Each subscription carries a per-subscription signing secret (`whsec_…`, 32 random bytes), used to
HMAC-SHA256 the raw delivery body. The data model mandates it is **never stored plaintext**, naming the
column a "vault ref". The PoC has no vault, so **the deployment key IS the vault**: an AES-256-GCM
envelope over the secret, keyed by `MARGINCE_WEBHOOK_KEY` (`cipher.go`).

```text
create/rotate:  generateSecret() → "whsec_…"  ──seal(key)──▶  signing_secret_ref (base64 nonce‖ciphertext)
                       │                                                    │
                returned to caller ONCE                              stored, never re-shown
                                                                            │
delivery:       payload ──HMAC-SHA256(open(ref))──▶  X-Margince-Signature: sha256=…
```

The plaintext exists in exactly two places and nowhere else: the create/rotate HTTP response, and
transiently in the delivery signer (`open`ed per attempt). A wrong-length key is a **loud boot error**,
never silently padded — a secret sealed under a guessable key is a security defect, not a degraded
feature. A ciphertext that fails to open (wrong key, tamper) is surfaced, never treated as an empty
secret (signing with an empty secret would ship an attacker-forgeable signature).

**Rotation is immediate.** `RotateSecret` mints and seals a new secret and returns the plaintext once;
the prior secret stops verifying at once, so a receiver must adopt the new value. The rotation is
audited **without recording either secret value**.

### The wire contract

A receiver verifies `X-Margince-Signature` against the **raw request body** using its stored secret,
and dedupes on `X-Margince-Delivery`:

| Header | Value |
|---|---|
| `X-Margince-Event` | the event type (e.g. `deal.stage_changed`) |
| `X-Margince-Delivery` | the delivery id — stable across retries, the receiver's dedupe key |
| `X-Margince-Signature` | `sha256=` + hex HMAC-SHA256 of the raw body under the secret |

The `sha256=` scheme prefix names the MAC so a future rotation to another algorithm is distinguishable
on the wire rather than an ambiguous hex blob; `whsec_` marks the secret so a leaked string is
identifiable (mirroring the passport-token convention).

## 3. The delivery state machine

One `webhook_delivery` row is created per `(workspace, subscription, event)` — a unique key, so a
redelivered bus event (the bus is at-least-once) conflicts and yields no new row: **it never
double-POSTs**. The signed body is kept verbatim on the row (`payload`) so a parked delivery can be
replayed after the bus stream has trimmed the source event.

```text
 pending ──attempt──▶ delivered           (2xx)
    │
    └──attempt fails──▶ retrying           (non-2xx or transport error, budget left)
                          │                 next_retry_at = now + backoff(attempts)
                          │ RunRetrySweep claims due rows
                          └──attempt fails, budget spent──▶ dead_lettered   (6 attempts)
                                                                │
                                                          replay (human) ──▶ pending (fresh budget)
```

- **Budget & backoff** (`delivery.go`): 6 total attempts; the gap after `n` failures is exponential
  `1s, 2s, 4s, 8s, 16s`, capped at 32s (the cap never binds within the budget — it guards a future
  budget increase). Timestamps come from an **injected clock** so the schedule is deterministic under
  test (no sleeps).
- **The sweeper** (`RunRetrySweep` / `SweepOnce`): a ticker claims a bounded batch (128) of `retrying`
  rows whose `next_retry_at` has elapsed **and whose subscription is still active** — a paused
  subscription's retries wait until it resumes. One tenant's scan failure is logged and skipped, never
  allowed to starve the fleet. `SweepOnce` is exposed so a test can step the schedule under the
  injected clock.
- **`deliverOnce` never returns an error** — the outcome IS the record. A failure to *persist* the
  outcome is logged; the row's prior state is safe and the next sweep re-scans it.
- **Replay** (`POST …/replay`): a human action — RBAC-gated, existence-hiding, and audited to the
  acting human *before* the re-attempt. It resets attempts to a fresh budget (the operator is asserting
  the endpoint is fixed, so the exponential clock restarts). It **refuses with a 503 up front** when no
  signing key is configured — never a silent reset that leaves the row mis-stated.

`loadTarget` rehydrates a delivery from the *subscription's current* target URL and sealed secret, so a
rotation or re-target between attempts takes effect on the next try.

## 4. The fan-out and the owner-scope gate (BYO-EVT-4)

This is the crux, and the part reviewers guarded hardest: **a webhook must never become a
privilege-escalation channel.** A subscription owned by a rep who can only see their own deals must not
receive an event about a deal they could never read in the UI.

`HandleEvent` runs three steps, in the event's workspace under the tenant GUC:

1. **`matchingSubscriptions(event.type)`** — active, non-archived subscriptions whose `event_types`
   contain this type. The candidate set, *before* any visibility filter.
2. **`ownerCanSee(event, owner)`** — for each candidate, resolve the owner's **live** RBAC through the
   `authz.Resolver` seam and probe whether the event's subject entity is within that principal's row
   scope. This is the gate at **enqueue time**: a revocation that lands before the event stops
   delivery. One owner's resolver/visibility failure is logged and skipped (fail-closed for *that*
   subscription) — it never strands the rest of the fan-out; the bus redelivery re-evaluates.
3. **`enqueueForSubscriptions(visible)`** — create a pending delivery per visible subscription
   (idempotent), then attempt each immediately.

**The visibility map is an allow-list, fail-closed** (`entityVisibleTo`, `deliverystore.go`):

| Subject class | How it's scoped |
|---|---|
| `person`, `organization`, `deal`, `lead`, `voice_profile` | probed against the owner's live row scope (`auth.EnsureVisible`) |
| `activity`, `signal` | probed via their bespoke link-walk / resolver gates |
| `offer` | inherits its **parent deal's** scope (an offer carries no owner of its own) |
| `pipeline`, `stage`, `approval`, `audit`, `user`, `role`, `passport`, `onboarding`, `mirror`, `incumbent`, `coldstart` | genuinely ownerless workspace/admin-level facts — deliver to any live owner |
| **anything else** | **DENIED** (the `default` branch) |

The point of the explicit deny default: adding a new subscribable event whose subject is row-scoped
*forces* you to add a probe — it can never silently inherit fan-out-to-everyone. Adding one that is
genuinely ownerless *forces* you to add it to the allow-list. The choice is forced, never defaulted.

**Two identities, kept straight.** A delivery runs under a synthesized `PrincipalSystem` context (the
delivery worker acts as the system over the whole workspace, not as any human) — that's the
*attribution*. But the fan-out is *authorized* against the **owner's** live RBAC — that's the security
subject. Once a delivery is enqueued it carries its frozen payload through retry and replay without
re-checking: those re-send an already-authorized delivery to the owner's own endpoint, they are not a
fresh fan-out.

## 5. The two runtime lanes and where they run

Delivery is a background capability, gated on the deployment signing key:

- **`cmd/worker`** runs the `cg:webhooks` consumer + the retry sweep whenever `--webhook-key` /
  `MARGINCE_WEBHOOK_KEY` is set. Unset, the delivery worker stays off entirely.
- **`cmd/api` under `--inline-relay`** (the default single-process dev/small-deploy shape) runs the
  same consumer + sweep inline, on the in-process relay group, when the key is set.
- Either lane carries the identity-backed `authz.Resolver` for the owner-scope gate; the HTTP-transport
  deliverer that serves **replay only** needs no resolver (replay re-sends an already-authorized
  delivery, it never fans out).

The retry-sweep tick is `--webhook-retry-interval` (default `5s`). See
[reference/configuration.md](../reference/configuration.md) for the full flag/env table.

## 6. The SSRF guard on the dialer

A `target_url` is tenant-supplied, so delivery is a classic SSRF surface. The production client
(`NewGuardedClient`, `client.go`) dials a target **only if it resolves to a public address**, checked
post-DNS on the concrete IP so a DNS-rebind cannot bypass the guard (`netguard.RefusePrivate`). Every
redirect hop re-enters the same guarded dialer, and the chain is capped (5 hops). One attempt is bounded
end-to-end (10s), and the receiver's response body is read only capped (8 KiB, discarded) — a hostile
endpoint cannot exhaust memory by streaming forever, nor pin a worker goroutine.

`HTTPDoer` is the transport seam: production wires the guarded client; tests inject a
loopback-permitting one, because netguard by design refuses the `127.0.0.1` an `httptest` receiver
listens on. The guard itself is pinned by a dedicated SSRF test on `NewGuardedClient` — the seam is for
testability, not a way to disable the guard in production.

## 7. The 503 key-gate — the honest unconfigured state

Without `MARGINCE_WEBHOOK_KEY` there is no way to seal or open a signing secret, so the surface
degrades **honestly**, never silently:

- **Read paths still work** — list/get/deliveries return metadata (which never includes a secret).
- **Any path that must seal or use a secret returns `503 webhooks_not_configured`** — create, rotate,
  and replay. Never an unsigned fallback, never a guessable-key seal, never a silent no-op.
- **No delivery runs** — the consumer and sweep don't start.

This is the same `ErrNotConfigured` posture the rest of the codebase uses for a capability that needs a
deployment secret: a loud, mapped 503 that names the missing capability.

## Rules of thumb

- **The signing secret leaves the system exactly once** — at create/rotate. There is no "show secret"
  read; a lost secret is rotated, not recovered.
- **The event-type catalog is the contract.** An unknown type is a 422. Pipeline (`capture.*`) events
  are not subscribable — they name no subject to scope by.
- **Fan-out never escalates.** Delivery is gated at enqueue against the owner's *live* row scope, and
  the visibility map is fail-closed — an unclassified subject type is denied, not delivered.
- **The owner is server-derived**, never a request field; a principal with no human identity cannot own
  a subscription.
- **`deliverOnce` records the outcome; the sweeper is the recovery.** A persist failure is safe — the
  row's prior state stands and the next scan re-attempts.
- **Delivery is a keyed capability.** No key → read-only surface, 503 on secret paths, no worker lane.

## Where the code lives

| | |
|---|---|
| Subscription CRUD + write shape + RBAC gate | `internal/modules/webhooks/store.go` |
| Delivery state machine + fan-out queries + visibility map | `internal/modules/webhooks/deliverystore.go` |
| The delivery engine (fan-out, retry sweep, replay, one attempt) | `internal/modules/webhooks/delivery.go` |
| Secret sealing (AES-256-GCM) | `internal/modules/webhooks/cipher.go` |
| Secret minting + HMAC signing + the wire headers | `internal/modules/webhooks/signing.go` |
| The SSRF-guarded delivery client | `internal/modules/webhooks/client.go` |
| HTTP transport (shadows the generated stubs) + error mapping | `internal/modules/webhooks/handlers.go`, `mapping.go` |
| The tables + RLS + indexes | `backend/migrations/core/0113_webhook.up.sql` |
| Compose wiring (key-gate options, the two deliverers) | `internal/compose/webhooks.go` |
| Process-role wiring (consumer + sweep) | `backend/cmd/worker/main.go`, `backend/cmd/api/main.go` |
| The `cg:webhooks` consumer group | `internal/shared/kernel/events/catalog.go` |
| The contract | `backend/api/crm.yaml` (`/webhook-subscriptions`) |

## Where to go next

- Registering, verifying, and inspecting a webhook end-to-end: [how-to/register-a-webhook.md](../how-to/register-a-webhook.md).
- What every module owns, including `webhooks`' tables and HTTP surface: [reference/modules.md](../reference/modules.md).
- The outbox → relay → consumer-group bus the delivery worker rides: [write-backbone.md](write-backbone.md).
- Why the owner-scope gate reads *live* RBAC and what row scope means: [authorization.md](authorization.md), [rbac-roles-and-teams.md](rbac-roles-and-teams.md).
- Every flag and env var (`--webhook-key`, `--webhook-retry-interval`): [reference/configuration.md](../reference/configuration.md).
