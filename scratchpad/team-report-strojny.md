# Strojny backend review — final report

**Issue:** #16 · **PR:** #21 (`feat/lars-strojny-feedback`) · **Decisions:** `decisions/0019` · **Date:** 2026-07-08

Lars Strojny reviewed our backend and raised ~10 areas. We verified every finding
against the code first — essentially all were real, two were worse than he wrote.
Everything below is implemented, tested, and in PR #21.

---

## The decisions we made (and why)

1. **Locking: "guard, not version."** Every update of a mutable row must now hold
   either a version check (If-Match) or a real row lock — an unguarded by-id
   UPDATE is no longer even expressible in our code. A test derives the list of
   versioned tables from the migrations and fails any update that skips the guard.
   Open product question (filed): should If-Match become mandatory on the API?

2. **RLS row-scope: DECLINED (founder decision, made at the start).** The review
   suggested moving per-user/per-team row visibility into the database as
   row-level-security policies. We decided **against** it: it's a big separate
   workstream (per-request identity in the DB, a policy per table, performance
   work), and our application-layer gates plus a fitness test ("every store entry
   point passes an auth gate") already hold the line. **Important:** workspace
   tenant isolation is still enforced by database RLS on every table — that is
   untouched. Only the finer sharing check stays in code. (`decisions/0019` §7)

3. **sqlc: DECLINED.** Our store layer is built on dynamic patches, predicates,
   and an RLS transaction wrapper — exactly what sqlc's static SQL model can't
   express. Adopting it would split the store layer in two for little gain.
   (`decisions/0019` §8)

4. **Consent merge semantics ratified.** When two people are merged, a withdrawal
   always wins and now writes its proof event in the same statement; consent rows
   travel with their original proof chain. A fitness test fails any code path
   that changes consent state without the paired proof event.

5. **Parse, don't validate.** Email, phone (E.164), domain, money, slug, timezone
   are now real types parsed once at the store boundary — bad input is a 422, and
   a half-money (amount without currency) is unrepresentable in Go **and** blocked
   by a database CHECK.

6. **The meta-rule (now house policy):** invariants enforced at call sites keep
   failing. The ladder is: types first → database constraints → derived fitness
   tests (which compute their obligations from the tree, so they can't go stale)
   → call-site discipline only as a last resort.

---

## What we built (the ten workstreams)

- **WS1–2 Concurrency:** fixed three real races — merge-target TOCTOU (pair
  locks), duplicate-person-on-concurrent-promote (which also emitted phantom
  events), offer supersede. Each proven by a race test against real Postgres.
- **WS3 Consent merge:** proof events paired with every state change (above).
- **WS4 Deal money:** both-or-neither amount/currency, in code and as a DB CHECK.
- **WS5 Row-scope hardening:** auth primitives reject unknown table names;
  fitness test pins the gate coverage. (DB-level RLS declined — see decision 2.)
- **WS6 Value objects:** the parse-don't-validate types (decision 5). Found and
  fixed along the way: phone E.164 was documented but enforced by nothing, and
  the API's address field was silently dropped — both now real.
- **WS7 Enums:** typed vocabularies (lead/deal status, stage, consent state…);
  a test derives Go const sets and SQL CHECK sets from the tree, fails on drift.
- **WS8 JSON→relations:** person social links and addresses moved out of jsonb
  into proper columns/tables with RLS.
- **WS9 Typed IDs (finished this session):** one generic `ids.ID[K]` type —
  passing an organization id where a person id belongs is now a **compile
  error**. All 14 modules converted (people first as the pattern, then the
  remaining 13 this session, one green commit each). Polymorphic seams (activity
  links, list membership, audit envelopes) deliberately stay untyped — they
  legitimately point at any entity.
- **WS10 Search/FTS:** accent-folding (Müller≡Muller), typo/fragment quick-find,
  per-language stemming on activities (Vertrag≡Verträge), weighted ranking —
  each proven by integration test.

---

## Getting PR #21 mergeable (this session)

- **DCO gate (was red):** all branch commits now carry the required
  `Signed-off-by` line; history rewritten once and force-pushed. Green.
- **Coverage gate (was 79% vs 80% required):** added real tests for the
  least-covered new code — the value-object DB read/write seams (a broken one
  silently corrupts stored data) and the typed-ID type vocabulary. Kernel
  packages now 91–97%; aggregate projects ~82%.
- **Craft cleanup (all 43 warnings, at the founder's request):** 6 were false
  positives (Go's DB interfaces mandate `any`) — waived in-source with reasons.
  The other 37 were size findings — files >500 lines, functions >80 lines,
  mostly pre-existing. We split 9 big files by concept and extracted helpers
  from ~29 long functions across 12 packages. **Zero behavior change** — code
  moved, not rewritten — verified by the full merge gate plus the complete
  real-Postgres integration suite (31 packages green). The PR diff now scans
  **0 blocker / 0 major / 0 minor**.
- **Spec pushed upstream:** the data-model updates from the review (FTS
  linguistics, guard-not-version, money CHECK, E.164, consent-merge) are on
  `margince-foundation` main.

## What we did NOT do (and why)

- **No DB-level per-row RLS** — declined, see decision 2. Not a gap: a fitness
  test guards the app-layer enforcement.
- **No sqlc** — declined, see decision 3.
- **No "ban raw UUIDs in signatures" auto-check yet** — a naive version would
  flag the many legitimate untyped seams. Needs design; tracked follow-up. The
  conversion itself is compiler-enforced either way.
- **Issue #16 reply not posted** — drafted (`scratchpad/issue16-reply-FINAL.md`),
  outward-facing, awaiting founder approval.
- **No behavior changes anywhere** — the whole session is type safety, tests,
  structure, and process gates.

## Status

**MERGED.** PR #21 landed on `main` (squash-merge, 2026-07-08) with every gate
green: build/lint/arch/contract-drift, craft 0/0/0 on the PR diff, the full
real-Postgres integration suite, DCO, and the SonarCloud quality gate
(new-code coverage 80.1% ≥ 80%, 0 new issues, 0 hotspots). The spec updates
are on `margince-foundation` main. Open items: post the approved reply on
issue #16, and the tracked follow-up for the raw-UUID fitness gate.
