# Register an outbound webhook

Register an HTTPS endpoint to receive signed, retried deliveries of published domain events, then
verify the signature, inspect deliveries, and replay a parked one. For the mental model (the config
surface vs. the delivery engine, secret sealing, the contract-first payload pipeline, versioning, the
retry/dead-letter state machine, the owner-scope fan-out gate), read
[explanation/outbound-webhooks.md](../explanation/outbound-webhooks.md) first.

Everything below also works from the UI: **Settings → Integrations** drives the same create / pause /
resume / re-target / archive / rotate / replay actions this guide curls, plus a deliveries and
dead-letter panel — use whichever is convenient, they hit the same API.

**First-party, outbound only.** This registers a *subscription* Margince delivers to — it is not an
inbound receiver and not a third-party app install. Deliveries are signed HTTP POSTs of a **typed,
contract-generated** payload (`backend/api/public-events.yaml` → `internal/contracts`, one
`WebhookPayload<Event>` schema per event type) on the [Standard Webhooks](https://www.standardwebhooks.com/)
scheme — the same convention used by Anthropic, OpenAI, Stripe, and Svix — so any off-the-shelf SW
verifier library works unmodified.

> **Single-organization installation (ADR-0061/A107).** One installation serves one organization; the
> server resolves its singleton organization itself, so no request selects a tenant — there is no
> `X-Workspace-Slug` header. The `curl`s below carry only the session cookie. ("Workspace" still names
> the internal RLS tenant these tables are scoped by.)

## Prerequisites

- **Admin or ops RBAC.** Managing subscriptions is organization-wide integration config (the same
  posture as quotas), gated `admin`/`ops`-only; every role may *read* a subscription and its
  deliveries.
- **A deployment signing key must be configured** — `MARGINCE_WEBHOOK_KEY` (see step 1). Without it the
  read surface still lists, but create/rotate/replay answer `503 webhooks_not_configured` and no
  delivery runs.
- **An HTTPS endpoint you control** that can verify an HMAC and return `2xx`. Plain `http://` targets
  are rejected at create.

## 1. Configure the deployment signing key

The key seals every subscription's signing secret at rest (AES-256-GCM) — it is a **base64-encoded
32-byte** key, shared by the api and the delivery worker. Mint one:

```sh
openssl rand -base64 32
```

Set it on both the api and the worker before boot (`--webhook-key` or `MARGINCE_WEBHOOK_KEY`). A
wrong-length key is a **boot error**, never silently padded. Rotating this deployment key re-seals
nothing automatically — treat it as long-lived; it is the per-subscription secret (step 5) you rotate
operationally, not this one.

```sh
export MARGINCE_WEBHOOK_KEY="$(openssl rand -base64 32)"   # then (re)start the api + worker
```

In `make dev` the single-process api runs delivery inline (`--inline-relay`); in a split deployment the
worker runs the `cg:webhooks` consumer + retry sweep. Either way the same key must be set on the
process that delivers.

## 2. Create a subscription

```sh
curl -X POST http://localhost:8080/v1/webhook-subscriptions \
  --cookie 'crm_session=<admin or ops session>' \
  -H 'Content-Type: application/json' \
  -d '{
        "target_url": "https://example.test/hooks/margince",
        "event_types": ["deal.stage_changed", "person.created"]
      }'
```

- `target_url` **must be `https://`**.
- `event_types` is a **non-empty subset of the published catalog** — an unknown type is a `422`, and
  the entity-less capture-pipeline events (`capture.*`) are rejected (they name no subject to scope
  delivery by). What actually validates a create/update is the runtime catalog (`events.Types()` minus
  the pipeline class); the contract's `SubscribableEventType` enum (`public-events.yaml`) is the
  documented projection of it, but as shipped it undershoots by six types (`approval.*`, `coldstart.*`,
  `audit.appended` — accepted and delivered by the API, but not offered by name here or in the UI
  picker; see `explanation/outbound-webhooks.md` §3). For the documented set:
  ```sh
  grep -A200 'SubscribableEventType:' backend/api/public-events.yaml | grep '^\s*- ' | sed 's/^\s*- //'
  ```
  Every type in that enum has a published `WebhookPayload<Event>` schema in the same file, describing
  the exact `data` shape your receiver will see for it — a few are catalogued but not yet emitted or not
  yet delivered (`explanation/outbound-webhooks.md` §5); their schema's own description says so.

The `201` response is the subscription **plus the signing secret** — the ONLY time the plaintext
appears:

```json
{
  "subscription": { "id": "…", "target_url": "https://…", "event_types": ["…"],
                    "state": "active", "version": 1, "owner_id": "…" },
  "signing_secret": "whsec_…"
}
```

**Store `signing_secret` now** — it is never shown again on any read. If you lose it, rotate (step 5);
you cannot recover it. The `owner_id` is the acting human, stamped server-side — the fan-out only ever
delivers events that owner may see.

> **Legacy secrets (pre-Standard-Webhooks migration).** A subscription created before this scheme
> shipped minted its secret with URL-safe base64; Standard Webhooks requires *standard* base64, so
> that secret can no longer sign. It is not silently broken-and-unnoticed — deliveries simply stop
> verifying against it. Fix: create a fresh subscription, or rotate the secret (step 6) on the existing
> one; either mints a new standard-base64 `whsec_…` secret that works.

## 3. Verify the signature on your receiver

Every delivery is a POST carrying three [Standard Webhooks](https://www.standardwebhooks.com/) headers.
Verify `webhook-signature` against `{webhook-id}.{webhook-timestamp}.{raw request body}` using the
secret from step 2, and dedupe on `webhook-id` (the bus is at-least-once; the delivery id is stable
across retries — `webhook-timestamp` is minted fresh on every attempt, so a captured signature cannot
be replayed against a receiver that enforces a timestamp tolerance window):

| Header | Meaning |
|---|---|
| `X-Margince-Event` | convenience only — the event type, e.g. `deal.stage_changed` |
| `webhook-id` | the delivery id — your dedupe key, stable across retries |
| `webhook-timestamp` | unix seconds when THIS attempt was signed — fresh every attempt |
| `webhook-signature` | `v1,` + base64 HMAC-SHA256 of `{webhook-id}.{webhook-timestamp}.{body}`, keyed by the secret's decoded bytes |

A minimal receiver check (Go) — note the secret is `whsec_`-stripped and **base64-decoded to bytes**
before use as the HMAC key (the SW compatibility requirement):

```go
func verify(secret, webhookID, webhookTimestamp string, body []byte, sigHeader string) bool {
	key, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(secret, "whsec_"))
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(webhookID + "." + webhookTimestamp + "."))
	mac.Write(body)
	want := "v1," + base64.StdEncoding.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(want), []byte(sigHeader))
}
```

Return any `2xx` to acknowledge; any non-2xx (or a timeout — one attempt is bounded at 10s) is treated
as a failure and retried.

## 4. Inspect deliveries

Every attempt is logged. List a subscription's deliveries newest-first — this is the dead-letter
inspection surface:

```sh
curl --cookie 'crm_session=<session>' \
  "http://localhost:8080/v1/webhook-subscriptions/<id>/deliveries?limit=50" \
  | jq '.data[] | {event_type, status, attempts, last_status_code, last_error, next_retry_at}'
```

The `status` vocabulary is the state machine:

- `pending` — freshly enqueued, first attempt imminent.
- `delivered` — a `2xx` was received.
- `retrying` — failed, parked with a `next_retry_at`; the sweeper re-attempts (backoff `1,2,4,8,16s`).
- `dead_lettered` — the 6-attempt budget is spent; it will not retry on its own.

`page.has_more` reports truncation honestly — the view never looks complete while older parked
deliveries sit behind the limit.

## 5. Replay a dead-lettered delivery

Once you've fixed the receiver, replay a parked delivery. This is a **human, audited** action; it resets
the attempt budget and re-sends the *stored* body (so it works even after the source event has aged off
the bus):

```sh
curl -X POST --cookie 'crm_session=<admin or ops session>' \
  http://localhost:8080/v1/webhook-subscriptions/<id>/deliveries/<deliveryId>/replay \
  | jq '{status, attempts, last_status_code}'
```

Replay re-sends the delivery's stored body **verbatim**, at whatever `version` it was originally
enqueued with — it never re-renders the payload against a schema that has since (additively) grown, so
an old delivery replays exactly as your receiver saw it the first time.

## 6. Rotate the signing secret

Rotation mints a new secret and returns it once; the prior secret **stops verifying immediately**, so
roll it into your receiver before (or atomically with) the switch:

```sh
curl -X POST --cookie 'crm_session=<admin or ops session>' \
  http://localhost:8080/v1/webhook-subscriptions/<id>/rotate-secret \
  | jq -r '.signing_secret'
```

## 7. Pause, re-target, or archive

Pause/resume and re-target run under an optimistic-concurrency guard — read the current `version` and
pass it as `If-Match`:

```sh
# pause delivery without archiving
curl -X PATCH --cookie 'crm_session=<admin or ops session>' \
  -H 'If-Match: "1"' -H 'Content-Type: application/json' \
  -d '{"state": "paused"}' \
  http://localhost:8080/v1/webhook-subscriptions/<id>

# archive — stops all delivery permanently
curl -X DELETE --cookie 'crm_session=<admin or ops session>' \
  http://localhost:8080/v1/webhook-subscriptions/<id>
```

A paused subscription holds its retries until it resumes; an archived one stops delivery and reads as
`404` thereafter (existence-hiding). An empty PATCH (setting neither `state` nor `event_types`) is a
`422`, not a silent no-op.

## Verify end-to-end

1. **The secret appears exactly once.** A `GET /webhook-subscriptions/<id>` never returns
   `signing_secret` — confirm it is absent from every read.
2. **A signed delivery arrives and verifies.** Trigger a subscribed event (e.g. advance a deal's stage
   for `deal.stage_changed`), confirm your receiver got a POST, and confirm the HMAC verifies against
   the raw body.
3. **The owner-scope gate holds.** Register a subscription as a rep who can see only their own deals,
   then trigger `deal.stage_changed` on a deal they cannot see — confirm **no** delivery is enqueued
   for that subscription (it never escalates past what the owner may read in the UI).
4. **Retry and dead-letter.** Point a subscription at an endpoint that returns `500`, trigger an event,
   and watch the delivery walk `retrying → … → dead_lettered` over its backoff schedule; then fix the
   endpoint and `replay` it to `delivered`.
5. **The key-gate is honest.** Unset `MARGINCE_WEBHOOK_KEY` and restart: reads still list, but
   create/rotate/replay answer `503 webhooks_not_configured` — never an unsigned delivery.
6. **The delivered `data` matches the published schema.** For any event you subscribe to, diff your
   receiver's `data` field against that event's `WebhookPayload<Event>` schema in
   `backend/api/public-events.yaml` — they must agree field-for-field (the compile-time seam that
   guarantees this is [explanation/outbound-webhooks.md §3](../explanation/outbound-webhooks.md)).
7. **A ratified deferred-delivery type stays honestly silent.** Subscribe to `mirror.conflict` or
   `retention.applied` and trigger the underlying overlay/retention action — confirm you receive nothing
   for it; this is a documented gap ([explanation/outbound-webhooks.md §5](../explanation/outbound-webhooks.md)),
   not a bug in your setup.
8. **The UI mirrors the API.** Everything above also works from Settings → Integrations — the create
   modal reveals the secret once, and the deliveries panel shows the same retry/dead-letter/replay
   lifecycle you drove with `curl`.
