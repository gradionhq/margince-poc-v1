# Mail history import — the bounded backward scan, and what it costs

Connecting a mailbox starts *standing sync*: from now on, new mail flows in. But a CRM whose history
begins the day you installed it is a CRM with no history, so a fresh connection is also offered one
**bounded backward scan** — the history import. It is the only operation in the product that
deliberately spends money on a pile of data the user hasn't seen yet, which is why it is built
around a number the user agrees to *before* anything runs.

This page follows one import end to end: what the user is shown, where that estimate comes from, what
the run does per page, and what keeps happening after the progress bar fills. For how a connector
itself works — the normalize/Sink split, credentials, the OAuth flow — see
[capture-connectors.md](capture-connectors.md). For the pricing formula the estimate uses, see
[ai-runtime.md](ai-runtime.md#cost--the-meter-collects-tokens-a-rate-table-prices-them). To actually
connect a mailbox and try this, see [how-to/connect-a-mailbox.md](../how-to/connect-a-mailbox.md).

## The screen the whole design serves

After a mailbox connects, the user picks a window and sees this:

```text
  Import your mail history
  Choose how far back to import. You'll see the scope and estimated cost
  before anything runs — and you can skip this entirely.

    ( ) 3 months     (•) 6 months     ( ) 12 months

    ┌──────────────────────────────────────────────────────────┐
    │ Messages in this window: ~3,436                          │
    │ Estimated AI cost: ~0.84 USD                             │
    │ An estimate, not a bill — actual usage is metered and    │
    │ visible as it happens.                                   │
    │                          [ Start the import ]            │
    └──────────────────────────────────────────────────────────┘

  Skip the history import
```

Three windows are offered and nothing else: **3, 6 or 12 months**. A fourth choice, *none*, is
expressed by not starting — it short-circuits to an honest zero with no provider call at all.

Every section below answers a question that screen raises.

## "~3,436 messages" — counting the scope without reading the mail

The count is a real provider call, but it reads **ids only**. The preview pages
`messages.list?q=after:<date>` and counts what comes back — a list of `{id, threadId}` pairs, no
headers, no snippet, no body. The call that actually fetches a message is used by the import loop,
never by the preview.

Two decisions worth knowing:

- **The provider's own count is refused.** Gmail returns a `resultSizeEstimate`, and it is unreliable
  by multiples — a 1,300-message window can read as ~200. That is precisely the made-up number a user
  learns to distrust, and the count also feeds the spend estimate, so its accuracy is a consent
  property rather than a cosmetic one. An exact id count costs a handful of calls and is honest.
- **It is bounded.** 500 ids per page, 40 pages — so up to **20,000 messages are counted exactly**, and
  a larger mailbox reports the counted floor rather than turning a preview into a long scan. The scope
  preview is a bound to consent to, not a contract.

## "~0.84 USD" — pricing work that hasn't happened

An imported message doesn't just land in the timeline; it draws AI work. Three passes, each with its
own unit:

| pass | one unit is | what it produces |
|---|---|---|
| classify | a message | the attention label: commitment / meeting / noise |
| enrich | a newly created person | contact fields read from the mail signature |
| embed | an entity | the vector that makes it searchable |

So the estimate is `Σ per-pass (expected units × per-unit cost)`, and **both factors are measured
rather than assumed**:

```text
  expected units ◀── this connection's last completed import
                     (how many messages one scan captured,
                      how many people it created)

  per-unit cost  ◀── this workspace's last 7 days of real model calls,
                     each repriced at the model that will actually run it now
```

Each factor falls back independently. With no completed import, the units come from a built-in ratio;
with no call history, the cost comes from a **work-shape floor** derived from the size of the actual
prompt templates. Either fallback marks the whole estimate `heuristic` rather than `observed` — a
label the API returns so a reader can tell a measured number from a cold-start guess.

**The honesty rules matter more than the arithmetic:**

- If nothing can be priced at all, the cost field is **omitted**, not rendered as `0`. A fabricated or
  silently-zero number is the worst failure a consent-before-spend figure can have.
- A genuine local-model `$0` is a real price and *is* shown.
- If the estimate read fails outright, the preview degrades to a plain message count. It never blocks
  the flow.

That last point generalizes: **cost is transparency, never a gate.** Nothing in the import path
refuses to run because a number was large or unavailable.

## Starting — one page at a time, resumable

The run is a row with a state machine, paged by the worker:

```text
   queued ──▶ running ──▶ done
                 │
                 ├──▶ cancelled   (the human stopped it, or the connection changed under it)
                 └──▶ error       (a non-transient fault, or too many consecutive transient ones)
```

The worker calls one step at a time; each step pulls **one provider page of 100 messages**, pushes
every message through the same Sink that standing sync uses, and commits the page's outcome together
with the cursor that resumes it. Because the **message** counters and the cursor land in one statement,
a page that fails to commit has scanned nothing the resume point will redo. The **counterparty** counts
are deliberately outside that statement — see below.

Two conditions make that commit conditional, and both mean *this page no longer belongs to the run
being written*: the run reached a terminal state concurrently (a cancel), or the underlying connection
was disconnected and reconnected while the page was out at the provider. A page fetched under a grant
its human has since withdrawn is not history the connection gets to keep.

**Failure is sorted by kind, not by count.** A rate limit or an unreachable provider is waited out on a
doubling backoff — honouring the provider's own `Retry-After` whenever it asks for longer, since coming
back early only spends the next refusal. Anything else ends the run immediately. The transient ladder is
capped at 10 **consecutive** failures, and a committed page resets it to zero: an import that limps
through a flaky morning must not be ended by faults it already recovered from.

## What the run learns about itself

While it pages, the run measures its own yield — and this is what makes the *next* preview accurate.

The counterparty resolver reports what each ensure actually did. `people_created` counts persons
**minted**, not resolved onto — an email from someone already in the CRM triggers no enrich call
either. `organizations_created` counts something subtly different: **domains this run queued for a
company verdict**, because capture creates no companies at all. A run that met twelve new domains did
that work whether or not the crawls have answered yet, and reporting zero would hide it.

A counterparty is counted **the moment it is created**, in its own write, not folded into the page's
commit. That is the third shape of this counter and the only one that survives its edge cases: a
page's total is a batch, and every batch shape lost or doubled it somewhere — a mid-page cancel fenced
the credit away, the retry ceiling ran two writes that both credited, and a single unfenced write lost
a whole page when it failed. Capture is idempotent, so a replayed message never reaches the resolver
again and no retry re-offers those rows to anybody; there is nothing to rebuild a lost batch from.

What that buys, exactly, and what it does not:

- It never **double-counts**. A row is created once, the write runs on that one outcome, and nothing
  retries it.
- It is not **exactly-once**. Creation and counting are different transactions, so a failed counter
  write loses that creation's count permanently (logged at ERROR).
- The loss is **per failure and uncapped** — a database fault spanning a page loses one count for
  every creation inside it.

So read the committed columns as a **floor** on what the run created, never an overcount. Closing the
gap needs a ledger keyed on the created row's id, which is a design decision rather than a cleanup.

The yields are an honest **under-count**, by design. A sender the tier gate defers is resolved by the
verdict engine long after the page that saw it, and the person it may eventually mint is nobody's page
to claim. This is why a run reporting zero people created is read as **"ratio unavailable"** rather than
"zero people": a window whose senders were all already known, suppressed, or deferred reads zero while a
wider window would create plenty. Quoting a confident `$0` for enrich off that zero would be the
dishonest option, so the estimate floors instead and says `heuristic`.

## After the bar fills

**The import finishing is not the AI spend finishing.** The three passes the estimate priced are not
part of the paging loop — they are periodic sweeps over whatever backlog exists:

- **classify** runs hourly,
- **enrich** runs daily,
- **embeddings** ride their own lane.

So a freshly imported mailbox has its timeline immediately, and its labels, contact fields and search
vectors fill in behind it. Two consequences: the estimate covers work that lands after the progress bar
completes, and the live meter — not the preview — is the source of truth for what was actually spent.

One thing does fire on the completing edge: the same-day digest, so a freshly imported mailbox surfaces
on the morning screen instead of waiting for the nightly pass. It fires only on the single step that
moves a live run to `done`, so a lost race can never produce a spurious digest.

While it runs, the status surface reports `messages_scanned`, `captured`, `skipped`, `people_created`,
`organizations_created` and `dedupe_candidates`, alongside the estimate the run started with as the
progress denominator.

## Honest limitations

- **The people/org yields under-count** deferred senders, as above. Deliberate, and the zero case is
  handled rather than papered over.
- **The scope count is capped** at 20,000 messages; beyond that the preview reports a floor.
- **The estimate assumes your next messages look like your recent ones.** Longer mail costs more.
  It is an estimate, not a quote — hence the line under it.
- **The web UI under-reports the estimate today**: it renders cost only when greater than zero, and
  ignores the `observed`/`heuristic` label entirely. So an honest `$0` and the quality signal never
  reach the human, even though the API returns both.

## Where the code lives

| concern | where |
|---|---|
| run control (start, status, cancel), scope count | `internal/modules/capture/backfill.go` |
| the paging loop, commit, failure ladder | `internal/modules/capture/backfillpager.go` |
| yield measurement | `internal/modules/capture/backfillyields.go` |
| the provider-side count and page fetch | `internal/modules/capture/gmail/backfill.go`, `.../graph/backfill.go` |
| the cost estimator | `internal/compose/costestimate/` |
| HTTP surface | `internal/compose/backfilltransport.go` |
| the consent screen | `frontend/src/screens/backfill.tsx` |

## Where to go next

- [capture-connectors.md](capture-connectors.md) — how a connector works, and the one Sink every
  captured record goes through.
- [ai-runtime.md](ai-runtime.md#cost--the-meter-collects-tokens-a-rate-table-prices-them) — the model
  routing, the token meter, and the rate table the estimate prices against.
- [privacy-and-consent.md](privacy-and-consent.md) — what happens to imported personal data afterwards.
- [how-to/connect-a-mailbox.md](../how-to/connect-a-mailbox.md) — connect a mailbox and run one.
