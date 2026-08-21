# The ingress gate, auto-capture, and the verdict engine

This page explains what happens to a message **after** a connector fetched it from an external
provider, and before it appears in the CRM.

The path is the same for every source: Gmail, IMAP, Microsoft Graph, Telegram, or an extension unit
like `relay-probe`. How a connector talks to its provider is that connector's own business, and
is covered in [capture-connectors.md](capture-connectors.md).

A record has one of two **shapes**, and the shape decides which steps run:

- **Mail-shaped** — the counterparty is named by an address. This is the default path on this page.
- **Channel-shaped** — the counterparty is named by a *channel identity*: the account they hold at a
  messaging provider (Telegram, a unit-supplied transport). This is what makes a message repliable,
  and it skips the mail ladder (section 3).

A channel record may also carry an address. The identity still names the human; see "Channel records
don't climb the ladder" in section 3.

There are four stages:

```text
 1. a connector      2. the ingress gate    3. auto-capture        4. the verdict engine
    picks and    ──▶    allows or      ──▶    writes the      ──▶    decides who an
    normalizes          rejects it            message once           unknown sender is
    one message
```

Each stage answers one question:

| Stage | Question |
|---|---|
| Connector | Which messages are worth sending to the CRM? |
| Ingress gate | Is this call allowed, and whose permissions does it run under? |
| Auto-capture | What gets written to the database? |
| Verdict engine | Should this sender become a contact? |

Three rules apply everywhere:

- **Connectors don't write to the database.** They convert a provider's message into a standard
  record and hand it over. All permission checks, audit rows and events happen in one place.
- **Everything is idempotent.** If the same message arrives twice, the second time writes nothing.
  Connectors can safely re-send.
- **Nothing fails silently.** Every rejection either returns an error or writes a log row saying why.

## Which parts use AI

| Stage | How it works | AI task |
|---|---|---|
| 1. Ingress gate | Plain code | — |
| 2. Auto-capture | Plain code | — |
| 3. Tier ladder (T0–T4) | Plain code | — |
| 4. Verdict engine — claiming, writing, hiding, sweeping | Plain code | — |
| 4. Verdict engine — **judging one sender** | **AI** | `capture_counterparty_verdict` |
| Does this domain deserve a company record? | **AI** | `site_triage` |
| Attention labels on captured mail (separate feature) | **AI** | `capture_classify` |

A model makes one decision here: *what kind of sender is this?* It is asked once per **sender**, not
per message, and only for senders plain code could not classify. The answer decides whether a
**contact** is created. It never decides whether a **message** is kept.

If no model is configured, capture works normally. Only the judging step is skipped.

---

## 1. The ingress gate

The gate checks the call, then either rejects it or passes it to auto-capture. It writes nothing
itself.

Checks run cheapest-first:

| # | Check | Error | Reason |
|---|---|---|---|
| 1 | Is this source declared in the unit's manifest? | `ErrIngressNotDeclared` | A typo should be rejected, not create a new source name nobody knows about |
| 2 | Does the source vouch for the identity keys this record carries? | `ErrInvalid` | An address offered as matching evidence is a claim about who somebody is. Only a source that declared the `email` merge key may make it |
| 3 | Is this a background job, not a user request? | `ErrAttendedIngest` | A user request would mix two people's permissions |
| 4 | Is the caller already inside its own transaction? | `ErrNestedIngest` | Capture opens its own. Two connections from a small pool does not error — it hangs |
| 5 | Is the record within size limits? | `ErrInvalid` | Limits what a remote provider can make us store |
| 6 | Does the kind match the transport? | `ErrInvalid` | A message must name a channel the unit declared. No other kind may name one, because the reply path would answer on it |
| 7 | Is the counterparty's channel identity on a provider this unit supplies? | `ErrInvalid` | The reply path reads that binding to find *who* a reply goes to. An account bound under `telegram` would take a rep's next reply |
| 8 | Did this deployment wire up a capture pipeline? | named error | Better a clear error than a half-wired pipeline that saves messages but no contacts |
| 9 | Has this member stored a credential with this unit? | `ErrForbidden` | Storing the credential is how a member says "you may act for me" |
| 10 | What is this member allowed to do right now? | — | Looked up fresh on every call |

**Size limits (check 5):** raw payload 256 KB, subject 500 characters, body 32,768 characters, at
most 64 addresses (none empty), address at most 320 bytes, thread key 512 bytes, natural key 256
bytes, channel account id 256 bytes, display name 200 characters, and `OccurredAt` must be set.

A record may list no addresses at all, but only if its counterparty carries no address either. A chat
message often names none. A counterparty address with an empty address list is refused, because that
shape silently disables the internal-only check (section 2).

**The merge-key declaration (check 2)** is `IngressSource.Merges` on the unit's source. It is empty by
default and grants nothing, and an operator can read it in `manifest.generated.json` before enabling
the unit. The check is narrow: it only asks about an address that rides alongside a channel identity.
A mail-shaped record's address *is* its identity, so it needs no declaration.

The rule is checked in two places, on purpose. The gate refuses with the unit named, so a unit author
sees which declaration is missing. Capture's own admission check holds the same rule for every other
caller — a core connector, a fixture, a backfill — that never passes through the gate.

Two more things the gate does:

- **It fills in the source and `captured_by` itself**, from the calling unit. The record type has no
  field for these, so a connector cannot claim someone else captured a message.
- **It translates errors.** Database errors contain table names and SQL state, so only the error
  *class* reaches connector code.

The gate returns one of two results, and **both mean "move your cursor forward"**:

| Result | Meaning |
|---|---|
| `accepted` | The message is in the CRM |
| `skipped` | The core deliberately kept nothing, and logged why |

`skipped` is a success, not an error. As an error, it would make the connector retry the same message
on every poll forever.

## 2. Auto-capture: the single write

Everything below happens in **one database transaction**, and is idempotent on
`(source_system, source_id)` — the provider's own ID for the message. A repeat writes nothing.

Steps, in order:

1. **Erasure check.** If the message names an account that has been erased, reject it.
2. **Internal-only check.** If *every* address on the message belongs to the company's own mail
   domains, this is colleagues talking to each other. Write a short log row and drop the message.
   This runs **before** step 3 on purpose, so a colleague-only message is never stored at all.
3. **Store the raw payload.** Written once and never overwritten, so the original stays available.
4. **Write the activity row** — plus links, attachments, participants, an `audit_log` row and an
   `event_outbox` event. This is the standard [write backbone](write-backbone.md). The audit row
   stores metadata only, never the subject or body.
5. **Run the tier ladder** (section 3) inside a savepoint. If it fails, only the contact decision is
   lost; the message itself is still saved.

> **Why `Addresses` must list everyone**
>
> Step 2 asks "are all parties internal?". Over an empty list the answer is "no", so the message is
> kept. An empty address list therefore does not *skip* the check, it *disables* it, and internal
> chatter gets stored. `Counterparty.Domain` behaves the same way: if it is missing, the suppression
> rules below read the message as "keep".

## 3. The tier ladder: create a contact or not?

This is the **mail** path. It runs inside the same transaction as the message, so a message always
has a decision attached. All of it is plain SQL and Go. T4's only job is to record that a model
should be asked later. Channel records skip it — see the end of this section.

| Tier | Question | Result |
|---|---|---|
| **T0** | Is the sender a colleague? | Judge the external person on the message instead. If everyone is internal, create nothing |
| **T1** | Have we provably sent mail to this address before? | **Create the contact.** Beats every rule below |
| **T2** | Is this mail infrastructure (DocuSign, SendGrid…)? | Keep the message, create no contact and no company |
| **T2.5** | Did we already decide about this address? | Reuse that decision. No new question, no model call |
| **T3** | Is it a personal mail domain (`gmail.com`…)? | Create the person, but no company |
| **T4** | Nobody knows who this is | Create nothing yet. Write a row for the verdict engine |

Notes on the tricky ones:

- **T0 switches target instead of skipping.** If a colleague emails a client and copies you, the
  message is about the client. The ladder judges the client, and the colleague is never recorded as
  the contact.
- **T1 only trusts proof that *we* sent the mail**, not the `From` header, which can be forged. One
  outbound message counts, unless its text is a refusal ("not interested", "unsubscribe", "kein
  Interesse"). Two or more always count.
- **T1 also beats an old negative verdict**, so replying to a sender we once marked as noise brings
  them back properly.
- **T2.5 avoids paying twice.** Without it, every new message from a settled sender asks the model
  again and re-offers a decision a human already made. A live person holding the address counts as a
  settled `real`, except an address a channel connector vouched for: that identifies a person, but it
  is not mail correspondence.

**After the transaction commits,** the person is created through the people module. This happens
outside the transaction, so a failure there cannot lose the message. Failures are logged for the
nightly repair job.

Creating a person does **not** create their company. That is a separate question, answered by reading
the domain's website (AI task `site_triage`).

### Channel records don't climb the ladder

A channel record — a direct message on Telegram or on a unit-supplied transport — skips every tier
above. All four mail gates (internal domains, personal-mail domains, the transactional registry, the
deferral queue) key off a mail domain. The bypass keys off the **shape**, not off the address being
empty.

There is also nothing to defer. Someone who opens a conversation with the workspace's own bot is
already the intent T1 looks for evidence of: nobody messages a company's bot by accident, and a bot
cannot be cold-mailed. So the person is created at once — **person only, never a company**, and
**ownerless**, because a workspace bot has no granting human to own the record.

The account is then bound to that person. That binding is what a reply is routed on; a record with no
identity lands a message nobody can answer.

**If the record carries an address too** — allowed only for a source that declared the `email` merge
key — the identity still names the human, and the address only corroborates. The resolution ladder
matches on the address and **adopts** the person already captured from mail: the account is bound onto
their existing record, under the same lock a merge or an erasure takes. Without this, that colleague
quietly becomes a second contact.

A vouched address is stored as *not* correspondence (`person_email.from_correspondence = false`). It
identifies the person, but proves nothing about mail. Otherwise one direct message from a stranger
would mark their address a known counterparty for good: every later bulk mail from it auto-created,
and the noise sweep switched off for it permanently.

Erasure is checked on **both** keys before the record lands. An erasure keyed on an address must not
be undone by the subject's next direct message, which names them by an account that list never saw.

## 4. The verdict engine

The rows T4 created are processed hourly, per workspace.

**How the work is claimed.** Rows are leased in batches of 8 with a token, so several workers can run
at once and a crash strands nothing. Each decision commits on its own, so a crash keeps whatever was
already decided. The decision and its effect share a transaction, so a row can never say `real`
without the contact it promised.

**The AI call — `capture_counterparty_verdict`.** One sender per call, never a batch. Only that
sender's text is in the prompt, so a malicious message cannot speak for anyone else. Subject, body
and display name are trimmed in SQL first (300 / 1200 / 300 characters) and wrapped in a prompt
fence. The reply must be valid JSON with the exact requested ID, a kind from a fixed list, and a
confidence score. Anything else is rejected.

The model returns one of six kinds:

| Kind | What happens |
|---|---|
| `person` | Create the contact. Queue the domain for `site_triage` |
| `role_mailbox` (e.g. `support@`) | Keep the mail visible. Create no contact — there is no person to record |
| `organization_sender` | Same as above |
| `newsletter` | Hide the mail, and mark the domain as "not a company" |
| `transactional` | Same as above |
| `spam` | Same as above |

Marking the domain matters separately from hiding the mail. A newsletter company has a real website,
so without the mark, company triage would create it anyway the next time a named employee writes from
that domain. A domain an admin approved manually is never overwritten, and personal mail domains are
skipped.

**Low confidence is safe.** Below 0.7 the sender is asked once more on its own. Still below, the row
becomes `unsure`. It is never guessed into `noise`, which is the only verdict that hides anything. A
low score costs an extra question, never a wrong deletion.

**`unsure` goes to a human.** A proposal appears in the review queue, pointing at the *message*, since
the sender has no record yet. **Accept** creates the contact. **Reject** does nothing at all: the mail
stays where it is. Proposals can only add, so an old or wrongly-rejected proposal can never delete
anything.

**Noise is hidden first, deleted later.** The mail is hidden immediately, and its content is redacted
after the undo window. The scope is narrow: only inbound, unlinked mail from an address we have never
written to, and that no live person holds as correspondence. An address a channel connector merely
vouched for does not protect it.

**What runs each hour, in order:**

1. **AI** — judge senders that are due. Skipped if no model is configured.
2. Retire rows that used up their retries, and close proposals a human rejected.
3. Create review proposals for `unsure` rows.
4. Expire proposals that stood too long.
5. Hide new mail from senders already judged as noise.
6. Redact noise whose undo window has passed.

Steps 2–6 run even with AI switched off. Turning off AI does not mean keeping the content of messages
the workspace already decided were noise.

## 5. What a dropped message stores

"Dropped" means four different things, and only one of them stores nothing.

| Kind of drop | Activity row | Raw payload | What is stored |
|---|---|---|---|
| Connector filtered it out (a reaction, a bot) | no | no | Nothing. It never reaches the core |
| Connector could not build a valid record | no | no | The unit's own log row and a `record_dropped` event |
| **Internal-only** (section 2, step 2) | **no** | **no** | One `system_log` row: `capture_internal_dropped`, reason `internal_only`, plus the message's provider ID. **No address, subject or body** |
| T2 / T4 / `noise` verdict | **yes** | yes | The message, plus a decision row and a log row saying why no contact was made |

The internal-only check is the only one that discards message content, and it runs before the raw
payload is stored, so no copy exists anywhere. Storing an address in the log would leak exactly what
dropping the message was meant to prevent.

The other three keep the **message** and only withhold the **contact**. A `noise` verdict is the only
path that later deletes content, and only after the undo window.

## 6. Settings and limits

**Changeable at runtime, per workspace. Takes effect on the next message:**

| Setting | Where | Controls |
|---|---|---|
| Own mail domains | Capture settings (admins write, everyone reads) | Which messages count as internal, and are therefore dropped |
| Personal-mail domains | `POST /v1/capture/consumer-mail-domains` — additions and exceptions on top of a built-in list of ~8,700 domains | T3: create a person but no company |
| `auto_enrich` | `PATCH /v1/capture/settings` | Whether new companies are enriched automatically |
| Approved sender domains | People settings | Lets an admin allow a domain a verdict blocked |

**Changeable in `margince.yaml`. Requires a restart:**

| Key | Effect |
|---|---|
| `capture.transactional_extra` | Extra mail-infrastructure domains for T2 |
| `capture.transactional_never` | Domains that must never be treated as infrastructure. Wins over everything |
| `capture.freemail_extra` / `freemail_never` | **Still accepted, but ignored.** This list moved to the API above; the server logs a warning at boot |

**Fixed in code today. Changing one means a code change:**

| Limit | Value | Meaning |
|---|---|---|
| `PendingDeferralCap` | 500 | Maximum open questions per workspace. Past this, messages still arrive but are not judged |
| `PendingDeferralDomainCap` | 50 | Maximum open questions from one sender domain, so one domain cannot use up all 500 |
| `verdictConfidenceFloor` | 0.7 | Below this: ask again, then give up and ask a human |
| `PendingMaxAttempts` | 2 | Verdict retries before the row becomes `unsure` |
| `NoiseUndoWindow` | 7 days | How long hidden mail can still be recovered before redaction |
| `UnsureReviewWindow` | 30 days | How long a review proposal stands |
| `noiseVerdictReach` | 14 days | How far back a `noise` decision applies to later mail |
| Lease / batch / cap | 45 min / 8 / 200 | Claim lease, batch size, senders judged per run |
| Message size limits | section 1 | What one message may store |
| Schedule | 1 hour | Declared in `backend/api/jobs.yaml`. Changing it is a contract change plus regeneration |
| Capture-activity window | 24 hours | The trace's retention, swept hourly. The read and the sweep share one constant, so the API cannot show rows the sweep has decided to delete |

When either cap is hit, a `capture_deferral_capped` log row records **which** one, so "the queue is
full" and "one domain is flooding it" are never confused.

## 7. Seeing it for yourself: Capture activity

Every decision above is readable, per member, in **Settings → Capture activity**.

The tab shows the last 24 hours of the reader's own connections: a count per outcome, then a row per
message saying when it arrived, which connector carried it, what the pipeline decided, and — where
the decision needs it — the reason that changes what that decision means. A deferred message shows
what later became of its sender, read from the disposition ledger.

That last column is **mail rows only**. The ledger belongs to the mail ladder and is keyed on an
address. Without the guard, a direct message would inherit whatever verdict is pending for the same
human's mail, telling a member that a conversation they already answered is "waiting on a verdict". A
channel record has no ladder verdict, which is not the same as having one that is pending.

| | |
|---|---|
| Who sees what | Your own connections need no grant — it is your own data. Rows from a workspace-owned channel binding (a Telegram bot, a Zalo OA) belong to no member, and are shown to holders of the `capture_trace` object. A grant never reaches a colleague's mailbox |
| Where the numbers start | At the ingress gate. What a connector filtered on its own side is not comparable between connectors, so it is excluded and the page says so |
| Scope | Captured messages. Lead capture has its own outcomes and is not shown here |
| Retention | 24 hours, deleted by an hourly sweep |

**By default the trace stores nothing about the message itself**: no address, no subject. A row whose
message was dropped as internal shows the time and the connector, and says *content not stored*. That
is the honest answer, since the drop exists so that no copy is kept.

`capture.trace_payloads: true` in `margince.yaml` changes that. Each traced message then also keeps
its sender and a bounded subject for the same 24 hours, including internally-dropped mail, which is
the case an operator turns it on to diagnose. It is **off by default and settable only in the
deployment file** — no API, no in-app switch — because it retains correspondence the CRM otherwise
refuses to store. An erased subject's address is never written whatever the posture says, and an
erasure request inside the window reaches what is already there.

Operators also get `margince_capture_outcomes_total` on `/metrics`, counted per outcome since process
start. It makes a change like "every message from that mailbox has been dropped as internal since
somebody registered a domain" visible as a slope to alert on.

## 8. Examples

Assume `acme.com` is a registered own domain, and the client is `dana@client.io`.

| Situation | What happens |
|---|---|
| A colleague messages you | **Dropped** at section 2, step 2 (internal-only). Only a log row is written |
| A colleague sends you a recap of a client meeting, client not on the message | **Also dropped.** Everyone on the message is internal. A recap *about* a client is not correspondence *with* one |
| A colleague emails the client and copies you | **Kept.** T0 switches to `dana@client.io` and judges the client |
| The client replies, and we have emailed them before | **T1** — contact created immediately |
| A stranger writes for the first time | Message kept, **T4** — no contact until the verdict engine answers |
| …verdict `person` | Contact created, owned by the member whose connection captured it. Domain queued for `site_triage` |
| …verdict `spam` | Mail hidden now, domain marked not-a-company, content redacted after 7 days |
| …confidence below 0.7 twice | `unsure` — a proposal goes to the review queue. Rejecting it changes nothing |
| A DocuSign envelope arrives | Message kept, **T2** — no contact, and `eu.docusign.net` never becomes a company |
| A newsletter from someone you have emailed | **T1 keeps them.** A known contact is not infrastructure |
| A first-time sender at `gmail.com` | **T3** — person created, no company |
| Someone mails you from 60 fresh addresses on one domain | The first 50 get queued. The rest arrive unjudged, with a `capture_deferral_capped` log row |
| The same message is polled twice | Nothing happens the second time |
| A member's permissions were reduced after connecting | Their next poll runs with the reduced permissions |
| A stranger sends a direct message on a messaging channel | Message kept, **no tier runs**. An ownerless person is created at once and the account bound, so the message can be replied to |
| …and the connector also knows their address, and its source declared the `email` merge key | The person already captured from that address is **adopted**. The account is bound onto them, not onto a second contact |
| …but the source declared no merge key | Refused at the gate, naming the missing declaration. The address is not silently dropped |
| A DM from someone whose mail verdict is still pending | The trace shows the DM's own outcome, never the pending mail verdict |

## 9. What a connector must provide

| Field | Requirement | If you get it wrong |
|---|---|---|
| `Key` | The provider's own ID, identical every time it is read | A duplicate is created on every poll, and **nothing reports an error** |
| `Addresses` | Everyone on the message, including your own user. No blanks. May be empty only when the counterparty carries no address | The internal-only check stops working |
| `Domain` | Lower-case mail domain | Suppression rules treat a missing value as "keep" |
| `ThreadKey` | Prefixed with the provider name | Two providers can collide and merge unrelated conversations |
| `OccurredAt` | The provider's timestamp | The timeline is ordered by when we polled, not when things happened |
| `Raw` | The original payload | You lose the original record |
| `Counterparty.ChannelIdentity` | On a channel record: both halves (provider and account id), or neither | Half an identity is refused. An empty one lands a message with no reply box |
| `Counterparty.Email` on a channel record | Send it whenever the provider knows it, and declare `email` in the source's `Merges` | No declaration, no record. No address, and a colleague already captured from mail becomes a second contact |
| `Activity.ChannelProvider` | On a `message` and nothing else, naming a channel the unit declared | A non-message naming a transport is a note the reply path would answer on |

The gate can check all of these except `Key`. Getting `Key` right is on the connector.

The last three follow one rule: **a unit sends every field its provider gives it, and decides nothing
about identity**. Which of those fields the ladder may match on is the core's call, read from the
source's declaration. Dropping an address you hold, to fit a shape, throws away evidence the ladder
was entitled to.

## References

### Code

| Topic | File |
|---|---|
| Record type, size limits, results | `backend/pkg/extension/ingress.go` |
| The merge-key vocabulary a source declares | `backend/pkg/extension/mergekey.go` |
| A unit-supplied channel and its transport | `backend/pkg/extension/channel.go`, `internal/compose/extchannelsend.go` |
| The ingress gate | `backend/internal/compose/extingress.go` |
| Auto-capture and the internal-only check | `backend/internal/modules/capture/sink.go`, `sinkmailgates.go` |
| The tier ladder | `backend/internal/modules/capture/sinkensure.go` |
| The channel path: shape, admission, erasure lock | `backend/internal/modules/capture/sinkchannel.go` |
| Minting or adopting the human behind a channel account | `backend/internal/modules/people/ensurechannel.go`, `ensurechanneladopt.go` |
| Address-as-identity vs. address-as-correspondence | `backend/migrations/core/0269_person_email_correspondence_evidence.up.sql` |
| The decision ledger and its caps | `backend/internal/modules/capture/pending.go`, `pendingcap.go` |
| Review queue and sweeps | `backend/internal/modules/capture/pendingreview.go`, `pendingsweeps.go` |
| Own domains, personal-mail list, settings | `owndomainstore.go`, `freemaildomain.go`, `baselinelist.go`, `settings.go` |
| T2 registry and its config keys | `capture/transactional.go`, `platform/deployconfig/capture.go` |
| Verdict engine, prompt, sweeps, accept | `backend/internal/compose/captureverdict{,ask,sweeps,accept}.go` |
| AI task names and routing | `internal/modules/ai/tasks_gen.go` (generated), `internal/compose/brain.go` |
| Company triage trigger | `backend/internal/compose/capturedomaintriage.go` |
| Job schedules and timeouts | `backend/api/jobs.yaml` |
| An example connector | `extensions/relay-probe/` |

### Decisions

| Reference | Topic |
|---|---|
| ADR-0063 | Automatic contact creation |
| ADR-0072 / A118 | The tier ladder, the decision ledger, the confidence floor, hide-then-redact |
| ADR-0082 / A127 | The internal-only drop and the own-domain list |
| ADR-0069 | The extension tier |
| ADR-0107 / A158 | A message names the transport that carried it, separately from its kind |
| CAP-PARAM-5 / -6 / -7 | Personal-mail domains, the transactional registry, workspace capture settings |

### Related pages

[capture-connectors.md](capture-connectors.md) · [extensibility.md](extensibility.md) ·
[write-backbone.md](write-backbone.md) · [ai-runtime.md](ai-runtime.md) ·
[privacy-and-consent.md](privacy-and-consent.md)
