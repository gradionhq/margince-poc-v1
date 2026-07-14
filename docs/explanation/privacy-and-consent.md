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
- **The nightly retention evaluator** (`RunRetention`, scheduled in `cmd/worker`, default every 24h) —
  evaluates each workspace's enabled policies and applies the policy's single action to over-age
  records, **one audited transaction per record**. `legal_hold` rows are never auto-acted, and an
  activity is held transitively when any linked person/organization/deal is held. A policy whose scope
  the engine doesn't understand is **skipped loudly**, never half-applied.

## The single-transaction cross-store exception

`privacy` owns exactly one table (`erasure_suppression`) — yet erasure and retention deliberately
**write tables they do not own**: `person`, `person_email`/`_phone`/`_social`, `lead`, `activity`,
`deal`, `attachment`, `embedding`, `raw_capture`, `field_provenance`. That is by design: a data-subject
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
| Retention evaluator | `internal/modules/privacy/retention.go` (`RunRetention`), scheduled in `cmd/worker` |
| Cross-store ratification | `backend/tableownership_test.go` |
| Jurisdiction packs | `internal/shared/ports/jurisdiction/`, `internal/modules/de/` |
