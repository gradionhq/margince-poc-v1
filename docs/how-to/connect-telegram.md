# Connect a Telegram bot

Bind a Telegram bot so Margince captures the messages customers send it onto the timeline — creating
people and activities through the one dedupe chokepoint — and so a rep can reply from that timeline.
Everything here is done in the app; the REST surface behind it is the contract
(`backend/api/crm.yaml`, `/channel-connections*`), and nothing below requires you to call it by hand.
For the mental model on the inbound side — the connector seam, the one Sink, credential custody — read
[explanation/capture-connectors.md](../explanation/capture-connectors.md); for the outbound half — the
staging row, the gates, the dispatcher — [explanation/outbound-messaging.md](../explanation/outbound-messaging.md).

> **Single-organization installation.** One installation serves one organization; the server resolves
> it itself, so nothing you do here selects a tenant. `channel_connection` carries no tenant column at
> all (core `0282`), and core row-level security was retired in `0217` — "the bot" below means the one
> installation's bot.

## What kind of connection this is

A bot binding is **not** a mailbox. A mailbox (**Settings → Connections → Connected inboxes**) is one
human's grant over their own mail; a Telegram bot is an **admin binding one bot for everybody**. Three
consequences, all load-bearing:

- **One live bot, full stop.** `uq_channel_connection_ws` is a partial unique index over the live rows,
  keyed on `(provider)` since `0282`. Every outbound reply resolves the installation's bot, so with two
  live bindings the send path refuses to guess: a second bot would not add a channel, it would remove
  the ability to reply on either. The card enforces this in the UI too — the **Connect a Telegram bot**
  action disappears once one is bound, leaving **Replace token** and **Disconnect** as the only verbs.
- **Admin/ops bind it; everyone reads it.** The `channel_connection` RBAC object grants
  create/update/delete to `admin` and `ops` only, while `management`, `manager`, `rep` and `read_only`
  all hold read — a rep needs to know whether the channel is live before expecting a reply to arrive
  there. Every operation is also `x-agent-access: human-only`: the bot token grants read of every
  message the bot receives, so an agent must never bind one on its own initiative.
- **The customer messages the bot first.** A Telegram bot cannot open a conversation. A person becomes
  reachable only when an inbound message binds a channel identity for them, which is why the composer's
  **How to send** picker offers Telegram only for someone who already has a conversation on it — and
  labels it *"Continues your Telegram conversation"* rather than pretending it could start one.

Compared with a mailbox connector, what is *absent* matters as much as what is present: no OAuth app,
no consent redirect, no callback URI, and no inbound HTTP route at all — **ingress long-polls**, so
this installation dials out and is never dialled.

## Before you start

Two things, and nothing else:

- **A bot token from BotFather.** Message [@BotFather](https://t.me/BotFather) on Telegram, send
  `/newbot`, and keep the token it issues. It has the shape `<bot id>:<secret>`; the server shape-checks
  that before spending a network call on it, so a pasted bot *username* comes back refused rather than
  attempted.
- **The vault key** — `MARGINCE_KEYVAULT_ROOT_KEY` (base64 of exactly 32 bytes), on **both** the api
  and the worker. The api seals the token on connect; the worker unseals it on every poll. Without a
  vault the card renders **"Messaging channels aren't configured in this deployment."** instead of a
  connect action — the api refuses every mutating path by name rather than storing a token nothing
  could unseal, and the worker registers **neither** Telegram job (`telegram_poll` and
  `telegram_poll_sweep` both declare `registration: {when: [ChannelVault], absent: registers_nothing}`).

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

> **Restart after setting the vault key.** The api is a compiled binary — `make dev` again for it to
> take effect. Vite hot-reloads the SPA but not the Go api.

## Connect

1. **Settings → Connections.** The **Telegram bot** card sits below **Connected inboxes**, subtitled
   *"One bot receives and sends messages for the whole organization."* With nothing bound, its roster
   row reads *"No bot is connected yet."*
2. Click **Connect a Telegram bot** in the card header.
3. Paste the BotFather token into **Bot token** — a password field, hinted *"Paste the token BotFather
   gave you when you created the bot. We seal it in the credential vault and never show it again."*
   The form retains nothing after a failed submit, so a retry starts from an empty box.
4. **Connect.** On success the modal reads **"Connected as @yourbot."** with a status badge and a
   **Done** button. The card behind it re-reads the connection list to prove the binding rather than
   claiming one the server did not confirm.

The worker's `telegram_poll_sweep` dispatcher (every `30s`) picks the binding up on its next tick and
long-polls it. The cursor starts at `0` — "whatever Telegram still holds" — so a freshly connected bot
collects the messages already waiting for it.

**A failed connect cannot leave a half-connected state.** Connect runs a fixed order: `getMe`
(validates the token, yields the bot id and @username) → `deleteWebhook` (clears any registration;
`drop_pending_updates` is deliberately *not* sent, because those pending updates are the customer's
messages) → seal the token in the vault → insert the row `connected` with a zero cursor, in **one
transaction with its audit row**. Nothing follows that write — a poll dials out, so there is no
registration to make and no flip to perform — which is why the schema's `pending` status is a value no
server ever produces (the UI still renders it as *"Pending — not yet confirmed live"* rather than as
healthy, in case an older or foreign server ever sends one). A failure anywhere before the commit
leaves nothing behind but a vault entry the path destroys itself on a lost uniqueness race.

## What the card shows once it is bound

One row: **Telegram · @yourbot**, a status badge, and the two verbs that change it.

| Badge | Status | Meaning |
|---|---|---|
| **Capturing** | `connected` | Live. Polled every dispatcher tick. |
| **Needs reconnect** | `reauth_required` | Telegram refused the sealed token. No retry repairs it — **Replace token**. |
| **Sync error** | `error` | Another consumer holds this bot's updates. Find and stop it (a second installation, a staging stack, an unrelated integration). |
| **Disconnected** | `disconnected` | Archived. Filtered out of the roster. |

Neither parked status is polled again until an operator acts — the due-scan selects only `connected`
rows, which is what actually ends the retry loop. The reason is recorded in the audit trail's
`poll_stopped_because`, because the row itself has no column for it.

## Rotate the token

**Replace token** on the row, paste the new token, submit. The modal is titled **"Replace the bot
token"** and keeps the connection's **current status badge visible** while you work — a binding ingress
has parked must read that way here too, not silently as healthy just because an edit form opened on it.

Rotating goes **in place**; it is never a disconnect/reconnect cycle. What survives and what resets:

- **The row survives, and with it every channel identity binding and all captured history.** Telegram
  user ids are global and the identity's unique key omits the bot id, so identities keep resolving
  across a rotation — even a swap to a *different* bot.
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
re-reads the binding's version immediately before spending the credential (a transient refusal, so the
delivery re-resolves rather than parking).

## Disconnect

**Disconnect** on the row, then confirm **"Disconnect this bot?"** — *"This deletes the stored token and
stops checking the bot for new messages. Capture and sending stop immediately; everything already
captured stays in your CRM."* Three kinds of state, three different outcomes:

| | What happens |
|---|---|
| The binding row | **Archived**, status `disconnected`. Archiving is what actually stops ingress — the due-scan selects only live `connected` rows — and it frees the live-row unique index, so the same bot (or another) can be connected here again later. |
| The bot token | **Destroyed** in the vault. That is the custody guarantee: withdrawing a connection removes the credential, not just the row. |
| Captured activities, people, channel identities | **Kept.** Disconnecting stops capture; it does not erase history. (Erasure is Art. 17's job — see [explanation/privacy-and-consent.md](../explanation/privacy-and-consent.md).) |

Like connect and rotate, this needs the vault: without one it refuses rather than archiving a row whose
sealed token nothing could then destroy.

## Verify end-to-end

1. **The bot is bound.** **Settings → Connections** shows the Telegram row reading **Capturing**.
2. **A customer message becomes an activity.** From a **second** Telegram account, open a private chat
   with the bot and send a message. Within about one dispatcher tick it appears on the timeline as an
   activity with `kind: message`, `channel_provider: telegram`, `direction: inbound`,
   provenance-stamped `connector:telegram`. (`kind` stopped naming a transport in core `0251`/ADR-0107:
   every captured channel message files as the one `message` kind, and which transport carried it is
   `channel_provider` — so asking for Telegram means `channel_provider=telegram`.) A message whose
   payload is media with no words reads as a bracketed placeholder (`[photo]`, `[voice message]`) — the
   customer did reach out, and the timeline says so.
3. **The person was auto-created.** The Sink routes the sender through the people module's **one dedupe
   chokepoint**, exactly as a mail counterparty goes through it. The person is deliberately
   **ownerless**: a workspace bot acts for no one human, and the connection's `connected_by` is audit
   only, so reusing the connecting admin as an owner is precisely what is refused. **No organization is
   derived** — a channel identity carries no mail domain to derive one from.
4. **Grant consent.** Open the person, find the **Consent** section, and **Grant** the purpose you
   intend to send under (some purposes need a double opt-in token first). Outbound is default-deny *per
   purpose*: a grant for one purpose never authorizes another.
5. **Reply from the timeline.** **Reply** on the inbound entry opens the composer with no Subject and
   no Cc — a channel has neither — and asks for a **Consent purpose**. Confirm at **"Send this
   message?"**: *"You are sending this message now. This is an outbound, irreversible action."*
   - **The recipient is never named by you.** The entry you replied to *is* the conversation; its
     `channel_provider` names the medium; the recipient is resolved server-side as the channel identity
     of the person that conversation is with. The request carries only the body, the consent purpose,
     and optionally attachments already in the record library (named by id, never uploaded here).
   - Without a grant the send is suppressed and the composer says so — **"Send blocked — no consent"**,
     with a **Review consent** link back to the person. That refusal is the whole reason the surface
     shows a purpose picker at all.
   - **Reply does not appear** for a person the channel cannot reach — an unbound identity, or one
     Telegram has reported as blocking the bot. A button that could only fail is not offered.
6. **The reply is filed on the conversation it answers.** The outbound activity carries the anchor's
   `thread_key`, which is what lets capture's reply detection match the customer's next inbound message
   against it and emit `engagement.reply` naming `telegram` as the channel.
7. **The token is never echoed.** No read surface returns it — not the roster, not the connect
   response, not the audit trail (the audit images carry the provider, bot id, label and status, never
   a vault ref).

## When connecting fails

Every refusal is RFC 7807 with a fixed `detail`, and the modal renders that `detail` verbatim in a
danger callout rather than flattening it to "couldn't connect". Telegram's own `description` text never
reaches the wire — it rides the wrapped error logged server-side. The `code` column is what you will
find in a log or an audit row.

| What the callout tells you | `code` (status) | What it calls for |
|---|---|---|
| The token was refused, or cannot be a BotFather token at all | `channel_token_rejected` (400) | Check the token BotFather issued, and that it has not been revoked. |
| A bot is already connected here | `channel_workspace_already_bound` (409) | Disconnect it first, or **Replace token** to point the existing binding at a different bot. |
| Someone else changed this connection while you had it open | `version_skew` (409) | Re-open the card and retry; nothing was written. |
| Telegram could not be reached | `channel_provider_unreachable` (502) | Nothing was changed — retry once the provider is back. |
| Telegram understood the request and refused it | `channel_provider_rejected` (502) | Nothing was changed — check the bot has not been restricted or deleted in BotFather. |
| No credential store is configured | `channel_credentials_not_configured` (503) | Set `MARGINCE_KEYVAULT_ROOT_KEY` and restart. |
| Messaging channels aren't configured in this deployment | `channel_connections_not_configured` (503) | This process role composes no channel store — an honest answer, not a 500. `cmd/api` serves it. |
| You may not change this | `permission_denied` (403) | Read is available to every role; binding is admin/ops. |
| No such connection | `not_found` (404) | Existence-hiding — an archived binding reads the same. Re-open the card. |
| — | `conflict` (409) | The provider is not one this binary implements, or some unique index other than the live-row rule refused the write. Only `telegram` is implemented; this is the honest fallback, not a second binding rule. |

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
  was blocked by the user" and for a deactivated account; a staged delivery **parks at once** rather
  than burning the retry ladder — the park reason says retrying and reconnecting the channel both
  change nothing. Separately, Telegram reports the block as a `my_chat_member` update, which sets
  `blocked_at` on the person's channel identity; from then on the **Reply** button is not offered at
  all. Unblocking clears it, ordered by the update's own `update_id` so two transitions cannot apply
  backwards.
- **Private chats only.** Group and supergroup messages are refused before anything is stored: a bot in
  a group runs in Telegram's default privacy mode and would see only fragments, and a reply resolves
  its recipient through the sender's *private* chat, so a group-filed message could never be answered
  where it came from.
- **Inbound media is named, not downloaded.** The body carries the text or the caption; a wordless
  message reads as `[photo]` / `[document]` / `[attachment]`. Fetching the file itself is out of scope.
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
| Send-side resolve of the bot + the replacement fence | `backend/internal/modules/capture/channelsend.go` |
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
| The card, the connect/replace modal, and the status vocabulary | `frontend/src/screens/connectors.tsx`, `telegram-connect-form.tsx`, `connector-status.ts` |
| The reply composer and the timeline **Reply** action | `frontend/src/screens/compose.tsx`, `persontransports.ts` |

## Where to go next

- The inbound seam this rides on — the one Sink, the dedupe chokepoint, credential custody:
  [explanation/capture-connectors.md](../explanation/capture-connectors.md).
- The outbound half — the staging row, the seat/consent gates, the dispatcher, at-most-once:
  [explanation/outbound-messaging.md](../explanation/outbound-messaging.md).
- Connecting a mailbox instead (Gmail, IMAP, Graph, Calendar):
  [how-to/connect-a-mailbox.md](connect-a-mailbox.md).
- The consent model the reply gate reads: [explanation/privacy-and-consent.md](../explanation/privacy-and-consent.md).
- Every flag and env var, the vault key included: [reference/configuration.md](../reference/configuration.md).
