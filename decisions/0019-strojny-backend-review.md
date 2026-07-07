# 0019 — The Strojny backend review: guards, values, enums, and the invariant-enforcement ladder

Date: 2026-07-07. Implements gradionhq/margince-poc-v1#16 (Lars Strojny's
backend review, post architecture fixes). Every finding was verified against
the code before acting; all confirmed. This record ratifies the decisions the
fixes embody — the findings themselves and their per-file resolution live in
the PR ("Feedback Lars Stroiny").

## 1. Concurrency: guard, not version

Optimistic locking was optional (`Patch.Apply(…, ifVersion *int64)`, nil ⇒
last-writer-wins). Ratified rule: **every mutable-row update carries SOME
concurrency guard** — the client's If-Match version, a held row lock, or a
checked conditional write (RowsAffected CAS). Not "version everywhere": the
contract keeps If-Match optional (data-model §1.3a), and internal flows
(merge, promote, sweeps) hold locks, which is the stronger tool there.

Mechanically: storekit's `Apply` is gone. `ApplyWithVersion` is the CAS;
`ApplyGuarded` is the client seam (version when given, else it takes the row
lock); `ApplyLocked` takes a `RowLock` witness mintable only by
`LockRow`/`LockPair` (pair = both rows ordered by id, the deadlock-safe merge
prelude). An unguarded update is no longer expressible through storekit, and
`updateguard_test.go` keeps raw SQL honest (by-id UPDATEs on versioned tables
need a guard marker or a rationale-keyed waiver).

This closed three real races: merge target archived mid-merge (now LockPair),
concurrent promotes minting duplicate persons and phantom events (lead lock +
RowsAffected), offer supersede/accept (witness from `visibleOfferLocked`).

## 2. Consent merge semantics

`mergeConsent` flipped the survivor to withdrawn with no proof event and the
module doc contradicted the code. Ratified: **A's withdrawal propagates with
an appended consent_event** (source='merge'; a state change without proof
voids Art. 7(1) demonstrability), **B's existing rows always win otherwise**,
and **A's rows for purposes B lacks travel WITH their proof chain** — a merge
asserts one human, so a consent that human granted stays proven (the same
carry-through lead→person promotion does; the spec's person_consent notes are
the precedent). `consentproof_test.go` gates the pairing for every module.

## 3. Values and enums: parse, don't validate

`shared/kernel/values` (Tier-0, stdlib-only) holds Email/Phone/Domain/Money/
Slug/Timezone; constructors normalize once at the store Input seam and return
`*values.ParseError` → the 422 shape at httperr. The frozen apperrors registry
gets no validation sentinel — the MalformedCursorError pattern is the ratified
route. Phone is a hand-rolled minimal E.164 (no country-code inference:
carrier metadata doesn't belong in Tier-0); the schema's "E.164 normalized at
write" comment is now true for new writes, existing rows unbackfilled.

Domain enums the stores branch on are typed per module (LeadStatus,
DealStatus/StageSemantic, ConsentState, PromoteTrigger) with a Parse at the
seam; `enumsync_test.go` derives each const set and its column's
CHECK (IN (…)) set from the tree and fails on drift. Offer status reuses the
generated contract enum — no second vocabulary.

## 4. Structured shapes leave jsonb

person.social → the `person_social` relation; person/organization address
jsonb → the contract Address's six columns (0051). The schemaless jsonb
columns (audit images, envelopes, raw, snapshots) are ratified as jsonb.

## 5. Search linguistics (0052)

unaccent everywhere via the IMMUTABLE `f_unaccent` wrapper; names stay
'simple' (+ pg_trgm quick-find); activity free text gains `language`
('de'|'en', NULL = no stemming, never guessed) with stemmed AND simple tokens
indexed; setweight ranks names/subjects above bodies; one query parser
(websearch, accent-folded). The `language` column is a data-model addition —
logged as spec feedback.

## 6. Typed entity ids

One generic phantom-tag `ids.ID[K]` (embedding UUID; `[0]K` makes cross-entity
conversion a compile error), per-entity aliases, `ids.From[K]` as the one
greppable widening point at the contracts edge, `ids.Ref` for the polymorphic
seams. pgx carries them natively (uuid/uuid[] registered per connection);
`TestTypedIDsRoundTripThroughPgx` is the proof gate. Rollout is per-module,
leaf-first.

## 7. Row-scope stays call-site — for now

The auth primitives now reject unknown table names themselves (defense in
depth under the callers' allowlists) and `rbacgate_test.go` pins "every store
entry point reaches an auth gate" with a rationale-keyed waiver map.
**DB-level row-scope (a second GUC + per-shareable-table policies, the same
posture as workspace RLS) is the recorded direction** — it is the only
enforcement that survives a forgotten call site — but it is its own
workstream: per-request principal binding, policy-per-table design, and a perf
pass. Until then the fitness gates are the invariant.

## 8. Declined: sqlc

storekit's dynamic Patch/predicate/keyset machinery and the RLS transaction
wrapper are exactly what sqlc's static model cannot express; adopting it would
bifurcate the store layer and add a second codegen pipeline for marginal gain.
Revisit only if hand-written SELECT maintenance becomes a real sink.

## The ladder (the review's closing point, adopted)

Prefer, in order: **types** (values, typed ids, enums) → **DB constraints**
(CHECKs, composite FKs, RLS) → **derived fitness tests** (updateguard,
consentproof, enumsync, rbacgate — obligations derived from the tree, waivers
carrying rationales, stale waivers failing). Call-site discipline is the thing
that keeps failing; it is now the fallback, never the plan.
