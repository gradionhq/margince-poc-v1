# ADR-0062 — The dedupe review queue is its own table of record pairs

**Status:** Active
**Decided:** 2026-07-17

## The decision

A probable-duplicate pair that needs a human decision goes into a dedicated
table, `dedupe_candidate`, not into the approval inbox. Each row is one
unordered pair of two people or two organizations, stored with the lower id
on the left, so a pair can exist only once no matter which side detected it.
A human dispositions the row as `merged` or `not_a_duplicate`, and a
`not_a_duplicate` row stays forever and suppresses the pair from every later
sweep. Merging still runs through the one merge path the people module owns;
this table decides nothing, it only holds the queue.

## Why

An approval and a duplicate verdict have different lifetimes. An approval is
one pending action that a human resolves, after which it is history. "These
two are not the same person" is a permanent fact about a pair. Storing a
permanent fact in a row designed to be resolved and forgotten means the next
sweep re-proposes the same pair, and the admin decides it again every night.
The pair also has no natural place in an approval row, which keys on a
proposed change rather than on two record ids.

## What it binds in this repository

- `backend/migrations/core/0096_dedupe_candidate.up.sql` creates the table.
  It carries `entity_type` (`person` or `organization`), the four nullable
  pair columns, `confidence`, an `evidence` object, and a `disposition` of
  `open`, `merged` or `not_a_duplicate`.
- `dedupe_candidate_ordered` is the constraint that makes suppression
  structural: the left id must sort below the right id, so `{A,B}` and
  `{B,A}` cannot both be stored and the unique index cannot be bypassed by
  re-detecting the pair from the other side.
- `dedupe_candidate_shape` allows exactly the two id columns the
  discriminator names and no others, so every id keeps a real foreign key
  instead of being an untyped uuid.
- `evidence` is captured at detection time — the per-field agreement and the
  score's arithmetic — so the queue shows what the detector actually saw,
  not a re-derivation against rows edited since.
- `backend/internal/modules/people/dedupequeue.go` is the store:
  `ListDedupeCandidates`, `GetDedupeCandidate`, `DisposeDedupeCandidate`,
  `executeDedupeMerge` and `UndoDedupeDisposition`.
- `backend/tableownership_test.go` assigns the table to
  `internal/modules/people`, so no other module may write it.
- `backend/api/crm.yaml` exposes `/dedupe/candidates`,
  `/dedupe/candidates/{id}`, `/dedupe/candidates/{id}/disposition` and
  `/dedupe/candidates/{id}/undo`.
- `backend/internal/modules/people/creatededupe.go` is the detector on the
  create path; it also writes a `dedupe_near_match` row to `system_log`.
- The approval inbox is untouched and still lives in
  `backend/internal/modules/approvals/`. The two surfaces stay separate: the
  inbox never holds pair verdicts and the queue never holds pending actions.

## History

Adopted from the retired specification, decided 2026-07-17. Rewritten in
plain language 2026-08-19.

One addition since the source: a disposition can be undone through
`POST /dedupe/candidates/{id}/undo` and `UndoDedupeDisposition`, which
reopens the pair. The source describes the verdict as final once written.
