# Capture connectors — the inbound integration seam & the mail pipeline

`internal/modules/capture` (interfaces.md §1, ADR-0063, capture.md CAP-*) is Margince's **inbound**
integration surface: a *connector* talks to an external provider (Gmail, an IMAP mailbox, Microsoft
365 / Outlook via Graph, Google Calendar), normalizes each provider record onto the clean relational
core, and hands it to the **one** `connector.Sink` the capture module owns. It is the mirror image of
[outbound-webhooks.md](outbound-webhooks.md): that is the governed *egress* surface, this is the
governed *ingress* one.

For the one-paragraph version see [reference/modules.md](../reference/modules.md); to actually *connect
and test* a mailbox, jump to [how-to/connect-a-mailbox.md](../how-to/connect-a-mailbox.md); for the
write shape every captured row commits through and the bus the pipeline rides, see
[write-backbone.md](write-backbone.md).

## The core principle

**A connector normalizes; the Sink writes.** A connector is a small, pure-ish thing — it knows how to
authenticate to one provider, pull records incrementally from a cursor, and map one raw record to domain
structs. It knows *nothing* about how the CRM stores data, who may see it, or how an event ships.
Everything security-relevant — RBAC, RLS, provenance, audit, the outbox event, idempotency — lives
behind the **one** Sink, so it happens in exactly one place, once per record:

```text
provider record ──▶ connector.Normalize ──▶ Sink.Upsert  (ONE transaction)
                    (pure mapping, no I/O)     ├─ raw_capture    the re-parseable original
                                               ├─ domain row     person / organization / activity
                                               ├─ audit_log      stamped: connector principal
                                               └─ event_outbox   the domain event

              idempotent on (source_system, source_id) — a replay is a free no-op
```

Two invariants ride on top of that write:

- **connector ≤ human.** A connector's declared scopes must be a subset of the granting human's *live*
  scopes, enforced at connect time (`ErrScopeExceeded`) — exactly the discipline agents follow. Every
  mail connector declares read-only (`ScopeRead`, tier `TierAutoExecute`); none can write outbound.
- **Connecting is human-only.** Every connector op is `x-agent-access: human-only` (bar the session-less
  OAuth callback). An agent self-granting read of a human's personal mail is precisely what we never
  allow.

## The connector interface

Every integration implements `connector.Connector`
(`internal/shared/ports/connector/connector.go`), registered by `Descriptor().Name`:

| Method | What it does |
|---|---|
| `Descriptor()` | Static metadata read at registration — the stable name, declared `Scopes`, the 🟢/🟡 `RiskTier`, the `Produces` entity types. Drives the scope gate and contract gen. |
| `Authenticate(req)` | Establishes/refreshes credentials for one per-user, per-workspace connection; returns the opaque `Auth` bundle the other methods reuse. |
| `Sync(auth, cursor, sink)` | Pulls **incrementally** from `cursor` (history API / delta token / UID watermark), emits records via the Sink, returns the advanced cursor. |
| `Normalize(raw)` | Maps ONE raw provider record to domain structs. **Pure — no I/O** — so the mapping is the agent-edited, test-guarded surface. Returns `ErrSkip` for deliberately excluded input. |
| `HealthCheck(auth)` | Feeds the ops surface; an outage degrades capture, never blocks the CRM. |

Two **optional** seams a connector implements only when its provider supports them — the registry
type-asserts and skips a connector that doesn't:

- **`Watcher`** — a renewable push subscription (Gmail's 7-day Pub/Sub watch). A provider with no
  renewable push simply is not a `Watcher`.
- **`Backfiller`** — bounded date-backward enumeration of a mailbox (`EstimateBackfill` +
  `BackfillPage`). A provider that can't page backward is not a `Backfiller`, and the backfill engine
  refuses honestly rather than pretending.

## The one Sink — where the security lives

`Sink.Upsert` is the single write path (the core-principle diagram above): one transaction commits the
raw original + the domain row + the `audit_log` entry (stamped from the *connector* principal, never
forgeable) + the outbox event, idempotent on the `(source_system, source_id)` natural key so any replay
— a re-delivered push, a re-anchored cursor, an overlapping backfill page — collapses to a no-op.

Two pipeline concerns run *inside* the Sink, before anything is written:

- **The personal-mail exclusion gate (RC-2).** Three fixed rule kinds — `sender_domain`,
  `recipient_domain`, `label` — matched case-insensitively against the record's attributes. A match
  returns `ErrSkip` and **nothing is written** (a privacy control: matching mail leaves zero CRM rows).
- **Counterparty auto-create (PO-F-1/PO-F-2).** Every captured message names the human on the other side
  (direction-classified against the mailbox owner). The Sink routes it through the people module's **one
  dedupe chokepoint**: an exact match reuses, a fuzzy match creates-and-records for the review queue. A
  freemail sender suppresses the domain-named *organization* create; an erased address stays dead (A13).
  The `ThreadKey` (Gmail `threadId` / Graph `conversationId` / the RFC822 `References` root) is the
  reply-detection join key behind `engagement.reply`.

## Credentials & the vault

A connector credential — an OAuth refresh token, or a standing IMAP password — is **never** stored in
the clear and never on the `capture_connection` row. It is sealed with AES-256-GCM under
`MARGINCE_KEYVAULT_ROOT_KEY` (base64 of 32 bytes) into the operational `vault_secret` table; the
connection row carries only an opaque, workspace-scoped `credential_ref`. `Registry.Connect` seals
before it commits and **refuses loudly** if the vault is absent, rather than persist a credential in the
clear. A key that is set but not exactly 32 bytes is a boot error, never a silent fallback.

The **one-shot IMAP pull needs no vault** — it persists nothing. Every persisting path (OAuth Connect
seals, Sync resolves) requires it. The worker migrates any legacy `auth`-bytea rows onto the vault at
boot (idempotent).

## How records arrive — three ingestion modes

A connector's records reach the CRM through one of three paths, all converging on the same Sink:

1. **Bounded backfill — preview before spend (ADR-0063/ADR-0020).** On connect, a `Backfiller` fills the
   CRM *backward* over a chosen window. The connector's `EstimateBackfill` returns only the provider-side
   message count; the `/backfill/preview` endpoint pairs that count with an estimated AI cost it derives
   separately — together the consent surface, shown *before* anything runs. `StartBackfill` enqueues a job the
   worker walks one page per tick, committing the cursor per page so a crash resumes honestly. Windows
   are **widen-only** (3m → 6m → 12m); cancel keeps every row already captured.
2. **Continuous incremental sync — the sweep.** The worker's dispatcher (every `30s`) selects **due**
   connections (`status IN ('connected','error')` AND `next_sync_at ≤ now`) and runs one `SyncOnce`
   each. Pacing lives in the `capture_sync_state` sidecar (`next_sync_at = success + 2m`). A failure
   never kills a connection — the sidecar backs off (`2m·2^n`, capped `4h`, ±20% jitter), degrades to a
   daily probe after 20 consecutive failures, and heals on one success. The error taxonomy
   (`rate_limited | unreachable | auth | history_gone | internal`) surfaces as `last_sync_error_class`.
3. **Push — Gmail only.** With a Pub/Sub topic configured, Gmail delivers change notifications to `POST
   /webhooks/gmail-push` (a shared-secret token + Google OIDC when set); the handler zeroes the mailbox's
   `next_sync_at` and enqueues an immediate sync. Push is a *latency* optimization over the poll, not a
   separate write path.

Incremental sync moves *forward* from the connect-time watermark; backfill pages *backward* on its own
token; they never fight, and the capture key makes any overlap a no-op.

## Connecting — the OAuth flow

The standing connectors (`gmail`/`gcal`/`graph`) share one handshake (`capture/oauthflow`). Only the
connect step is worth a picture — everything after is the sync above:

```text
1. POST /connectors/{gmail|gcal|graph}/connect      (human session)
      → sign state (HMAC key, TTL 10m) + set CSRF cookie
      → return authorize_url  ──▶  user consents at the provider

2. GET /connectors/{provider}/callback              (session-less redirect target)
      → verify signed state + CSRF cookie + code
      → exchange code → REFRESH TOKEN
      → Registry.Connect:  scope ⊆ human?  →  seal token in vault  →  capture_connection (connected)
      → redirect to /#/…/connect/ok    (the SPA re-reads GET /connectors to prove it)
```

The access token is minted fresh per sync from the refresh token and **never stored**. IMAP does **not**
use this flow — it is a one-shot pull with no OAuth and no persisted connection (see below).

## The connectors

All four register in `internal/compose/capture.go`; all are read-only and produce `activity`. The
differences that matter:

| | **Gmail** | **IMAP** | **Graph** (Outlook) | **Calendar** (gcal) |
|---|---|---|---|---|
| Auth | OAuth `gmail.readonly` | IMAPS app-password | OAuth `Mail.Read` | OAuth `calendar.readonly` |
| Connection | standing | **one-shot** | standing | standing |
| Cursor | `historyId` | — | `deltaLink` | `syncToken` |
| Push | Pub/Sub 7-day | — | — (poll) | — (poll) |
| Backfill | ✔ | — | ✔ | — |
| Connect UI | ✔ | ✔ | ✘ (curl) | ✘ (curl) |

### Gmail — standing OAuth, push-capable

OAuth2 to Google with a single read-only scope (`gmail.readonly` — no send, no modify). Incremental sync
walks the **history API** from a `historyId` watermark; a stale watermark (`ErrHistoryGone`, Gmail
expires it ~weekly) degrades to a bounded re-list, not a full re-scan. It implements **both** optional
seams: a `Watcher` (the Pub/Sub 7-day push watch, renewed by the worker every `6h`, `48h` ahead of
expiry) and a `Backfiller` (3/6/12-month widen-only windows). **To run:** a Google OAuth app + the vault
key; a Pub/Sub topic is optional (without it, capture runs on the 2-minute poll). **UI:** the onboarding
"Connect Google" button + the backfill panel; the Settings roster reconnects/disconnects.

### IMAP — one-shot pull, nothing persisted

IMAPS (TLS-only, port 993) with a username + password. It is a **one-shot pull**: the credentials are
used for a *single* call — never stored, never logged — to fetch the most-recent `max_messages` and
capture each; to capture more, run it again. There is no cursor, no push, no backfill, and **no vault
needed**. The dialer is **SSRF-guarded** (`netguard.RefusePrivate`, checked post-DNS on the concrete IP),
so it refuses private/loopback hosts — you cannot point it at a localhost mailserver. **To run:** the
host + port + email + an **app-password** (both Gmail and Outlook block basic-auth IMAP with a normal
password; an app-password satisfies the provider requirement). **UI:** the onboarding IMAP form → a
captured/contacts/skipped tally.

> A *standing* IMAP connection (a UID-watermark cursor, a vault-sealed credential) exists in the backend,
> but the exposed connect route is the one-shot; there is no reachable path to create the standing one
> today (see [Honest limitations](#honest-limitations)).

### Microsoft Graph — standing OAuth, poll-only

OAuth2 to the Microsoft identity platform with delegated scopes `offline_access User.Read Mail.Read`
(tenant defaults to `common`). Incremental sync walks a **delta query** from a `deltaLink`; a stale link
(`ErrDeltaGone`, HTTP 410) re-anchors to a bounded 7-day window. It is a `Backfiller` but **not** a
`Watcher` — there is no change-notification subscription built, so Outlook latency is the poll interval,
not a push p95. **To run:** a Microsoft Entra (Azure AD) app + tenant + the vault key. **UI:** none to
*initiate* yet — connect via `curl`; the roster manages an existing connection. Note Microsoft **rotates
the refresh token on every redemption** and Margince does not yet persist the rotated value: the stored
original typically keeps working up to Microsoft's default **90-day inactive lifetime** for a confidential
client (an actively-syncing mailbox stays inside it), **but it can be revoked or expire earlier** (a
password change, an admin revoke, a conditional-access policy). When it stops working, the sync/connect
path surfaces `reauth_required` and the user must **reconnect** — there is no silent recovery until the
credential-update seam lands.

### Google Calendar (gcal) — standing OAuth, poll-only

OAuth2 to Google with `calendar.readonly`. It **reuses the same Google OAuth app as Gmail**, but as its
*own* authorization requesting the calendar scope alone (deliberately no `include_granted_scopes`, so the
mail-read grant never bleeds into this credential). Incremental sync uses a `syncToken`; a stale token
(`ErrSyncTokenGone`) re-anchors. No push, no backfill. **To run:** the *same* Google app as Gmail, with
the calendar scope enabled and a `/connectors/gcal/callback` redirect URI added, + the vault key. **UI:**
none to *initiate* yet — connect via `curl`; the roster manages an existing connection.

## Where each piece runs

Capture spans both process roles ([ADR-0054's four `cmd/<role>` binaries](architecture.md)):

- **`api`** serves the *interactive* surface: `connect`, `callback`, `disconnect`, `list`, the one-shot
  IMAP pull, backfill `preview`/`start`(enqueue)/`status`/`cancel`, the Gmail push webhook, the morning
  `digest` read, and the exclusion-rule CRUD.
- **`worker`** runs *every background pull* as leader-elected River periodic jobs: the sync dispatcher
  (`30s`) → per-connection `SyncOnce`, the backfill engine (one page/tick), the Gmail watch-renewal scan
  (`6h`), and the nightly capture suite (classify hourly, enrich + digest daily). The Surface-B agent
  runner shares the worker process but is a *separate* scheduler — it does not run capture.

Gmail/Graph OAuth needs its config on **both** roles (the api connects, the worker syncs). The full
flag/env table is [reference/configuration.md → Capture connector OAuth](../reference/configuration.md).

## The connect UI

Two entry points, both hitting the same API:

- **Onboarding → connect step** (`onboarding-connect-panels.tsx`) — where you *add* a connection.
  `GoogleConnectPanel` does a full-page OAuth redirect, proves the connection, and renders the
  `BackfillPanel` (window → estimate → start → live progress). `ImapConnectPanel` is a form that runs the
  one-shot pull and shows the tally. The **Microsoft chip is disabled ("Soon")**, and there is no gcal
  affordance at all.
- **Settings → Integrations** (`connectors.tsx`, `ConnectorsCard`) — the standing-connection roster: a
  status badge (`connected` / `reauth_required` / `error`) + last-synced per connection, a **reconnect**
  action for a `reauth_required` OAuth connection, and a confirm-gated **disconnect**. It sits next to
  the `WebhooksCard` (the egress side).

## Honest limitations

Per [STATUS.md](../../STATUS.md) — the pipeline is live; these were scoped out, not missed:

- **No first-connect UI for Graph or Calendar.** Both `graph` and `gcal` are fully wired OAuth connectors
  (same `connect`/`callback`/`disconnect` + sync as Gmail), and the roster shows/reconnects/disconnects an
  *existing* connection — but nothing in the UI *starts* one. Both are pure-FE follow-ups
  (`.tmp/MISSING-UI-V4.md` §8a); the backend is ready.
- **Graph is poll-only.** The change-notification subscription (validationToken handshake, `clientState`,
  ≤3-day renewal) is unbuilt, so Outlook latency is the poll interval. (The Gmail push-watch renewal runs,
  but the poll remains the active sync path even for Gmail today.)
- **Graph refresh-token rotation isn't persisted.** The stored token usually lasts up to Microsoft's
  default 90-day inactive lifetime (a confidential client) but can be revoked or expire earlier; on
  expiry the connection goes `reauth_required` and the user reconnects. Avoiding that reauth needs a
  credential-update seam the `Connector` interface lacks.
- **Standing IMAP is latent.** The one-shot pull is the exposed IMAP path; the UID-watermark standing
  connection exists in the backend but isn't wired to a reachable route.
- **No UI for exclusions or connector-health.** The personal-mail exclusion CRUD and the digest's
  `connectors[]` health rows are live on the API but have no screen yet (`.tmp/MISSING-UI-V4.md`
  §8 CN-3/CN-4).

## Rules of thumb

- **The connector normalizes; the Sink writes.** A connector never touches the CRM, RLS, audit, or the
  outbox — audit + provenance + event + idempotency all live in the one Sink, once per record.
- **connector ≤ human.** A demoted human instantly narrows every grant the sync runs under.
- **Capture is idempotent on `(source_system, source_id)`.** Replays, re-anchored cursors, overlapping
  backfill pages — all no-ops.
- **A failure degrades a connection, never kills it.** `error` is syncable (daily probe); only
  `disconnected`/`reauth_required` park a row.
- **Connecting is human-only.** An agent never self-connects a mailbox.
- **The credential leaves the connection row entirely** — vault-sealed under `credential_ref`; the
  one-shot IMAP pull persists nothing at all.
- **IMAP is one-shot; Gmail/Graph/gcal are standing.** Only the standing OAuth trio sync in the
  background; only Gmail/Graph backfill; only Gmail pushes.

## Where the code lives

| | |
|---|---|
| The connector seam (Connector / Watcher / Backfiller / Sink / NormalizedRecord) | `internal/shared/ports/connector/connector.go` |
| The one Sink + write shape + idempotency | `internal/modules/capture/sink.go` |
| The registry — scope intersection, Connect/Disconnect, SyncOnce, backfill, watch | `internal/modules/capture/registry.go`, `registry_connections.go`, `registry_watch.go`, `backfill.go` |
| Sync-state sidecar (backoff, error taxonomy, degrade/heal) | `internal/modules/capture/syncstate.go` |
| Personal-mail exclusion gate (RC-2) | `internal/modules/capture/exclusion/exclusion.go` |
| Counterparty / RFC822 mapping (direction, ThreadKey, skip rules) | `internal/modules/capture/mailmap/mailmap.go` |
| Gmail connector (OAuth, history sync, Pub/Sub watch, backfill) | `internal/modules/capture/gmail/` |
| IMAP connector (transient one-shot + latent standing; netguard SSRF guard) | `internal/modules/capture/imap/` |
| Graph connector (OAuth, delta sync, backfill) | `internal/modules/capture/graph/` |
| Google Calendar connector (OAuth, syncToken) | `internal/modules/capture/gcal/` |
| Shared OAuth handshake (authorize URL, code/refresh exchange) | `internal/modules/capture/oauthflow/oauthflow.go`, `capture/googleconn/` |
| Connect surface + state signing + CSRF (api) | `internal/compose/connectors.go`, `connectors_imap.go`, `imapconnect.go` |
| Backfill + digest HTTP surface | `internal/compose/backfilltransport.go` |
| Gmail push webhook (token + OIDC) | `internal/compose/gmailpush.go`, `capture/push.go` |
| Background jobs (dispatcher, sync, backfill, watch renewal, digest) | `internal/compose/jobs.go`, `capturejobs.go`; `backend/cmd/worker/main.go` |
| The tables + RLS | `raw_capture, capture_connection, capture_exclusion_rule, capture_sync_state, capture_backfill, workspace_email_domain, capture_digest` |
| The REST contract | `backend/api/crm.yaml` (`/connectors*`, `/capture/exclusions`, `/digest`) |
| The connect UI (Settings + onboarding) | `frontend/src/screens/connectors.tsx`, `onboarding-connect-panels.tsx`, `backfill.tsx` |

## Where to go next

- Connecting and testing a mailbox end-to-end (Gmail OAuth + IMAP for Gmail/Outlook):
  [how-to/connect-a-mailbox.md](../how-to/connect-a-mailbox.md).
- Every connector flag and env var (OAuth apps, Pub/Sub, sync interval, the vault key):
  [reference/configuration.md](../reference/configuration.md).
- The write shape every captured row commits through, and the outbox bus the pipeline rides:
  [write-backbone.md](write-backbone.md).
- The egress mirror image — the governed outbound webhook surface: [outbound-webhooks.md](outbound-webhooks.md).
- What every module owns, including `capture`'s tables and HTTP surface: [reference/modules.md](../reference/modules.md).
