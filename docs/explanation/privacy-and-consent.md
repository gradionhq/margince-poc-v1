# Privacy, consent & the GDPR engines

How Margince meets data-subject obligations: the **consent suppression gate** that guards every
outbound send, and the **privacy engines** (erasure, subject-access, retention) that a fulfilled
request executes. Two modules cooperate — `consent` owns the gate and the case queue, `privacy` owns
the machinery — and they are stitched together at the composition root, never by a sibling import.

## The default-deny suppression gate (`consent`)

`consent` owns per-purpose consent: the purpose catalog, each person's current state, and an
**append-only proof log**. The load-bearing piece is the **Gate** — the default-deny check every
outbound surface consults *before anything leaves the workspace*:

- The question is always **per purpose**: a `marketing` grant never authorizes a `profiling` use.
- **Default-deny in every direction** — an unknown purpose, an address that resolves to no subject,
  state `unknown`, and state `withdrawn` all block. A double-opt-in purpose additionally requires the
  confirmed round-trip on the proof log (a granted-but-unconfirmed row does not send).
- A refusal answers `ErrConsentNotGranted` and names only the address — it discloses nothing new.

The gate is spelled once (`consent.NewGate`) and **injected into the send path** (activities) at the
composition root, so consent never becomes an import edge between siblings. Every consent *state* write
also appends a proof row (Art. 7(1) demonstrability) — a fitness test (`consentproof_test.go`) fails any
state write that skips its proof.

## The privacy engines (`privacy`)

`privacy` owns the GDPR machinery a fulfilled request runs. The DSR **case queue** lives in `consent`
(the `data_subject_request` rows + their HTTP surface); the composition root injects privacy's engines
into consent's handlers.

- **Art. 17 erasure** (`Eraser.ErasePerson`) — anonymize the normalized rows in place, purge raw
  capture, embeddings, and attachment bytes, hash the identifiers onto a **suppression list** so
  re-capture can't resurrect the subject, and prove it with a **PII-free audit tombstone** — all in
  **one transaction per record**. Atomicity *is* the guarantee. It refuses a subject under `legal_hold`.
- **Art. 15 subject access** (`AssembleSAR`) — one *privileged* read (needs the `person.delete` grant
  **and** an unbounded row scope) gathers everything held about a person — channels, deals, leads,
  activities, attachments, consent + its proof log, raw capture, field origins — into one export
  package, itself audited (`action=export`).
- **The nightly retention evaluator** (`EvaluateWorkspace`, run as one River job per workspace off
  the `privacy_retention` dispatcher in `cmd/worker`, default every 24h) — evaluates **one**
  workspace's enabled policies and applies the policy's single action to over-age records, **one
  audited transaction per record**, and a tenant whose pass fails fails its own job row.
  `legal_hold` rows are never auto-acted, and an activity is held transitively when any linked
  person/organization/deal is held. A policy whose scope the engine doesn't understand is
  **skipped loudly**, never half-applied.

## The single-transaction cross-store exception

`privacy` owns exactly one table (`erasure_suppression`) — yet erasure and retention deliberately
**write tables they do not own**: `person`, `person_email`/`_phone`/`_social`,
`person_channel_identity`, `lead`, `activity`, `activity_participant`, `graph_interaction_edge`,
`linkedin_connection`, `comms_outbound`, `deal`, `attachment`, `embedding`, `raw_capture`,
`field_provenance`, `preference_token`, `capture_pending_counterparty`, `voice_learning_signal`,
`ai_call` and `ai_call_payload`. The ratified list is the cross-writer map in
`backend/tableownership_test.go`, which gates it — that file, not this page, is the authority.

Four of those are worth naming for *why* nothing else can reach them. A **channel identity** is the key
an inbound message would re-bind the subject by, so it must die in the same commit that hashes it onto
the suppression list. A **LinkedIn ghost** holds the subject's name, employer and address, imported
from a colleague's export without the subject ever being asked, and is invisible to every person-keyed
clause because a ghost is not a person row. The **interaction edge** would otherwise be left to a bus
consumer, and an Art. 17 obligation discharged by an event is one that fails silently when the bus is
behind. And a participant's **address arm** exists precisely for a party who never became a record, so
it survives the `person_email` purge and would keep the erased address re-matchable.

That is by design: a data-subject
obligation must reach **every** store that holds the subject, in **one transaction per record** —
routing each purge through the owning module would trade away the atomicity that is the guarantee.

This is the one sanctioned exception to "a module writes only its own tables." Every such write is
**ratified per table** in `backend/tableownership_test.go` with a self-contained rationale; a reasonless
or stale waiver fails the test. See
[reference/modules.md](../reference/modules.md) for the ownership map and
[write-backbone.md](write-backbone.md) for the write shape these purges still ride.

## Jurisdiction retention floors

A destructive retention action must not violate a statutory floor. Country packs register through the
`ports/jurisdiction` seam and **compile into the binary by a blank import** — core code never names a
jurisdiction. The **`de`** (German) pack declares GoBD retention classes; the retention evaluator takes
the strictest compiled-in **commercial-correspondence** floor and shields external business
correspondence (a *Handelsbrief*) from destruction below it — while an internal note or task, which is
not correspondence, carries no floor. A fitness test pins that boundary (a 400-day email survives; a
same-age note is erased).

## Where the code lives

| | |
|---|---|
| The suppression gate | `internal/modules/consent/gate.go` (`NewGate`) |
| Consent state + proof log | `internal/modules/consent/` (`consent_purpose`, `person_consent`, `consent_event`) |
| Art. 17 erasure | `internal/modules/privacy/erasure.go` (`NewEraser`, `ErasePerson`) |
| Art. 15 SAR | `internal/modules/privacy/sar.go` (`AssembleSAR`) |
| Retention evaluator | `internal/modules/privacy/retention.go` (`EvaluateWorkspace`), fanned out per workspace by `internal/compose/jobs_privacyretention.go` in `cmd/worker` |
| Cross-store ratification | `backend/tableownership_test.go` |
| Jurisdiction packs | `internal/shared/ports/jurisdiction/`, `extensions/de/` |
