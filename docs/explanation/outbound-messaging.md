# Outbound messaging — the delivery machinery behind a sent message

`internal/modules/comms` is Margince's **outbound message delivery**: the durable record of what was
staged for transmission, the rules that decide whether it may go now, and the dispatcher that hands it
to a provider. It is the mirror image of [capture-connectors.md](capture-connectors.md): that is the
governed *ingress* surface, this is what happens when a rep — or a governed agent — answers.

For the one-paragraph version see [reference/modules.md](../reference/modules.md); to actually connect
a transport and send something, jump to [how-to/connect-a-mailbox.md](../how-to/connect-a-mailbox.md)
or [how-to/connect-telegram.md](../how-to/connect-telegram.md); for the write shape every row here
commits through, see [write-backbone.md](write-backbone.md).

## The split — comms owns the machinery, activities owns the message

**The user-visible fact of an outbound email is the activity row.** The `activities` module writes and
owns it; `comms` holds only the state needed to get that message out and to say honestly why it has
not. Comms owns exactly one table: `comms_outbound`.

That split is what makes the surface trustworthy on a page reload. The rep's send answers `202
Accepted`, and what is durable at that moment is the *timeline row* — the message they can see. The
delivery row beside it is bookkeeping: an attempt counter, a status, a reason, a receipt.

```text
POST /activities/{id}/send-email     (or /send-message)
      │
      ├─ gates that run while the rep is still looking at the screen
      │  (authorization → wiring → recipients → consent)
      │
      └─ ONE transaction ────────────────────────────────► 202
           ├─ activity row        the message, on the timeline   (activities)
           ├─ comms_outbound row  status='pending'               (comms)
           └─ comms_send_email    the transmit job               (River)

                          … later, on the worker …

         Dispatcher ── authority → seat → consent → pacing → transmit
                          └─ sent | parked | postponed | retry | skipped
```

Provider I/O lives in whichever connector implements `ports/connector.EmailSender` or
`ports/connector.MessageSender`. **comms never speaks to Google or to Telegram.**

## The durable staging row, and why a send is accepted rather than transmitted

`Store.StageTx` (mail) and `Store.StageChannelTx` (a messaging channel) both write into
`comms_outbound` **inside the caller's transaction**, so the delivery and the activity it reports on
commit together or not at all. The transmit job is enqueued on that same transaction
(`Runner.EnqueueTx`), which is what makes "the message is on the timeline" and "something will try to
send it" one fact rather than two.

Accepting asynchronously is not a latency optimization. It is what lets the send be **governed at the
moment it actually leaves**:

- **Consent can be withdrawn between staging and transmission.** The dispatcher's consent call is the
  authoritative one; the request-time check exists to fail fast and keep the response ordering honest,
  not to stand in for it.
- **A seat can be revoked or downgraded in that window**, and a staged delivery carries no session of
  its own for a revocation to invalidate.
- **A provider outage must not become a failed user action.** The delivery rides a bounded retry ladder
  (`sendMaxAttempts = 10`, roughly five hours on River's default backoff) and parks with a reason when
  the ladder is spent, instead of erroring in the rep's face at click time.
- **Sends need pacing.** Providers throttle an account that bursts, and pacing ourselves is what keeps a
  legitimate run of sends from costing the user their mailbox's standing.

There is deliberately **no in-flight status**. A crash mid-send would strand a row in it, and a guard
keyed on that status would turn River's redelivery into a silent skip — disabling the connector's
retransmission check in exactly the crash it exists for. What makes redelivery safe instead is the
terminal status: every transition is guarded on `status = 'pending'`, and a delivery that already
finished answers `ErrTerminal`, which the dispatcher reads as `OutcomeSkipped`.

The row is **mail-shaped or channel-shaped and never half of each** (the `comms_outbound_shape`
constraint, migration 0155). Two Go input types carry that invariant up out of the database: one struct
with a mode flag could name a subject *and* a channel recipient together, and the only thing left to
refuse it would be Postgres — after the caller had already decided to write.

## The dispatcher and its gates

One dispatch attempt is `Dispatcher.DispatchWithWait`, and the sequence is
**authority → seat → consent → pacing → transmit**. Gates and policies are different mechanisms because
they state different facts: **a gate says never** (no amount of waiting repairs a revoked grant), so
gates are inline, fixed and not configurable; **a policy says not yet**, so policies are an ordered
chain the deployment assembles.

The order is load-bearing rather than stylistic: **authority must refuse before consent answers**, or
the difference between "you may not" and "they said no" tells a caller with no rights at all something
about a person's consent state.

### Authority — what the provider says about the credential

`gateSendAuthority` reads the scope list the resolver *just* read from the provider, not a copy stored
when the grant was made. `SendScopeFor` answers in three states, not two, because two cannot express a
bot token:

| Capability | Provider | Meaning |
|---|---|---|
| `SendsWithScope` | `gmail` | The grant must carry `.../auth/gmail.send`; a connection without it parks with "reconnect it to enable sending". |
| `SendsWithoutScope` | `telegram` | The credential **is** the whole authority — a bot token carries no OAuth grant, so there is nothing to intersect. |
| `CannotSend` | anything else | Nothing on this installation transmits for it. It is the zero value, so a capability nobody answered refuses rather than sends. |

Collapsing "sends without a scope" into "cannot send" would read a channel provider as capture-only and
park every message it was ever handed, under a reason naming a connector limitation that does not exist.

> **Gmail is not read-only.** The Gmail connector requests **two** scopes on **one** consent —
> `gmail.readonly` for capture and `gmail.send` for the governed outbound path — because Google will
> not add a scope to an existing refresh token, so a second grant would mean a second connection for
> the same mailbox. The pair is still least-privilege: no `gmail.modify`, no settings, no delete, and
> the send scope permits transmission only. `comms` cannot import a capture provider, so the scope
> literal it demands at the authority gate is bound to the connector's own constant by a fitness test
> in the composition layer — drift there is silent, and a misspelled scope parks every send as
> ungranted, which reads to an operator as a user who declined.

### The seat — this installation's answer about the human

`SeatAuthority.ActiveSeat` re-reads the **staging human's live seat at transmit time**. Deactivating a
user revokes their sessions and passports, but a delivery staged before that moment carries no session
of its own — so without this gate the off-boarded account's staged batch keeps leaving their mailbox
for as long as the maximum age allows. A **downgrade** binds identically: `seat_type` is the licensing
ceiling every other seam enforces before it lets a principal mutate, so **a rep demoted to a read seat
between staging and transmit is refused**, whatever staged the message.

It **parks** rather than retries, because both an off-boarding and a downgrade are *answers* — no amount
of waiting restores the authority. A seat authority that could not *answer* is the opposite case and
retries, so an identity-store outage does not destroy every send in flight. The same split applies on
the channel: the credential lookup moves off the human's account (a bot is bound once for the whole
workspace), but the seat check does not move at all.

### Consent — default-deny, per purpose, over every subject

`ConsentGate.RequireGrantedForRecipients` is asked about **every subject the delivery reaches**, not
just the To line: a Cc'd person is owed the same suppression, and this call is the only one that runs
after they could have withdrawn. One-click unsubscribe writes a per-purpose consent withdrawal, so this
gate *is* the suppression mechanism.

It asks in **recipients** rather than addresses, and that shape is what lets one ladder carry both
transports: a channel recipient has no address, so a gate that could only be handed addresses would be
handed an empty list for every channel delivery — and a default-deny gate asked about nobody refuses
nobody, so the whole channel would pass a check that never ran.

Default-deny is literal. An unknown purpose, a grant for a *different* purpose, `unknown`, and
`withdrawn` all block; a purpose declaring `requires_double_opt_in` additionally needs a confirmed
`consent_event`. The gate must distinguish an **answer** (`apperrors.ErrConsentNotGranted` — park) from
a **fault** (anything else — retry): getting that backwards silently kills legitimate mail.

### Confirm-first for agents; a human's own action is its own approval

Both send operations declare `tier: confirmation_required` (🟡) in the contract's `x-mcp-tool`
extension, and both take an `ApprovalToken` parameter. The composition root's autonomy gate reads that
tier off the contract and enforces it **before** the request reaches the handler, so an agent caller
must present an approval token while a human caller's own call arrives as the approval it is. That is
the same 🟡 machinery the approvals module runs for every other confirm-first verb — see
[agent-surface.md](agent-surface.md).

### Pacing — the policies that postpone

`MailboxRatePolicy` keys on the **mailbox**, not the message: a per-message key would give every send
its own window and pace nothing. It *peeks* the limiter rather than spending a slot, because a slot
stands for a message that actually reached the provider — and it is told about a real send only after
the receipt is durable (`SendRecorder.Recorded`), because a limiter counting checks instead of sends
paces nothing. A permanently saturated policy would defer a delivery forever, silently, so past the
configured maximum age the delivery parks with a reason instead.

### The five outcomes

`OutcomeSent`, `OutcomeSkipped`, `OutcomePostponed`, `OutcomeParked`, `OutcomeRetry` — the caller's
whole instruction, so a job runner maps them to done / snooze / back off without re-deriving anything
from the row. Park reasons are written for the human who has to decide what happens next: the
recipient-unreachable reason names the recipient *and* says which two remedies are wasted; the
unknown-outcome reason says the message will not be retried and to check the conversation.

## Receipt before bookkeeping — key on the identity the provider stamped

Gmail rewrites `Message-ID`. A message staged under the identity this system minted goes out under
Google's, so bookkeeping keyed on the id we *requested* loses the receipt: the echo collapse (the
captured copy of our own sent mail folding onto the same activity instead of duplicating it), the reply
join and the threading headers all key on a string the wire never carried.

`Store.RecordSent` therefore does two things in **two transactions**, and which fact is in which is the
safety property:

1. **`commitReceipt`** writes the receipt alone — `status='sent'`, `provider_message_id`, `sent_at`,
   `inflight_at=NULL` — in a transaction carrying no bookkeeping it could fail with. It returns only
   once that is durable.
2. **`reconcileIdentity`** then moves the delivery and its timeline row onto the identity the provider
   actually stamped (`RFC822MessageID`), in a transaction of its own, best-effort, reporting nothing.

**The rule this turns on: receipt before bookkeeping.** By the time `RecordSent` is called the provider
has accepted the message, so an obligation exists that nothing afterwards may revoke. Leaving the
delivery pending sends it back to River, and the connector's prior-send lookup cannot see an identity
the provider discarded — it finds nothing and **transmits again**. A single transaction with the re-key
under a savepoint is *not* the same guarantee. A savepoint isolates one refused statement. It does not
survive a failed RELEASE, a dropped connection, or a panic raised outside the guarded call. Any of
those leaves the receipt as an uncommitted UPDATE in a transaction that then fails to commit — and the
double-send is back.

So the whole reconcile is defensive by construction. It runs inside a `recover` boundary covering the
transaction plumbing *and* the fault report, because a panic escaping it would unwind the dispatch
attempt and let the redelivery re-send. A provider identity that is not a shape a message could carry
is **recorded, never adopted**. Every failure degrades to exactly one outcome: *receipt recorded, one
duplicate timeline row*, with a `comms_identity_reconcile_failed` breadcrumb in `system_log` for the
operator. `thread_key` moves only when it equalled the message's own identity — a conversation **root**
re-roots onto the identity the world will reply to, while a **reply**'s thread key belongs to the
conversation it joined.

Both writes run under a context **detached from the caller's** with a deadline of its own: cancelling
the job cannot un-send the mail, so it must not be able to un-record it either.

### The seam that cannot look back — at-most-once

Mail can discover a prior send: the RFC822 identity is searchable at the provider, so a mail delivery
rides the retry ladder as it always has. Telegram's `sendMessage` has **neither an idempotency key nor
a prior-send lookup**, so no later attempt could ever tell. `sendSeam.detectsPriorSend` is the one flag
that distinguishes them, and it turns on two behaviours:

- **`MarkInFlight` before the provider call, not after.** Marked afterwards, a worker that died mid-send
  would leave a row that looks untried and the redelivery would deliver a second copy with nothing able
  to notice. A delivery that *already* carries the marker parks under the unknown-outcome reason.
  `ClearInFlight` retracts it only on a **definite** answer from the provider, which proves nothing was
  transmitted; it is shape-blind, and a no-op on mail rows where the column is always NULL.
- **`ParkTransmitted` instead of the ladder** when the receipt itself fails to write. For a seam with no
  prior-send lookup, returning to the ladder is not a delay but a *loss* — the next attempt reads the
  marker, learns nothing, and parks the delivery as an outcome nobody knows, while the customer is
  holding the message. Parking here states what is definitely true and **keeps the provider's message
  id**, which after a failed receipt is the only handle left on that message.

Connector-side, `sendOutcome` is the one translation from Telegram's sentinels into the shared
vocabulary, and it is where the honesty lives: a transport failure becomes
`connector.ErrSendOutcomeUnknown` (never retried), a 403 becomes `connector.ErrRecipientUnreachable`
(definite, permanent, parks at once), a 401/404 becomes `connector.ErrAuthRejected`, and a 429 passes
through with Telegram's own stated interval so a backoff of our own invention does not earn a harder
limit.

## The channel twin — the reply that can only reach the human who wrote

`POST /activities/{id}/send-message` is `send_email`'s sibling, and `resolveSeam` is the **one** branch
on provider class in the whole path. Past it, the gates, the pacing chain, the ladder and the four
dispositions are one code path for both transports — a second branch downstream would be two send paths
wearing one name, and the one exercised less (the channel, by a wide margin) is the one that would
quietly stop matching the rules the mail path keeps.

What differs is only the vocabulary of the transport:

- **The activity's `kind` names the medium.** Capture files a Telegram update under `kind='telegram'`,
  and the reply transmits through the provider of that same name. `IsChannelKind` lists only what this
  installation can actually transmit through — `whatsapp` is a kind the contract reserves with no
  connector behind it, and admitting it would accept a reply that could only park.
- **The recipient is resolved, never named by the caller.** `SendMessageRequest` carries `body` and
  `consent_purpose` and nothing else. A channel identity is an opaque third-party account id, so a
  caller able to name one could message a human this conversation is not with — and the reply surface
  has no legitimate use for that. The server reads the anchor's `activity_link` rows, asks the people
  module which of those people are **reachable** on the provider, and refuses unless the answer is
  exactly one.
- **Reachability replaces address validity.** `ReachableChannelIdentities` returns live identities with
  `blocked_at IS NULL`. It returns a **list**, because the unique key binds an account to one person and
  not a person to one account — handing back the first row would reply to whichever account the planner
  returned.

Three refusals, all `422`, all before anything is staged:

| Case | Code |
|---|---|
| A person with no live channel identity — they never messaged the workspace's bot, or they blocked it | `person_unreachable` |
| The conversation reaches more than one person | `ambiguous_channel_recipient` |
| No live bot is bound for the provider at all | `channel_not_send_capable` |

The outbound activity carries **no subject and no natural key**: a channel has no subject line, and a
bot files no echo of the sent message back into capture for a `(source_system, source_id)` key to
collapse onto. It does carry the anchor's `thread_key` — the reply must be filed on the conversation it
answers, or reply detection will never see it. The delivery itself is staged **unanchored**: the chat
*is* the conversation, and anchoring to a specific message would mean guessing at the capture provider's
natural-key format.

Reachability is checked at request time and not again inside the write transaction. That gap is
deliberate and bounded: a block landing in between leaves a staged message the provider itself refuses
with a definite error, so the delivery fails visibly rather than arriving — the same fail-fast split the
consent gate makes.

## Reply detection — the channel is resolved, not assumed

An **inbound** message in a thread we previously wrote **outbound** in is a reply (CAP-FORMULA-1), and
`engagement.reply` is what the engagement scoring feeds on. The formula keys on nothing but `thread_key`
and `direction`, so it holds for any threaded medium — but the event has to name the channel it actually
arrived on, because an automation answering a reply routes on that value alone.

`replyOriginOf` resolves **both halves in one switch** over `counterpartyShapeOf` — the single place
capture asks how a record names its human:

- **`shapeMail`** → channel `"email"`, and the person resolved from `person_email`. The medium and not
  the source system, on purpose: `gmail`, `imap` and `graph` are three ways of reaching one inbox, and a
  consumer routing a reply back has the same job for all three.
- **`shapeChannel`** → channel = the identity's own **provider** (`telegram`), and the person resolved
  from `person_channel_identity`. The provider *is* the medium there.

A missing person is not a fault — the ensure that creates them runs after the capture transaction
commits, so a first-ever sender simply has no person yet on either medium. The malformed shapes
(a record naming its human both by an address and by a channel identity; half a channel identity) return
their sentinels rather than a silent miss: the Sink refuses both at the edge, so reaching them here means
that guard was bypassed, and the reply path states the invariant break instead of absorbing it.

The prior-outbound scan matches **within one medium** (`kind = $2`), and that predicate is a security
control, not a nicety. `thread_key` is a single flat namespace holding both a mail thread root and a
channel's `<provider>:<bot>:<chat>` key, and the mail half is attacker-supplied — it is the message's own
`References` root, chosen verbatim by the sender. Without the predicate, a forged `References` header
naming a Telegram conversation (whose parts are both discoverable: a bot id is public, and a private
chat's id is the user's own) would manufacture a reply fact against a conversation that sender was never
in. A reply is answered on the medium it arrived on, so a cross-medium match could never have been
actionable anyway.

## Voice-bound drafts — how a draft binds to its send

Margince learns each rep's writing voice from what they actually send. When the AI drafts an email for
a human, the send is the moment we find out whether they sent that draft as-is or reworded it — and
that judgement is captured here, on the send path, because nowhere else can see both texts.

Drafting hands the caller an **opaque draft reference** for the text a model served. The send that
carries that reference back is what says whether the human sent that text or reworded it first — the only
evidence a later corpus decision has that the profile is drafting in its owner's voice.

The binding is the `DraftRef` on the mail send's input. `Store.recordDraftOutcome` runs
`RecordSendOutcomeTx` **inside the send's own transaction**, so the judgment commits with the message or
not at all, and the outcome is classified by comparing the served text against what actually went out:
`accepted` when the tokens are identical, `edited_sent` otherwise, with a **pinned** similarity metric
(a normalized token-level Levenshtein ratio over NFC-normalized, case-folded, whitespace-collapsed text)
— pinned because a later corpus is built retroactively from these rows, and a definition that drifted
would poison every decision made from the ones already stored.

Two asymmetries carry the whole design:

- **`recorded=false` with a nil error is every learning-domain answer** — a reference this installation
  never issued, one whose served text an erasure already removed, one another user owns, one a previous
  send already decided, or a sender who is not human. None of them blocks the send. A message that
  legitimately went out must never be refused over a learning signal, and answering "nothing to record"
  for a row the caller may not touch keeps a foreign reference indistinguishable from an unknown one.
- **A non-nil error is a genuine fault and does fail the send**, because it arrives inside the
  transaction that already holds the activity and the delivery, and half of that write shape must never
  commit.

**An agent's send carries no draft reference**, and that absence is a statement: a voice outcome is the
*owner's* judgment of the machine's draft, so an agent's edit is not the owner's authored text. The
recorder refuses a non-human principal anyway; naming a reference on the agent path would only make that
refusal look like an accident of wiring. The channel reply carries none either — `SendMessageInput` has
no such field.

The signal row deliberately keeps **no** `final_text`, and carries no person, activity or subject
linkage: Art. 17 erasure structurally could not find it, so persisting the sent correspondence there
would keep an erased person's mail alive for the retention window.

## Provider seams

Comms depends on interfaces and never on a provider:

| Seam | What it does |
|---|---|
| `connector.EmailSender` | `SendEmail(auth, EmailMessage)` — the mail transmission, with the prior-send lookup that makes a retry safe. |
| `connector.MessageSender` | `SendMessage(auth, ChannelMessage)` — the optional channel seam; a capture-only connector simply does not implement it, and the resolver reports `ErrCannotSend` rather than treating it as absent. |
| `ConnectionResolver.Resolve` | Resolves **one human's** mailbox: the send seam, its unsealed credential, and the scopes the provider says the grant holds. |
| `ConnectionResolver.ResolveChannel` | Resolves the **workspace's** channel binding: seam + credential, no user id (a bot is bound once for the whole workspace) and no scope list (there is nothing to intersect). |
| `MessageIdentityReconciler` | Re-keys the timeline row when the provider stamped a different identity. A required constructor parameter, because a role that transmits without one files every sent message under an identity that exists nowhere on the wire. |
| `SeatAuthority` / `ConsentGate` | The two authority answers, each obliged to distinguish an answer from a fault. |
| `SendPolicy` (+ optional `SendRecorder`) | The ordered pacing chain; adding a policy is a registration, not a change to the dispatch sequence. |

Three deployment facts are the **only** errors a resolver may report that park a delivery —
`ErrNoMailbox`, `ErrCannotSend`, `ErrProviderNotConfigured`. **Every other error is transient.** A
keyvault blip or a database timeout is a failure to *get an answer*, and parking on one would
permanently destroy a legitimate send that nothing is wrong with. `ErrProviderNotConfigured` is a
sentinel of its own precisely because reading it as transient would leave the row pending forever,
looking live and never sending.

## Rules of thumb

- **The activity is the message; `comms_outbound` is the machinery.** If a fact is user-visible, it
  belongs on the timeline row.
- **A gate says never, a policy says not yet.** Gates are fixed and inline; policies are a configured
  chain. Never mix the two.
- **Authority refuses before consent answers.** A caller with no rights learns nothing about a person's
  consent state.
- **The staging human's seat is re-read at transmit time**, on both transports.
- **Consent is default-deny, per purpose, over every subject the delivery reaches** — including Cc.
- **Receipt before bookkeeping.** A message the provider accepted may never end up recorded as unsent.
- **Park only on an answer, never on a failure to get one.**
- **A seam that cannot detect a prior send marks in-flight first and never retries an unknown outcome.**
- **comms never speaks to a provider.** Everything provider-shaped is behind the connector seams.

## Where the code lives

| | |
|---|---|
| The module contract (what comms owns, and what it does not) | `backend/internal/modules/comms/doc.go` |
| The staging row, `Load`, the transitions, the receipt + re-key ordering | `backend/internal/modules/comms/store.go` |
| The channel-shaped staging + the at-most-once marker + `ParkTransmitted` | `backend/internal/modules/comms/storechannel.go` |
| The dispatch sequence and the five outcomes | `backend/internal/modules/comms/dispatcher.go` |
| The one branch on provider class + the at-most-once guard | `backend/internal/modules/comms/sendseam.go` |
| The seams, the send-capability table, recipient derivation | `backend/internal/modules/comms/seams.go` |
| The identity reconcile and everything that keeps it from costing a second email | `backend/internal/modules/comms/identityreconcile.go` |
| The pacing chain | `backend/internal/modules/comms/policy.go` |
| The mail send — gate order, addressee derivation, the draft-outcome hook | `backend/internal/modules/activities/email.go`, `draftoutcome.go` |
| The channel reply — recipient resolution, reachability, the outbound row | `backend/internal/modules/activities/channelsend.go`, `handlers_channelsend.go` |
| The send-capability refusals shared by both transports | `backend/internal/modules/activities/sendauthority.go` |
| Reply detection and the reply origin switch | `backend/internal/modules/capture/sinkreply.go` |
| Channel reachability (`blocked_at`) | `backend/internal/modules/people/channelidentity.go` |
| The consent gate the dispatcher calls | `backend/internal/modules/consent/gate.go` |
| The voice learning loop's send half | `backend/internal/modules/ai/voice_sendoutcome.go` |
| Composition — the stager, the transmit job, the resolver, the Gmail scope pair | `backend/internal/compose/commsjobs.go`, `comms.go`, `capture.go` |
| The tables | `comms_outbound` (0136, + 0155 channel shape, 0156 in-flight marker) |
| The REST contract | `backend/api/crm.yaml` (`/activities/{id}/send-email`, `/activities/{id}/send-message`) |
| The job declaration | `backend/api/jobs.yaml` (`comms_send_email`) |

## Where to go next

- The inbound mirror image — the connector seam, the one Sink, the three ingestion modes:
  [capture-connectors.md](capture-connectors.md).
- Binding the channel this path replies on: [how-to/connect-telegram.md](../how-to/connect-telegram.md).
- Connecting a mailbox to send from: [how-to/connect-a-mailbox.md](../how-to/connect-a-mailbox.md).
- The consent model, the purposes, and Art. 17 erasure: [privacy-and-consent.md](privacy-and-consent.md).
- The 🟡 confirm-first machinery both send verbs declare: [agent-surface.md](agent-surface.md).
- The write shape and the outbox bus every row here commits through: [write-backbone.md](write-backbone.md).
- The other governed egress surface — outbound webhooks: [outbound-webhooks.md](outbound-webhooks.md).
- What every module owns, `comms` included: [reference/modules.md](../reference/modules.md).
