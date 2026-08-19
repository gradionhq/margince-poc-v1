# ADR-0022 — Capture semantics are built here; transport is borrowed, and only inside the customer's boundary

**Status:** Active — capture, the raw payload, the connectors, and hybrid
retrieval are built. Re-derivation over stored raw payloads is **not built**;
see below.
**Decided:** 2026-06-17

## The decision

Margince builds the meaning of captured communication and borrows only the
plumbing that moves it. Normalization, entity resolution, deduplication,
provenance stamping, and the links into the record graph are written here and
owned here. Transport is a provider client that runs inside the customer's own
boundary — Gmail, Microsoft Graph, IMAP, Telegram, Zalo — using the customer's
own credentials. No aggregator that syncs a customer's mail through a third
party's servers sits in the capture path. The raw captured payload is stored
alongside the derived rows, so a later model can re-derive from the original
message rather than from someone's earlier guess about it.

## Why

Every competing product in this category keeps the customer's communication in
its own cloud, which makes "own your data" false in the one place it matters
most. Borrowing transport costs nothing there, because the bytes still land in
the customer's database. Keeping the raw payload is what makes the derived rows
correctable; without it, a parsing bug from six months ago is permanent.

## What it binds in this repository

- `backend/internal/modules/capture/` owns the semantics: `mailmap` for address
  resolution, `laddersubject.go` and `freemaildomain.go` for the matching
  rules, `lifecycleaudit.go` for provenance, and `autoenrich.go` for derivation.
- The transport clients are subpackages of the same module and hold no domain
  rules: `capture/gmail`, `capture/graph`, `capture/imap`, `capture/gcal`,
  `capture/telegram`, and the shared `capture/oauthflow`. Each authenticates as
  the customer, not as Gradion.
- `raw jsonb` columns carry the original payload on the captured records —
  `backend/migrations/core/0004_people.up.sql`,
  `0005_organizations.up.sql`, `0006_deals.up.sql`, and
  `0008_activity.up.sql`, whose comment records that the column is deliberately
  off the hot path.
- `backend/internal/modules/search/fuse.go` fuses the lexical and vector lanes
  with reciprocal rank fusion over the `tsvector` and `vector` columns the core
  migrations add to people, organizations, deals, and activities.
- `backend/internal/modules/capture/backfill.go`, `backfillpager.go`,
  `backfillprogress.go`, and `backfillyields.go` walk the customer's mailbox
  history in bounded batches.
- The channel connectors that ship outside the core module are separate units
  under `extensions/` — `dispact-connector` and `zalo-oa` — so a new channel is
  a new unit rather than an edit to core.

## What is owed

Two parts of the decision are sketched rather than finished.

**Re-derivation over stored raw payloads.** The record says a model improvement
should trigger a retroactive pass that rewrites derived fields from the raw
payload it already holds. What exists is history backfill: reaching further back
into the customer's mailbox for messages not yet captured. Re-running derivation
over payloads already stored is a different job, and it is **not built**. It
needs a budget, a provenance rule for the rewritten fields, and an answer for a
human edit that a re-derivation would otherwise overwrite. None of that is
settled.

**The optional reranker.** The record specifies full-text and vector search
fused by reciprocal rank fusion, plus an optional cross-encoder reranker on top.
The fusion is built — `backend/internal/modules/search/fuse.go` holds it, and
`TestHybridRRFAgreementWins` in
`backend/internal/compose/integration/embedding_integration_test.go` proves that
agreement between the two lanes beats a strong result from one. The reranker is
**not built**, and whether it earns its latency is still open; it needs a recall
measurement before it is worth designing.

The messaging caveat in the source is not owed work, it is a standing fact
worth restating: for a messaging network the operator is unavoidably in the
path. Telegram can run in-boundary. WhatsApp cannot be zero-egress at all,
because the on-premises product was withdrawn. Any sovereignty claim in
documentation or marketing has to carve out messaging networks by name.

## History

Adopted from the retired specification, decided 2026-06-17. Rewritten in plain
language 2026-08-19. The source records no amendment.
