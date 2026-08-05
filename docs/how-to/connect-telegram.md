# Connect a Telegram bot

Bind a Telegram bot so Margince captures the messages customers send it onto the timeline — creating
people and activities through the one dedupe chokepoint — and so a rep can reply from that timeline.
This guide is **UI-first**: you drive it from the app, with the equivalent `curl` shown alongside for
scripting and verification. For the mental model on the inbound side — the connector seam, the one
Sink, credential custody — read [explanation/capture-connectors.md](../explanation/capture-connectors.md);
for the outbound half — the staging row, the gates, the dispatcher —
[explanation/outbound-messaging.md](../explanation/outbound-messaging.md).

> **Single-organization installation.** One installation serves one organization; the server resolves
> it itself, so no request selects a tenant — the `curl`s below carry only the session cookie.
> ("Workspace" still names the internal RLS tenant these tables are scoped by.)

## Which path do I want?

There is one path, and it is not a mailbox. A `capture_connection` models **one human's grant over
their own mailbox**; a `channel_connection` is an **admin binding one bot on behalf of the whole
workspace**. Three consequences, all of them load-bearing:

- **One live bot per workspace.** `uq_channel_connection_ws` is a partial unique index over the live
  rows, and it exists because every outbound reply resolves the workspace's bot: with two live
  bindings the send path refuses to guess, so a second bot would not add a channel — it would take
  away the ability to reply on either.
- **Admin/ops bind it; everyone reads it.** The `channel_connection` RBAC object grants
  create/update/delete to `admin` and `ops` only, while `manager`, `rep` and `read_only` all hold
  read — a rep needs to know whether the channel is live before expecting a reply to arrive there.
  Every operation is also `x-agent-access: human-only`: the bot token grants read of every message
  the bot receives, so an agent must never be able to bind one on its own initiative.
- **The customer messages the bot first.** A Telegram bot cannot open a conversation. A person becomes
  reachable only when an inbound message binds a `person_channel_identity` for them, so there is no
  "message this person on Telegram" action that works before they have written to you.

Compared with a mailbox connector, what is *absent* is as important as what is present: no OAuth app,
no consent redirect, no callback URI, and no inbound HTTP route at all — **ingress long-polls**, so
this installation dials out and is never dialled.

## Prerequisites

Two things, and nothing else:

- **A bot token from BotFather.** Message [@BotFather](https://t.me/BotFather) on Telegram, `/newbot`,
  and keep the token it issues. It has the shape `<bot id>:<secret>`; the connect surface shape-checks
  that before spending a network call on it, so a pasted bot *username* or an empty box is refused
  immediately.
- **The vault key** — `MARGINCE_KEYVAULT_ROOT_KEY` (base64 of exactly 32 bytes), on **both** the api
  and the worker. The api seals the token on connect; the worker unseals it on every poll. Without a
  vault the api still lists bindings, but every mutating path refuses by name
  (`503 channel_credentials_not_configured`) rather than storing a token nothing could unseal, and the
  worker registers **neither** Telegram job at all — `telegram_poll` and `telegram_poll_sweep` both
  declare `registration: {when: [ChannelVault], absent: registers_nothing}`.

```sh
export MARGINCE_KEYVAULT_ROOT_KEY="$(openssl rand -base64 32)"  # base64 of exactly 32 bytes
```

Explicitly **not** needed, and not read by this surface:

| Not needed | Why |
|---|---|
| An OAuth app / client id + secret | There is no consent handshake — the token *is* the credential. |
| `MARGINCE_CONNECTOR_STATE_KEY` | Nothing to sign: there is no redirect and no state round-trip. |
| `MARGINCE_PUBLIC_BASE_URL` | Nothing is ever told where to reach this installation. |
| A callback / redirect URI | Same. `WithChannelSurface()` takes no deployment configuration at all. |
| An inbound webhook route | The poller calls `getUpdates`; connect actively **clears** any webhook the bot arrives carrying, because Telegram refuses `getUpdates` while one is registered. |

> **Restart after backend config.** The api is a compiled binary — setting the vault key needs
> `make dev` again to take effect; Vite hot-reloads the SPA but not the Go api.

## Connect from the UI

1. Open the app and go to **Settings → Integrations**. Below the mailbox roster sits the **Telegram
   bot** card ("One bot receives and sends messages for the whole workspace").
2. Click **Connect a Telegram bot** and paste the BotFather token into the **Bot token** field (a
   password input; the form never retains it after a failed submit).
3. Submit. On success the modal shows **Connected as @yourbot** with a status badge, and the card's
   roster re-reads `GET /channel-connections` to prove it rather than claiming a connection the server
   did not confirm.

The worker's `telegram_poll_sweep` dispatcher (every `30s`) picks the binding up on its next tick and
long-polls it. The cursor starts at `0` — "whatever Telegram still holds" — so a freshly connected bot
collects the messages already waiting for it.

**A failed connect cannot leave a half-connected state.** `ChannelStore.Connect` runs a fixed order:
`getMe` (validates the token, yields the bot id and @username) → `deleteWebhook` (clears any
registration; `drop_pending_updates` is deliberately *not* sent, because those pending updates are the
customer's messages) → seal the token in the vault → insert the row `connected` with a zero cursor, in
**one transaction with its audit row**. Nothing follows that write — a poll dials out, so there is no
registration to make and no flip to perform — which is why the schema's `pending` status is a value no
server ever produces. A failure anywhere before the commit leaves nothing behind but a vault entry the
path destroys itself on a lost uniqueness race.

<details><summary>Same thing via <code>curl</code></summary>

Read the token **silently** so it never lands in your shell history or a process listing, mirroring
the "never echoed back" guarantee:

```bash
read -rsp 'BotFather token: ' BOT_TOKEN; echo    # silent — never echoed, never in history
# The secret reaches jq on stdin, never on a command line: printf is a shell
# builtin, so no process's argv ever holds it (a jq `--arg` would, and `ps` reads argv).
printf '%s' "$BOT_TOKEN" \
| jq -Rs '{provider:"telegram", botToken:.}' \
| curl -X POST http://localhost:8080/v1/channel-connections \
    --cookie 'crm_session=<session>' -H 'Content-Type: application/json' --data @- \
| jq '{id, provider, channelId, channelLabel, status, version}'
unset BOT_TOKEN

curl --cookie 'crm_session=<session>' http://localhost:8080/v1/channel-connections \
| jq '.data[] | {channelLabel, status}'
```

`201` carries the connected row. No read surface ever returns the token or its vault ref: the
`ChannelConnection` shape carries `channelId` (the bot's global numeric id — the key) and
`channelLabel` (the @username — display only, because a username is mutable and re-assignable).
</details>

## Rotate the token

Rotating goes through `PATCH`, **in place** — never a disconnect/reconnect cycle. Click **Replace
token** on the connection's row in **Settings → Integrations**, paste the new token, submit. The same
modal serves both; the connection's current status stays visible while you do it, so a binding ingress
has parked reads that way rather than silently as "connected".

```sh
curl -X PATCH http://localhost:8080/v1/channel-connections/<id> \
  --cookie 'crm_session=<session>' -H 'Content-Type: application/json' \
  -d '{"botToken":"<new-token>"}' | jq '{channelLabel, status, version}'
```

What survives and what resets:

- **The row survives, and with it every `person_channel_identity` binding and all captured history.**
  Telegram user ids are global and `person_channel_identity`'s unique key omits the bot id, so
  identities keep resolving across a rotation — even a swap to a *different* bot.
- **The ingress cursor RESTARTS** (`poll_offset = 0`). `update_id` is a per-bot sequence, so inheriting
  the outgoing bot's position would ask the incoming bot for updates numbered beyond anything it has
  ever sent, and every message it had received would be skipped silently.
- **The connection never passes through a not-live state.** A poll dials out, so the row is the only
  thing that decides which token the next poll spends, and repointing it *is* the whole change.
- **The superseded token is destroyed** from the vault once the row names the new one; nothing else
  would ever collect it.
- The incoming bot's webhook is cleared, for the same reason connect clears one. The outgoing bot needs
  nothing — it stops being polled the instant the row stops naming it.

A rotation that lands while a poll or a send is mid-flight is fenced rather than raced: the poll's
offset advance carries a `channel_id = <the bot it actually spoke to>` predicate, and the send path
re-reads the binding's version immediately before spending the credential
(`ErrChannelBindingReplaced` — transient, so the delivery re-resolves rather than parking).

## Disconnect

**Settings → Integrations** → **Disconnect** on the row, then confirm. Three different things happen to
three different kinds of state:

| | What happens |
|---|---|
| The binding row | **Archived**, status `disconnected`. Archiving is what actually stops ingress — the due-scan selects only live `connected` rows — and it frees both partial unique indexes, so the same bot (or another) can be connected here again later. |
| The bot token | **Destroyed** in the vault. That is the custody guarantee: withdrawing a connection removes the credential, not just the row. |
| Captured activities, people, channel identities | **Kept.** Disconnecting stops capture; it does not erase history. (Erasure is Art. 17's job — see [explanation/privacy-and-consent.md](../explanation/privacy-and-consent.md).) |

```sh
curl -X DELETE http://localhost:8080/v1/channel-connections/<id> \
  --cookie 'crm_session=<session>' -i     # 204
```

Like connect and rotate, this needs the vault: without one it refuses
`503 channel_credentials_not_configured` rather than archiving a row whose sealed token nothing could
then destroy.

## Verify end-to-end

1. **The bot is bound.** **Settings → Integrations** shows a `telegram` row reading **connected**
   (or `GET /channel-connections`).
2. **A customer message becomes an activity.** From a **second** Telegram account, open a private chat
   with the bot and send a message. Within about one dispatcher tick it lands as an activity with
   `kind: telegram`, `direction: inbound`, provenance-stamped `connector:telegram`. A message whose
   payload is media with no words reads as a bracketed placeholder (`[photo]`, `[voice message]`) —
   the customer did reach out, and the timeline says so.
3. **The person was auto-created.** The Sink routes the sender through the people module's **one dedupe
   chokepoint**, exactly as a mail counterparty goes through it. The person is deliberately
   **ownerless**: a workspace bot acts for no one human, and `channel_connection.connected_by` is audit
   only, so reusing the connecting admin as an owner is precisely what is refused. **No organization is
   derived** — a channel identity carries no mail domain to derive one from.
4. **Grant consent, then reply from the timeline.** The send is default-deny per purpose, so record a
   grant first (`POST /people/{id}/consent` with the `transactional` purpose id from
   `GET /consent-purposes`), then use the reply box on the conversation.

   ```sh
   curl -X POST http://localhost:8080/v1/activities/<activity-id>/send-message \
     --cookie 'crm_session=<session>' -H 'Content-Type: application/json' \
     -d '{"body":"Thanks — looking into it now.","consent_purpose":"transactional"}' \
   | jq '{id, kind, direction}'      # 202 Accepted; the outbound activity is the durable fact
   ```

   **The recipient is never named by the caller.** The `{id}` activity is the conversation being
   answered; its `kind` names the medium; and the recipient is resolved server-side as the channel
   identity of the person that conversation is with. `SendMessageRequest` carries only `body` and
   `consent_purpose` — no addressee, no subject. A conversation that reaches nobody, or more than one
   person, is refused (`422 person_unreachable` / `422 ambiguous_channel_recipient`) rather than
   guessed at.
5. **The reply is filed on the conversation it answers.** The outbound activity carries the anchor's
   `thread_key`, which is what lets capture's reply detection match the customer's next inbound message
   against it and emit `engagement.reply` naming `telegram` as the channel.
6. **The token is never echoed, and disconnect destroys it.** No read surface returns it — not the
   list, not the connect response, not the audit trail (the audit images carry the provider, bot id,
   label and status, never a vault ref).

## Failure modes

Every refusal below is RFC 7807 with a fixed detail; Telegram's own `description` text never reaches
the wire — it rides the wrapped error that is logged server-side.

| Status + `code` | What happened | What it calls for |
|---|---|---|
| `400 channel_token_rejected` | Telegram answered 401/404, or the value cannot be a BotFather token at all. | Check the token BotFather issued, and that it has not been revoked. |
| `409 channel_workspace_already_bound` | This workspace already holds a live bot. | Disconnect it first, or `PATCH` its token to point it at a different bot. |
| `409 conflict` | *That bot* is bound elsewhere in this installation. | Use a different bot. (Distinct from the row above on purpose — the remedies differ.) |
| `409 version_skew` | A concurrent rotate/disconnect moved the row between your read and your write. | Re-read the connection and retry; nothing was written. |
| `502 channel_provider_unreachable` | DNS/TCP/TLS/timeout, or a Telegram 5xx. | Nothing was changed — retry once the provider is back. |
| `502 channel_provider_rejected` | Telegram understood the request and refused it on its own terms. | Nothing was changed — check the bot has not been restricted or deleted in BotFather. |
| `503 channel_credentials_not_configured` | No vault is configured, so a token can be neither sealed nor destroyed. | Set `MARGINCE_KEYVAULT_ROOT_KEY` and restart. |
| `503 channel_connections_not_configured` | This role composes no channel store (an honest answer, not a 500). | Reach the surface on a role that serves it — `cmd/api` composes it. |
| `403 permission_denied` | A non-admin/ops principal tried to create, update or delete a binding. | Read is available to every role; binding is admin/ops. |
| `404 not_found` | No live connection with that id in this workspace (existence-hiding — an archived one reads the same). | Re-read `GET /channel-connections`. |

Two states ingress itself can park a binding in, both visible as the row's `status` and both requiring
an operator:

- **`reauth_required`** — Telegram refused the sealed token. No retry repairs it: an admin re-pastes a
  token with **Replace token**.
- **`error`** — another consumer holds this bot's updates. The poller establishes this rather than
  inferring it: on Telegram's 409 it clears any registered webhook, re-asks with no long-poll interval,
  and only parks if the refusal repeats against a bot that provably carries no webhook. Find and stop
  the other consumer (a second installation, a staging stack, an unrelated integration).

Neither status is polled again until an operator acts — the due-scan selects only `connected` rows,
which is what actually ends the retry loop. The reason is recorded in the audit trail's
`poll_stopped_because`, because the row itself has no column for it.

## Limits

- **No backfill, ever.** The Bot API exposes no history endpoint, and Telegram retains unacknowledged
  updates for only about 24 hours — so there is nothing to page backward through. The Telegram
  connector is deliberately not a `Backfiller`; capture starts at connect time.
- **Latency is one dispatcher tick.** `telegram_poll_sweep` runs every `30s` and enqueues one
  `telegram_poll` job per live binding; each holds a 25-second long poll open (`telegram_poll`'s job
  timeout is `2m`, which must exceed the long poll plus the client's headroom). A poll that comes back
  *with* updates ends its job, so a backlog drains at one Bot API batch per tick. The per-bot
  uniqueness that satisfies Telegram's one-consumer rule is declared on the args type
  (`TelegramPollArgs.InsertOpts`), so no inserter can drop it by omission.
- **A blocked bot is a recipient problem, not a credential problem.** Telegram answers `403` for "bot
  was blocked by the user" and for a deactivated account; that maps to `ErrRecipientUnreachable`, and a
  staged delivery **parks at once** rather than burning the retry ladder — the park reason says
  retrying and reconnecting the channel both change nothing. Separately, Telegram reports the block as
  a `my_chat_member` update, which sets `blocked_at` on the person's channel identity; from then on the
  reply surface refuses with `422 person_unreachable` instead of offering a reply box that could only
  fail. Unblocking clears it, ordered by the update's own `update_id` so two transitions cannot apply
  backwards.
- **Private chats only.** Group and supergroup messages are refused before anything is stored: a bot in
  a group runs in Telegram's default privacy mode and would see only fragments, and a reply resolves
  its recipient through the sender's *private* chat, so a group-filed message could never be answered
  where it came from.
- **Media is named, not downloaded.** The body carries the text or the caption; a wordless message
  reads as `[photo]` / `[document]` / `[attachment]`. Fetching the file itself is out of scope.
- **A reply is not nested under a specific message.** The chat *is* the conversation, so an outbound
  channel delivery is staged unanchored rather than guessing at the capture provider's natural-key
  format.

## Where the code lives

| | |
|---|---|
| The workspace binding — connect ordering, the write shape, the RBAC gate | `backend/internal/modules/capture/channelconn.go` |
| Rotate + disconnect (in-place repoint, archive, credential destruction) | `backend/internal/modules/capture/channelconnedit.go` |
| The `/channel-connections` transport + the wire-code mapping | `backend/internal/modules/capture/handlers_channel.go` |
| The poller's reads/writes — due scan, poll target, cursor advance, park | `backend/internal/modules/capture/channelpoll.go` |
| Send-side resolve of the workspace's bot + the replacement fence | `backend/internal/modules/capture/channelsend.go` |
| The channel counterparty auto-create + the erasure mutex | `backend/internal/modules/capture/sinkchannel.go` |
| Bot API boundary, sentinels, token shape check | `backend/internal/modules/capture/telegram/api.go`, `auth.go` |
| The pure update → activity mapping, and the membership (block/unblock) parse | `backend/internal/modules/capture/telegram/normalize.go`, `membership.go` |
| The registered connector + its `MessageSender` seam | `backend/internal/modules/capture/telegram/send.go` |
| Composition: the connect surface, the poll dispatcher + worker, the ingest worker | `backend/internal/compose/channelconnect.go`, `telegrampoll.go`, `telegrampollscope.go`, `telegramingest.go` |
| Reachability (`blocked_at`) and the identity binding | `backend/internal/modules/people/channelidentity.go` |
| The governed reply — recipient resolution, gate order, staging | `backend/internal/modules/activities/channelsend.go` |
| The tables | `channel_connection` (0151), `person_channel_identity` (0152), `erasure_suppression` channel rows (0153), `comms_outbound`'s channel shape (0155/0156) |
| The REST contract | `backend/api/crm.yaml` (`/channel-connections*`, `/activities/{id}/send-message`) |
| The job declarations | `backend/api/jobs.yaml` (`telegram_poll_sweep`, `telegram_poll`, `telegram_ingest`) |
| The connect UI | `frontend/src/screens/telegram-connect-form.tsx`, `connectors.tsx` |

## Where to go next

- The inbound seam this rides on — the one Sink, the dedupe chokepoint, credential custody:
  [explanation/capture-connectors.md](../explanation/capture-connectors.md).
- The outbound half — the staging row, the seat/consent gates, the dispatcher, at-most-once:
  [explanation/outbound-messaging.md](../explanation/outbound-messaging.md).
- Connecting a mailbox instead (Gmail, IMAP, Graph, Calendar):
  [how-to/connect-a-mailbox.md](connect-a-mailbox.md).
- The consent model the reply gate reads: [explanation/privacy-and-consent.md](../explanation/privacy-and-consent.md).
- Every flag and env var, the vault key included: [reference/configuration.md](../reference/configuration.md).
