# The ingress gate, auto-capture, and the verdict engine

What happens to a message **after** a connector has pulled it out of a provider: the gate that admits
the record, the one write that lands it, the ladder that decides whether a contact is created, and
the engine that answers what the ladder deferred.

This is the core-side half of ingress — identical whether the record came from Gmail, Telegram, or an
extension unit such as `dispact-connector`. How a connector reaches its provider is its own business
and stops at one call. For the connector seam itself see
[capture-connectors.md](capture-connectors.md); for the extension tier,
[extensibility.md](extensibility.md).

```text
 a connector          the ingress gate          auto-capture            the verdict engine
  normalize    ────▶   refuse, or bind   ────▶  ONE transaction:  ────▶  judge the deferred
  ONE record           the member's             evidence, activity      sender, or ask a human
               ◀────   live authority    ◀────  + the tier ladder
  WHICH messages       WHO the write runs as    WHAT the message is     WHO the sender is
```

Three invariants:

- **The connector normalizes; the Sink writes.** No RBAC, provenance, audit or outbox logic lives in
  connector code — it happens in one place, once per record.
- **Every stage is idempotent**, so an at-least-once poll loop costs nothing.
- **A refusal is loud.** Each gate produces an effect or a record saying why it did not.

## Deterministic or AI

| Stage | Driven by | AI task |
|---|---|---|
| §1 the ingress gate — refusals, authority, provenance | ⚙️ code | — |
| §2 auto-capture — internal gate, evidence, activity, audit, outbox | ⚙️ code | — |
| §3 the tier ladder — T0 · T1 · T2 · T2.5 · T3 · T4 | ⚙️ code | — |
| §4 the verdict engine — claim loop, effects, sweeps | ⚙️ code | — |
| §4 …its **judging** stage only | 🤖 AI | `capture_counterparty_verdict` |
| Post-`real`: does this domain deserve a company? | 🤖 AI | `site_triage` |
| Adjacent (attention labels, not this path) | 🤖 AI | `capture_classify` |

**One decision in this pipeline is a model's**: *what kind of sender is this?* — asked once per
**sender**, not per message, and only for the class T0–T3 could not settle. The answer decides whether
a **contact** is created, never whether the **message** is kept. With no model configured, capture is
unaffected; only judging is skipped.

---

## 1. The ingress gate — ⚙️

Converts and **refuses**; writes nothing. Refusals run in cost order.

| # | Check | Refusal | Why |
|---|---|---|---|
| 1 | Is the named source one this unit **declared**? | `ErrIngressNotDeclared` | A typo is a refusal, not a second provenance namespace |
| 2 | Is this an **unattended** run? | `ErrAttendedIngest` | A caller plus a member is two authorities — the shape where a low-privileged caller acts as somebody else |
| 3 | Is one of the unit's transactions open? | `ErrNestedIngest` | A second pool connection while holding one does not fail, it **hangs** |
| 4 | Is the record within bounds? | `ErrInvalid` | Caps on what a remote party can make this installation store |
| 5 | Did this role compose a capture pipeline? | named error | A bare sink would land activities and silently create no people |
| 6 | Does the member hold one of this unit's user-scoped secrets? | `ErrForbidden` | The deposit **is** the consent |
| 7 | What may that member do **right now**? | — | Resolved fresh every call |

**Bounds (4):** 256 KB raw · 500-rune subject · 32 768-rune body · ≤ 64 addresses, none blank · ≤ 320
bytes per address · thread key ≤ 512 B · natural key ≤ 256 B · non-zero `OccurredAt`.

**Provenance is core-stamped** — the source (`ext:<unit>:<system>`) and `captured_by` come from the
invoking unit, and the published record type has no field for either. **Errors are mapped, not
wrapped**: only the class crosses the boundary.

**Two dispositions, both of which advance a cursor:** `accepted` (the ref names the row) and
`skipped` (the core deliberately kept nothing and committed a breadcrumb). A deliberate drop reported
as a failure would be retried forever.

## 2. Auto-capture: the one write — ⚙️

One transaction, idempotent on `(source_system, source_id)`. A replay writes nothing.

1. **Erasure guard** — a record naming an erased account is refused under that account's own lock.
2. **The internal-only gate** — if *every* address is on the workspace's own mail domains, this is
   colleagues talking. Breadcrumb, drop, `skipped`. Runs **before** the evidence store, so a message
   that got past it is kept whatever happens next.
3. **Raw capture** — the original bytes, append-once. A replay with different bytes keeps the first.
4. **The activity** — RBAC-gated upsert, links, attachments, participants, field provenance,
   `audit_log` (metadata-only after-image) and an `event_outbox` event: the standard
   [write backbone](write-backbone.md). `captured_by` names the connector **and** the human behind it.
5. **The tier ladder**, inside a **savepoint** — a gate fault costs the derivation, never the message.
   Swallowing the error instead would poison the transaction and roll back the activity, the
   evidence, the audit row and the event.

> `Addresses` must name **every** party, including the member's own. The gate asks "is every party
> internal?", and over an empty set the answer is *no* — so an empty list does not opt out of the gate,
> it **disables** it. Same for `Counterparty.Domain`: the suppression tiers read a missing answer as
> *keep*.

## 3. The tier ladder — ⚙️

Runs inside the capture transaction, so no activity ever exists without a disposition. Every tier is
SQL and Go; T4's job is to decide that a model must be asked *later*.

| Tier | Question | Outcome |
|---|---|---|
| **T0** internal | Is the counterparty a colleague? | Judge the **external** party they named instead; wholly internal → nothing |
| **T1** correspondence | Has the workspace **provably written** to this address? | **Create.** Outranks everything below |
| **T2** transactional | Mail infrastructure (DocuSign, SendGrid…)? | Activity stands; no person, no company |
| **T2.5** already decided | Did a prior verdict or human settle it? | Reuse the answer — no new question, no model call |
| **T3** free mail | Consumer domain (`gmail.com`…)? | Person yes, company no |
| **T4** ambiguous | Nobody yet knows | **Nothing created**; a ledger row for the verdict engine |

- **T0 re-targets** rather than skipping: a colleague writing to a prospect with the prospect copied
  is an introduction. Who *wrote* and who a record is *for* are two questions.
- **T1** reads only the "this installation sent it" attestation, never the derived `direction` — a
  spoofed `From: owner` would otherwise whitelist any address it names. One outbound counts unless
  its own words decline ("not interested", "unsubscribe", "kein Interesse"); two always count.
- **T1 outranks a stale terminal verdict**, so "reply to recover" works fully rather than half.
- **T2.5** stops re-billing: without it, every later message re-asks and re-offers a settled question.
  A `real` verdict whose kind names no human (shared mailbox, org sender) resolves to "known, nobody
  to create".

**After the commit** the person is created through the people module's resolver seam — outside the
transaction, so the timeline row is never lost to a resolver fault; a fault is logged for the nightly
reconcile. Creating a person does **not** create their company: the domain is queued for a site read
(🤖 `site_triage`), which decides from the actual website.

## 4. The verdict engine — 🤖 one stage, ⚙️ the rest

T4's ledger rows are drained hourly, per workspace. The model answers a **label**; code does
everything else — claiming, resolving, creating, hiding, suppressing, staging, redacting.

**The claim loop.** The backlog is a query, not a queue the worker holds: rows are leased with a
token in batches of 8, and each disposition commits on its own transaction, so a crash or a budget
stop keeps whatever was decided. The ledger resolution and its effect share that transaction — a row
can never read `real` without the records it promised. Resolution is a compare-and-set, so a replayed
job is a no-op rather than a second creation.

**The AI call — `capture_counterparty_verdict`.** One sender per call, never a batch: the only text
in a prompt is the sender being judged, so there is nobody else for a hostile message to speak for.
Its subject, body and display name are bounded in SQL (300 / 1200 / 300 chars) before reaching the
worker, and wrapped in a per-request prompt fence. The structured answer is validated — the requested
id exactly once, verbatim, a kind from the closed set, a confidence in range — and refused outright
rather than partially believed.

| Kind | Effect |
|---|---|
| `person` | **Create** the withheld records, owned by the granting human; queue the domain for `site_triage` |
| `role_mailbox`, `organization_sender` | Mail stays visible; no contact invented for a mailbox nobody owns |
| `newsletter`, `transactional`, `spam` | **Hide** the mail, and refuse the sender's domain a company |

The switch is exhaustive by construction — a `default` falling through to hiding is how a new kind
would silently start hiding real mail. Refusing the domain is separate from hiding, because a
newsletter publisher has a real website: without it the triage would create the vendor by another
door. It never overwrites a human's admission, and skips free-mail domains.

**The confidence floor (0.7)** re-asks solo once, then settles at `unsure` — never guessed into
`noise`, the only verdict that hides anything. The floor can cost an extra question; it can never
cost a wrong deletion.

**`unsure` reaches a human** as a proposal pointing at the *message* (the sender has no record yet)
and carrying the disposition id, so a stale offer cannot resolve a newer question. **Accept** creates
the withheld records; **reject** does nothing at all — the mail stays where it is. A proposal may only
ever *add*, which is what keeps approvals approve-only-effects.

**Hide, then redact.** `noise` hides immediately and redacts after the undo window, and only within a
narrow scope resolved on the verdict's own transaction: inbound, unattested, unlinked mail from an
address the workspace has never written to.

**The pass, in dependency order:** 🤖 judge (skipped with no model) → ⚙️ retire exhausted + reconcile
declines → ⚙️ stage reviews → ⚙️ age out stale reviews → ⚙️ hide stragglers → ⚙️ redact expired noise.
Steps 2–6 run whether or not AI is configured: **turning AI off is not consent to retain the content
of messages the workspace already decided were noise.**

## 5. What a drop actually stores

"Dropped" is four different things, and only one of them stores nothing about the message.

| Drop | Domain row | Raw evidence | What is stored |
|---|---|---|---|
| Connector-side filter (a reaction, a bot) | — | — | Nothing; it never reaches the core |
| Connector-side unrepresentable record | — | — | The unit's own ledger row + `record_dropped` event |
| **Internal-only** (§2 step 2) | — | **—** | One `system_log` breadcrumb: `capture_internal_dropped`, reason `internal_only`, and the **natural key only** |
| T2 suppression / T4 deferral / `noise` verdict | **activity kept** | kept | The activity, plus a disposition row and a breadcrumb naming the reason |

The internal-only gate runs *before* raw capture precisely so a colleague-only message leaves no copy
of its content anywhere — no `raw_capture`, no `activity`, no address, subject or body in the ledger.
An address in `system_log` would recreate exactly the disclosure the drop exists to prevent.

The other three keep the **message** and withhold only the **contact**. A `noise` verdict is the one
path that later destroys content, and only after its undo window.

## 6. Thresholds: what is configurable today

**Runtime, per workspace — takes effect on the next message:**

| Setting | Where | Decides |
|---|---|---|
| Own mail domains | `capture` own-domain registry (admin/ops write, every role reads) | Whether a message is internal-only — i.e. dropped |
| Consumer-mail domains | `POST /v1/capture/consumer-mail-domains` — `extra` / `never` deltas over a shipped ~8 700-domain baseline | T3: person-yes-company-no |
| `auto_enrich` | `PATCH /v1/capture/settings` | Whether a new company is enriched automatically |
| Bulk-sender domain admission | people store (a human admission is never overwritten by a verdict) | Whether a suppressed domain may become a company |

**Deployment file (`margince.yaml`), restart to apply:**

| Key | Effect |
|---|---|
| `capture.transactional_extra` | Extra mail-infrastructure eSLDs for T2 |
| `capture.transactional_never` | Allowlist that wins over every T2 baseline and prefix rule |
| `capture.freemail_extra` / `freemail_never` | **Accepted but ignored** — the list moved to the workspace surface above; the role warns at boot |

**Compile-time pins — not configurable today** (changing one is a code change, and ADR-0072 pins
several of them):

| Pin | Value | What it bounds |
|---|---|---|
| `PendingDeferralCap` | 500 | Open questions per workspace. At the cap messages still land, they simply go unjudged — an outsider mailing from fresh addresses must not set the AI spend |
| `PendingDeferralDomainCap` | 50 | Open questions from one sender domain, so one throwaway domain cannot fill all 500 |
| `verdictConfidenceFloor` | 0.7 | Below it: re-ask once, then `unsure` |
| `PendingMaxAttempts` | 2 | Verdict retries before a row retires to `unsure` |
| `NoiseUndoWindow` | 7 days | Hidden → redacted |
| `UnsureReviewWindow` | 30 days | How long an unanswered proposal stands |
| `noiseVerdictReach` | 14 days | How far back a `noise` answer settles later mail |
| `pendingLease` · `verdictClaimSize` · `verdictCatchUpCap` | 45 min · 8 · 200 | Claim lease, batch size, senders judged per pass |
| Record bounds | §1 | What one remote message may store |
| Cadence | 1 h | Declared in `backend/api/jobs.yaml` — contract-first, so a change is a contract edit + regen |

Hitting either ceiling writes a `capture_deferral_capped` breadcrumb carrying **which** ceiling: "the
queue is full" and "one domain is flooding it" are not the same event.

## 7. Worked examples

Assume `acme.com` is a registered own domain and the client is `dana@client.io`.

| Scenario | Outcome |
|---|---|
| A colleague DMs you | **Dropped at §2 step 2** (internal-only). Breadcrumb only — no activity, no raw copy |
| A colleague recaps a client meeting to you, client not on the message | **Same drop.** Every party is internal; the recap is chatter about a client, not correspondence with one |
| A colleague mails the client and copies you | **Kept.** T0 re-targets to `dana@client.io`; the ladder judges the client, and the colleague is never recorded as the counterparty |
| That client replies, and you have written to them before | **T1 → create.** Person exists immediately |
| A stranger writes for the first time | Activity kept, **T4 defer**. No contact until 🤖 `capture_counterparty_verdict` answers |
| …answer `person` | Person created under the granting human; domain queued for 🤖 `site_triage` |
| …answer `spam` | Mail hidden now, domain refused a company, content redacted after 7 days |
| …answer below 0.7 twice | `unsure` → a proposal in the review queue. Rejecting it changes nothing |
| A DocuSign envelope | Activity kept, **T2** — no person, no company. `eu.docusign.net` never becomes a company |
| A newsletter from a contact you have emailed | **T1 spares it**: a known contact with a List-Unsubscribe footer is not infrastructure |
| A first-time sender at `gmail.com` | **T3** — person created, company suppressed |
| A stranger blasts you from 60 fresh addresses on one domain | The first 50 defer; the rest land **unjudged** with a `capture_deferral_capped` breadcrumb |
| The same message polled twice | Idempotent no-op: no second row, audit entry or event |
| A member demoted since connecting | Their next poll lands under the narrowed authority — resolved fresh at the gate |

## 8. What a connector must supply

| Field | Obligation | Otherwise |
|---|---|---|
| `Key` | Identical on a re-read | A second copy per poll, and **nothing fails** |
| `Addresses` | Every party, none blank | The internal gate is disabled and colleague chatter lands |
| `Domain` | Lower-cased mail domain | Suppression tiers read silence as *keep* |
| `ThreadKey` | Namespaced by provider | Two sources collide and join a stranger's conversation |
| `OccurredAt` | Provider time | A timeline ordered by this system's scheduling |
| `Raw` | The original as received | Evidence becomes a re-encoding |

The gate refuses all but the first. `Key` stability is the one it cannot see.

## References

### Code

| Concern | File |
|---|---|
| The published seam: `Record`, bounds, dispositions | `backend/pkg/extension/ingress.go` |
| The ingress gate | `backend/internal/compose/extingress.go` |
| Auto-capture: the one write, the internal gate | `backend/internal/modules/capture/sink.go`, `sinkmailgates.go` |
| The tier ladder and the post-commit ensure | `backend/internal/modules/capture/sinkensure.go` |
| The disposition ledger; the deferral ceilings | `backend/internal/modules/capture/pending.go`, `pendingcap.go` |
| The review queue and the ledger sweeps | `backend/internal/modules/capture/pendingreview.go`, `pendingsweeps.go` |
| Own domains · consumer-mail list · settings | `owndomainstore.go`, `freemaildomain.go`, `baselinelist.go`, `settings.go` |
| The T2 registry and its deployment keys | `capture/transactional.go`, `platform/deployconfig/capture.go` |
| 🤖 The verdict engine, prompt, sweeps, accept path | `backend/internal/compose/captureverdict{,ask,sweeps,accept}.go` |
| 🤖 Task names and lane routing | `internal/modules/ai/tasks_gen.go` (generated), `internal/compose/brain.go` |
| The domain-triage trigger | `backend/internal/compose/capturedomaintriage.go` |
| Job kinds, cadences, wall clocks | `backend/api/jobs.yaml` |
| A worked connector on this path | `extensions/dispact-connector/` |

### Spec and decisions

| Pin | Subject |
|---|---|
| ADR-0063 | Counterparty auto-create and the resolver seam |
| ADR-0072 / A118 | The tier ladder, the disposition ledger, the confidence floor, hide-then-redact, audit minimization |
| ADR-0082 / A127 | The internal-only drop, the own-domain set, why a skip advances a watermark |
| ADR-0069 | The stable extension tier |
| CAP-PARAM-5 / -6 / -7 | Free-mail domains · the transactional registry · the workspace capture posture |
| `specs/contract/formulas-and-rules.md` §20 | The zero-rows internal condition |

### Related pages

[capture-connectors.md](capture-connectors.md) · [extensibility.md](extensibility.md) ·
[write-backbone.md](write-backbone.md) · [ai-runtime.md](ai-runtime.md) ·
[privacy-and-consent.md](privacy-and-consent.md)
