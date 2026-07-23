# Outbound webhooks — the governed egress surface & delivery engine

`internal/modules/webhooks` (E10/S-E10.6, A51, ADR — B-E10.13a-c + B-E10.15, contract-first Phase 4)
is Margince's **first-party** outbound integration surface: a workspace registers an HTTPS target + a
subset of the published event catalog, and a delivery worker fans matching domain events to it as
[Standard Webhooks](https://www.standardwebhooks.com/)-signed HTTP POSTs — retried with exponential
backoff, parked in a dead-letter store, and replayable on demand. It is *first-party* (subscriptions
live in the workspace), not a third-party app marketplace, and it is **outbound only** — this is not an
inbound receiver (features/04 §3).

Every payload on the wire is generated from a dedicated public OpenAPI contract
(`api/public-events.yaml`, §3 below) rather than hand-shaped at each emit site, and every entry point in
`store.go`/`delivery.go` is reachable from `internal/compose`'s HTTP surface **and** from the Settings →
Integrations tab in the frontend (§9) — a subscription can be created, re-targeted, paused, archived,
rotated, and its deliveries inspected/replayed without leaving the UI.

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
              webhook_delivery row              deliverOnce: sign (Standard Webhooks) + POST
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

Each subscription carries a per-subscription signing secret (`whsec_` + 32 random bytes as **standard**
base64 — the Standard Webhooks compatibility requirement), used to HMAC-SHA256 each delivery attempt.
The data model mandates it is **never stored plaintext**, naming the column a "vault ref". The PoC has
no vault, so **the deployment key IS the vault**: an AES-256-GCM envelope over the secret, keyed by
`MARGINCE_WEBHOOK_KEY` (`cipher.go`).

```text
create/rotate:  generateSecret() → "whsec_…"  ──seal(key)──▶  signing_secret_ref (base64 nonce‖ciphertext)
                       │                                                    │
                returned to caller ONCE                              stored, never re-shown
                                                                            │
delivery:       Sign(open(ref), id, ts, body) ──▶  webhook-signature: v1,<base64 HMAC>
```

The plaintext exists in exactly two places and nowhere else: the create/rotate HTTP response, and
transiently in the delivery signer (`open`ed per attempt). A wrong-length key is a **loud boot error**,
never silently padded — a secret sealed under a guessable key is a security defect, not a degraded
feature. A ciphertext that fails to open (wrong key, tamper) is surfaced, never treated as an empty
secret (signing with an empty secret would ship an attacker-forgeable signature). Likewise, an
undecodable secret (corrupt base64) is a real, surfaced `error` from `Sign` — never silently keyed with
the raw prefixed string.

**Rotation is immediate.** `RotateSecret` mints and seals a new secret and returns the plaintext once;
the prior secret stops verifying at once, so a receiver must adopt the new value. The rotation is
audited **without recording either secret value**. A secret minted before this scheme's migration was
URL-safe base64 and can no longer decode under the standard alphabet — it stops signing, by design;
the fix is a fresh subscription or a rotation (`how-to/register-a-webhook.md` §2, legacy note). The wire
signing scheme itself (headers, HMAC construction) is documented once, in §3b below.

## 3. The contract-first payload pipeline

Every event a subscriber can receive is defined once, as data, and compiled — never hand-shaped at the
emit site.

**Why a separate file.** `backend/api/crm.yaml` is the REST contract, and OpenAPI 3.1's native
`webhooks:` block would be the obvious place for this — except `kin-openapi` (the validator the rest of
the contract pipeline runs on) silently prunes a `webhooks:` block on load, and enabling its global
skip-prune option trips a pre-existing `ApprovalToken` schema-name collision elsewhere in `crm.yaml`.
Rather than work around either, the public event contract lives in its own file,
**`backend/api/public-events.yaml`** — components-only (no paths, no `webhooks:` block), so nothing
about it needs pruning:

- **`SubscribableEventType`** — an enum intended to name every event type a subscription may select.
  **As shipped it is narrower than the runtime catalog**: `validateEventTypes` (`store.go`) actually
  gates a create/update against `events.Types()` minus the pipeline class (63 types), while the enum
  lists only 57 — missing the `approval.*`/`coldstart.*` family and `audit.appended`, each of which
  *does* have a published `PublicEvent<Event>` schema and *is* accepted and delivered by the API. The
  practical effect: those six types can be subscribed to over curl/REST today, but the frontend's
  event-type picker (§9, generated from this same enum) cannot offer them as a choice. Raised for
  reconciliation; not worked around here.
- **`PublicEventEnvelope`** — the public wire wrapper (§3c below).
- One **`PublicEvent<Event>`** schema per subscribable event (`PublicEventDealStageChanged`,
  `PublicEventPersonMerged`, …), each carrying `x-event-type` / `x-entity-type` / `x-version`
  extensions.

**The generator.** `backend/tools/gen-payloads` reuses the `oapi-codegen` *library* (not its CLI) over
this isolated file and writes `internal/contracts/webhookpayloads_gen.go` (package `crmcontracts`) —
plain generated structs, **plus** two things a stock schema-to-struct generator doesn't give you: for
every schema carrying `x-event-type`/`x-entity-type`, an `EventType()` / `EntityType()` method pair, and
a package-level `PublicEventVersions` registry mapping every such event type to its `x-version`. The
generator is config-driven (`gen-payloads/config.go`) — a "group" is one source file → one output
package, nothing in the generator itself is webhooks-specific, so a second isolated contract (should one
ever exist) reuses the same tool. Regenerate with `pnpm gen:events`'s Go counterpart, wired into
`make gen`; the frontend gets its own typed projection via `openapi-typescript` into
`frontend/src/api/public-events.ts` (`pnpm gen:events`, chained after `pnpm gen:api`).

**The compile-time guarantee.** Every emit site calls a typed seam, never `events.Emit` with a raw
`map[string]any`:

```text
storekit.EmitEvent(ctx, tx, auditID, entityID, payload)              // payload's own EventType()/EntityType()
storekit.EmitEventForEntity(ctx, tx, auditID, entityType, entityID, payload)  // dynamic-entity events
```

`EmitEvent` derives the event type and entity type FROM the payload struct (`payload.EventType()`,
`payload.EntityType()`) — a call site cannot pair `PublicEventDealCreated` with `person.created`
without the code failing to *compile*, not just failing a test. `EmitEventForEntity` is the same
guarantee for the handful of dynamic-entity types (`mirror.*`, `consent.changed`, `retention.applied`)
whose subject is a runtime value the caller resolves rather than the payload's static type. This was
proven, not assumed: renaming a schema field breaks the Go build, because the generated struct's field
literally disappears out from under every call site that references it. There are exactly two emit
paths in the codebase: `storekit` (every CRUD module) and `approvals.Service.emit` (the
approval/coldstart family, which stages `entity_type: "approval"` unconditionally rather than deriving
it, since a staged approval's entity is always itself).

### 3a. Versioning — additive-only, or a new name

A payload's `x-version` may only grow by **addition**: a new optional field, never a renamed, removed,
or re-typed one. `PublicEventVersions` and `events.VersionOf` are the two ends of the same fact
(pinned equal by the root fitness test `backend/publicevents_test.go`), and `payload_version_test.go`'s golden wire snapshots
(`testdata/wire/<type>.v<n>.json`) ratchet the shape byte-for-byte — a field rename or removal changes
the marshaled bytes and fails the snapshot comparison, forcing a reviewed, deliberate regeneration
(`UPDATE_SNAPSHOTS=1`) rather than letting a breaking change slip out unnoticed.

A genuinely breaking change (a field's *meaning* changes, not just its presence) ships as a **new event
type name**, never an in-place schema mutation of an existing one — the same discipline `deal.updated`
vs. `deal.owner_changed` already uses today (owner reassignment gets its own event rather than riding
inside `deal.updated`'s open `changed_fields`). A subscriber opts in explicitly by adding the new type to
`event_types`; nothing changes underneath an existing subscription.

**A replay carries its original version verbatim.** A `webhook_delivery` row stores its marshaled wire
body at enqueue time; retry and replay re-send that *stored* body forever, never re-render it against
the payload schema's current version. So a delivery enqueued under `deal.stage_changed` v1 replays as v1
even after the schema has (additively) grown to v2 — the receiver that verified it once verifies the
same bytes again.

### 3b. The wire contract (unchanged from the pilot)

Delivery follows [Standard Webhooks](https://www.standardwebhooks.com/) — the scheme Anthropic, OpenAI,
Stripe, and Svix share. A receiver verifies `webhook-signature` against
`{webhook-id}.{webhook-timestamp}.{raw request body}` using its stored secret's **decoded bytes** as the
HMAC key, and dedupes on `webhook-id`:

| Header | Value |
|---|---|
| `X-Margince-Event` | convenience only — the event type (e.g. `deal.stage_changed`) |
| `webhook-id` | the delivery id — stable across retries, the receiver's dedupe key |
| `webhook-timestamp` | unix seconds the CURRENT attempt was signed at — fresh every attempt, never reused across retries |
| `webhook-signature` | `v1,` + base64 HMAC-SHA256 of `{webhook-id}.{webhook-timestamp}.{body}` |

The fresh-per-attempt timestamp is the replay defense: a receiver enforcing a tolerance window (the
Standard Webhooks spec suggests ~5 minutes; Margince does not enforce one itself — that is the
*receiver's* obligation) rejects a captured signature replayed later, even though `webhook-id` (and the
body) stay identical across retries and replay. `v1,` names the scheme version so a future rotation to
another MAC is distinguishable on the wire — today it is always a single-entry list (multi-secret
rotation grace is deferred, A4); `whsec_` marks the secret so a leaked string is identifiable (mirroring
the passport-token convention). See [how-to/register-a-webhook.md §3](../how-to/register-a-webhook.md)
for a verification snippet.

### 3c. The public envelope — what a subscriber actually receives

`toWireEnvelope` (`internal/modules/webhooks/wireenvelope.go`) maps the INTERNAL bus envelope (the shape
every module publishes to the outbox, carrying full governance metadata) onto the PUBLIC
`PublicEventEnvelope` a subscriber receives — deliberately different shapes:

```json
{
  "event_id": "…", "type": "deal.stage_changed", "version": 1,
  "occurred_at": "2026-07-22T09:30:00Z",
  "actor": { "type": "human" },
  "entity": { "type": "deal", "id": "…" },
  "correlation_id": "…",
  "data": { "to_stage_id": "…", "from_status": "open", "to_status": "won", "win_probability": 100 }
}
```

Internal-only fields are **dropped, not merely omitted-when-empty**: `audit_log_id`, `causation_id`,
`passport_id`, `on_behalf_of`, and `workspace_id` never leave the process — a subscriber has no way to
learn the workspace's internal id, which agent passport (if any) drove the change, or the causation
chain. `actor` is reduced from the internal envelope's full principal (type + id + passport + on-behalf-
of) down to `type` alone (`human` / `agent` / `system` / …) — enough to say who acted, nothing that
identifies them. This mapping runs ONCE, at enqueue time, against a freshly observed bus event; a
delivery's stored body is never re-mapped on replay (§3a).

## 4. The delivery state machine

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

## 5. The fan-out and the owner-scope gate (BYO-EVT-4)

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

**The visibility map is an allow-list, fail-closed** (`entityVisibleTo`, `deliverystore.go`). It
classifies by EVENT type first (a deferred-delivery subject's runtime `object_class` would otherwise
collide with a row-scoped entity name below), then by entity type:

| Subject class | How it's scoped |
|---|---|
| `person`, `organization`, `deal`, `lead`, `voice_profile` | probed against the owner's live row scope (`auth.EnsureVisible`) |
| `activity`, `signal` | probed via their bespoke link-walk / resolver gates |
| `offer` | inherits its **parent deal's** scope (an offer carries no owner of its own) |
| `pipeline`, `stage`, `approval`, `audit`, `user`, `passport`, `onboarding_wizard_state`, `incumbent_connection` | genuinely ownerless workspace/admin-level facts (`workspaceLevelEntities`) — deliver to any live owner. `role.changed` and the `user.*` lifecycle both name entity `user`; the cold-start echoes name `approval`; there is no separate `role`, `coldstart`, or `mirror` key. |
| a ratified **deferred-delivery** subject (below) | ratified **not delivered** — an explicit decision, distinct from the fail-closed default |
| **anything else** | **DENIED** (the `default` branch) — fail-closed |

The point of the explicit deny default: adding a new subscribable event whose subject is row-scoped
*forces* you to add a probe — it can never silently inherit fan-out-to-everyone. Adding one that is
genuinely ownerless *forces* you to add it to the allow-list. The choice is forced, never defaulted.

**Ratified deferred delivery — subscribable but not delivered, on purpose.** Two families are
catalogued, valid subscription targets, and match `matchingSubscriptions`, yet `entityVisibleTo` returns
"not visible" for them **unconditionally** — not a bug, a documented gap raised upstream for spec
reconciliation (`.tmp/webhooks-contract-ui/UPSTREAM-P3.md`), because neither has an ownership model the
fan-out gate can bound delivery by:

- **The overlay `mirror.*` family** (`mirror.conflict`, `mirror.budget_degraded`, `mirror.deleted`, the
  reserved `mirror.write_rejected`) — keyed by EVENT type (`deferredDeliveryEvents`). Each emit site
  stamps the diverged record's *runtime* canonical class (`rec.ObjectClass` / `ref.Type` /
  `del.ObjectClass` — e.g. `"person"`, `"deal"`) as the envelope's entity type, but the entity id is a
  mirror-synthetic key or a pre-materialization ref, **not** a live record id the owner's grants can be
  probed against. Classifying by entity type would either miss (fail-closed by accident) or — for
  `mirror.budget_degraded`, whose ref can be a real record ref — deliver to an owner who must not see
  the record. Neither is acceptable, so the whole family is deferred by event type instead.
- **Two `retention.applied` telemetry subjects** — keyed by ENTITY type (`deferredDeliveryEntities`):
  `ai_call` (the embed-call sweep's traces) and `ai_call_payload` (retained call content). Most
  `retention.applied` subjects (`person`, `lead`, `deal`, `activity`) resolve through the normal
  row-scope probes and ARE delivered; these two are engine telemetry with no owner and no visibility
  probe — delivering them workspace-wide would leak which telemetry rows a retention sweep purged.

A subscriber can select `mirror.conflict` or `retention.applied` today and will simply receive nothing
for these specific subjects — fail-**safe**, not fail-silent: the gap is in `UPSTREAM-P3.md` for the
spec to grow an ownership model these subjects can be scoped by, not worked around here.

**Catalogued, never emitted.** Six schemas exist purely for whole-catalog coverage (`events.Types()` is
completely covered by a `PublicEvent<Event>`, the fitness-test definition of "Phase 4 done") but have
no emit site in the codebase today, so nothing is ever delivered for them regardless of the visibility
gate: `deal.restored`, `person.restored`, `pipeline.archived`, `stage.archived`, `mirror.write_rejected`
(reserved for a branch-2 overlay feature), and `audit.appended` (the audit ledger's own row is
workspace-level and resolved back under the receiver's own scope, so an empty payload would carry no
information a receiver doesn't already have). Each schema's description in `public-events.yaml` says so
explicitly — a subscriber selecting one of these types is not wrong, just early.

**Two identities, kept straight.** A delivery runs under a synthesized `PrincipalSystem` context (the
delivery worker acts as the system over the whole workspace, not as any human) — that's the
*attribution*. But the fan-out is *authorized* against the **owner's** live RBAC — that's the security
subject. Once a delivery is enqueued it carries its frozen payload through retry and replay without
re-checking: those re-send an already-authorized delivery to the owner's own endpoint, they are not a
fresh fan-out.

## 6. The two runtime lanes and where they run

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

## 7. The SSRF guard on the dialer

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

## 8. The 503 key-gate — the honest unconfigured state

Without `MARGINCE_WEBHOOK_KEY` there is no way to seal or open a signing secret, so the surface
degrades **honestly**, never silently:

- **Read paths still work** — list/get/deliveries return metadata (which never includes a secret).
- **Any path that must seal or use a secret returns `503 webhooks_not_configured`** — create, rotate,
  and replay. Never an unsigned fallback, never a guessable-key seal, never a silent no-op.
- **No delivery runs** — the consumer and sweep don't start.

This is the same `ErrNotConfigured` posture the rest of the codebase uses for a capability that needs a
deployment secret: a loud, mapped 503 that names the missing capability.

## 9. The Settings → Integrations UI

The whole config surface (and the delivery-inspection surface) is reachable from the SPA, not curl-only:
Settings → **Integrations** (`frontend/src/screens/webhooks.tsx`) mounts `WebhooksCard`, which drives
every REST verb the config surface (§1) and delivery-inspection surface (§4) expose:

- **List** — the subscription table, rendering `state`, the subscribed `event_types` set (the raw wire
  values — there is no per-type translated label, so showing `deal.stage_changed` verbatim is the
  honest choice), and last-updated. The event-type checklist options come from
  `subscribableEventTypeValues`, the generated runtime array `pnpm gen:events` derives straight from
  `public-events.yaml`'s `SubscribableEventType` enum — never a hand-maintained list in the frontend, so
  a catalog change can't silently drift out of sync with what a subscription may actually select.
- **Create** — a form (`target_url` + a multiselect over the event-type catalog) that surfaces the
  `signing_secret` from the `201` response in a **one-time reveal modal** — the same "shown once, never
  again" contract as the API (§2); closing the modal is the only way past it, so a user cannot create a
  subscription and lose the secret by accident.
- **Pause/resume, re-target, archive, rotate** — pause/resume and re-targeting the `event_types` set run
  through the same edit form (an `If-Match` PATCH under the hood); archive and rotate are confirm-gated
  actions (`ConfirmModal`) that call `DELETE` and `POST …/rotate-secret` respectively — rotate surfaces
  the new secret in the same one-time-reveal chrome as create.
- **Deliveries + dead-letter panel** — a per-subscription deliveries list grouped so dead-lettered rows
  are visually separated from the rest, with a per-row **replay** action (confirm-gated, audited) that
  calls `POST …/replay` and invalidates the query so the row's refreshed status renders immediately.

**The 503 is a UI state, not an error screen.** `useWebhookSubscriptions` reads the response status
directly (`response.status === 503`) rather than the generic error channel, and the card renders the
honest "not enabled on this deployment" empty state instead of a generic failure — the same "deliberate,
documented feature-off state, never an error" posture §8 describes for the API. `WebhooksCard` also
gates create/rotate/replay controls behind the same RBAC the API enforces (`canConfigureAutomations` /
admin-ops posture), so a viewer with read-only access sees the list and deliveries but not the mutating
affordances.

## Rules of thumb

- **The wire payload is generated, never hand-shaped.** `api/public-events.yaml` → `gen-payloads` →
  `crmcontracts.PublicEvent<Event>`, with `EventType()`/`EntityType()` methods that make an emit site
  mismatch a compile error. Two emit paths only: `storekit.EmitEvent`/`EmitEventForEntity`, and
  `approvals.Service.emit`.
- **Versions grow by addition, never by mutation.** A breaking change is a new event-type name, opted
  into explicitly by adding it to a subscription's `event_types`; a replayed delivery re-sends its
  originally-enqueued body forever, at the version it was stamped with.

- **The signing secret leaves the system exactly once** — at create/rotate. There is no "show secret"
  read; a lost secret is rotated, not recovered.
- **The event-type catalog is the contract.** An unknown type is a 422. Pipeline (`capture.*`) events
  are not subscribable — they name no subject to scope by.
- **Fan-out never escalates.** Delivery is gated at enqueue against the owner's *live* row scope, and
  the visibility map is fail-closed — an unclassified subject type is denied, not delivered. A handful
  of subjects (overlay `mirror.*`, two `retention.applied` telemetry entities) are *ratified* as
  deferred-not-delivered pending an upstream ownership model — subscribable, catalogued, honestly
  undelivered, never a leak.
- **Some catalogued types are never emitted at all** (`deal.restored`, `person.restored`,
  `pipeline.archived`, `stage.archived`, `mirror.write_rejected`, `audit.appended`) — published for
  whole-catalog coverage, not because a code path fires them yet.
- **The owner is server-derived**, never a request field; a principal with no human identity cannot own
  a subscription.
- **`deliverOnce` records the outcome; the sweeper is the recovery.** A persist failure is safe — the
  row's prior state stands and the next scan re-attempts.
- **Delivery is a keyed capability.** No key → read-only surface, 503 on secret paths, no worker lane.

## Where the code lives

| | |
|---|---|
| Subscription CRUD + write shape + RBAC gate | `internal/modules/webhooks/store.go` |
| Delivery state machine + fan-out queries + visibility map + deferred-delivery classification | `internal/modules/webhooks/deliverystore.go` |
| The delivery engine (fan-out, retry sweep, replay, one attempt) | `internal/modules/webhooks/delivery.go` |
| Internal → public envelope mapping (the field-dropping in §3c) | `internal/modules/webhooks/wireenvelope.go` |
| Secret sealing (AES-256-GCM) | `internal/modules/webhooks/cipher.go` |
| Secret minting + HMAC signing + the wire headers | `internal/modules/webhooks/signing.go` |
| The SSRF-guarded delivery client | `internal/modules/webhooks/client.go` |
| HTTP transport (shadows the generated stubs) + error mapping | `internal/modules/webhooks/handlers.go`, `mapping.go` |
| The tables + RLS + indexes | `backend/migrations/core/0113_webhook.up.sql` |
| Compose wiring (key-gate options, the two deliverers) | `internal/compose/webhooks.go` |
| Process-role wiring (consumer + sweep) | `backend/cmd/worker/main.go`, `backend/cmd/api/main.go` |
| The `cg:webhooks` consumer group | `internal/shared/kernel/events/catalog.go` |
| The REST contract | `backend/api/crm.yaml` (`/webhook-subscriptions`) |
| The public payload contract (§3) | `backend/api/public-events.yaml` |
| The payload generator | `backend/tools/gen-payloads/` → `internal/contracts/webhookpayloads_gen.go` |
| The typed emit seam (compile-time payload↔event binding) | `internal/platform/database/storekit/storekit.go` (`EmitEvent`, `EmitEventForEntity`) |
| The whole-catalog fitness gate (coverage/no-orphan/version/delivery-resolvability, A15) | `backend/publicevents_test.go` |
| The upstream-reconciliation note for the two deferred-delivery families | `.tmp/webhooks-contract-ui/UPSTREAM-P3.md` |
| The Settings → Integrations UI (§9) | `frontend/src/screens/webhooks.tsx` |
| The generated frontend event-type projection | `frontend/src/api/public-events.ts` |

## Where to go next

- Registering, verifying, and inspecting a webhook end-to-end: [how-to/register-a-webhook.md](../how-to/register-a-webhook.md).
- What every module owns, including `webhooks`' tables and HTTP surface: [reference/modules.md](../reference/modules.md).
- The outbox → relay → consumer-group bus the delivery worker rides: [write-backbone.md](write-backbone.md).
- Why the owner-scope gate reads *live* RBAC and what row scope means: [authorization.md](authorization.md), [rbac-roles-and-teams.md](rbac-roles-and-teams.md).
- How the REST contract (`crm.yaml`) is generated (the general contract-first pattern `public-events.yaml` follows a variant of): [contract-first.md](contract-first.md).
- Every flag and env var (`--webhook-key`, `--webhook-retry-interval`): [reference/configuration.md](../reference/configuration.md).
