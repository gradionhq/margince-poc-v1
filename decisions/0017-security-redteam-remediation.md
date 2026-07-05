# 0017 — Security red-team remediation batch

Date: 2026-07-05. Closes the findings in
`review_opus_security-redteam_2026-07-05.md` (6-agent adversarial sweep of
the backend). The isolation and authorization core held up under review; the
work here is on the compliance surface and on turning existing guards into a
gate that runs. Findings are keyed to the review.

## Fixed in code

- **C1 — Art. 17 erasure now reaches the activity timeline.**
  `privacy/erasure.go` gained `redactSubjectTimeline`: the on-demand erasure
  transaction now wipes `subject`/`body` of every activity linked to the
  erased person and to **no other** person (the generated `search_tsv`
  refreshes from the empty text, so the subject is no longer full-text
  searchable), mirroring the retention engine's `activity/erase` redaction.
  Shared-thread activities are left intact — redacting them would erase a
  different subject's record.
- **H2 — attachments reached.** The same transaction deletes attachments hung
  off the subject or a subject-only activity; `AssembleSAR` gained an
  `attachments` section so the Art. 15 export is complete. (No object-store
  upload path exists in the PoC yet; the DB row is the only reference held —
  when uploads land, the storage object must be purged in the same spot.)
- **H1 — the invariant, not the point fix.** `backend/piicoverage_test.go`
  adds a PII-table registry and asserts, from the parsed SQL of
  `erasure.go`/`sar.go`, that erasure WRITES and SAR READS every registered
  PII table. A new PII table that skips erasure or export now fails a test
  instead of shipping a silent leak. This is the fitness function C1/H2 were
  symptoms of (Rule 2).
- **M3 — HSTS.** `httpserver.SecureHeaders` now sets
  `Strict-Transport-Security: max-age=63072000; includeSubDomains; preload`.
- **M4 — RFC 7807 on param-parse.** `compose` sets `ErrorHandlerFunc`
  (`paramParseError`) so a bad `cursor`/`limit`/`sort`/UUID answers the
  `application/problem+json` 422 shape (mirroring the malformed-cursor path)
  instead of the generated `text/plain` `err.Error()` leak. It names only the
  offending parameter, never the wrapped parser text.
- **M5 — GoBD correspondence floor decoupled from `kind='email'`.** The
  retention selectors now protect **every non-task activity kind** (email,
  call, meeting, whatsapp, telegram, note) below the statutory floor, not
  email alone — a call log or WhatsApp message is commercial correspondence
  too. See the deferral note below for the 8y/10y classes.
- **M6 — egress gated on `send`, not `write`.** `send_email`/`book_meeting`
  now require `ScopeSend`, `draft_email` requires `ScopeDraft` (the admission
  gate and the mail fakes already expected this — the tool specs had drifted).
  A passport can now edit records yet be barred from sending mail.
  `agents/scope_fitness_test.go` asserts every egress tool requires `ScopeSend`
  and every tool's scope is in the passport vocabulary.
- **M7 — the C1 doc claim retracted.** CLAUDE.md/AGENTS.md and the
  `people/handlers_person.go` comment now state ADR-0055 (a passport on REST
  is governed like MCP), not the false "read-only on REST".
- **L1 — list members row-scoped.** `collections.ListMembers` filters each
  member through the parent list's entity-type visibility predicate at the SQL
  level (a list holds one entity_type, so `LIMIT` stays correct). A shared
  list no longer leaks the existence of out-of-scope rows.
- **L2 — DSR queue is admin-only.** `ListDSRs`/`GetDSR`/`UpdateDSR` now require
  a human with an unbounded row scope (`requireDSRAdmin`), the same bar
  `ListAuditLog` carries — a scoped rep can no longer enumerate everyone's
  data-subject requests. Intake (`CreateDSR`) stays at `person.update`.
- **L5 — unbound stagings are unredeemable.** `approvals.Redeem` now refuses a
  staging whose `PassportID` is nil (latent hardening: redemption is an
  agent-only path, so an unbound authority object must bind to nothing).
- **L8 — `govulncheck` pinned** (`GOVULNCHECK_VERSION`, no more `@latest`).
- **L10 — RLS coverage includes partitioned tables** (`relkind IN ('r','p')`)
  so a future partitioned tenant table cannot escape the coverage assertion.

## M1/M2 — the guards now run

`.github/workflows/ci.yml` runs `make check` **and** `make test-integration`
(Postgres + Redis services) **and** `make vuln` as required checks. The
tree-derived RLS-coverage and erasure-reach fitness tests are
`integration`-tagged; before this they blocked nothing. Now a migration that
forgets FORCE RLS, or an erasure that misses a PII table, fails the merge.

## Deferred — needs an ADR, recorded here so it is not lost

- **M8 (SUSPECTED) — redeem→execute TOCTOU.** `approvals.Redeem` re-checks the
  staged target version inside its transaction, but the concrete write runs in
  a **separate** transaction and the staged version is not carried into it. A
  concurrent writer in that window could apply an approved 🟡 action to a
  record that changed after the human's yes. Closing it cleanly needs the
  `datasource` seam's `Archive`/`Merge`/`PromoteLead` to accept an optimistic
  `IfVersion` guard (only `Update`/`AdvanceDeal` thread one today), plus
  injecting `If-Match` on the REST replay path — frozen-seam work that should
  land under its own ADR rather than a partial point-fix that covers only the
  money path. Narrow window; needs a concurrent writer.
- **M5 tail — the 8y/10y GoBD classes.** `accounting_records` (8y) and
  `books_and_annual_accounts` (10y) remain declared-but-unenforced because the
  PoC has **no accounting records or books** to bind them to (invoicing/DATEV
  arrive with the fiscal work packages, per `de/pack.go`). When those record
  types land, the pack's `RetentionPolicy.Classify` seam (architecture/14)
  maps each to its class; the floor mechanism already generalizes.

## Acknowledged as PoC-acceptable (unchanged)

M9 (process-global BYOK key), L3 (RemoteAddr rate-limit key), L4 (best-effort
secret stripper), L6 (pre-erasure PII in append-only audit_log — deliberate
under Art. 5 accountability), L7 (dev DB passwords, never read by Go), L9 (CSP
trusts Google Fonts — no exfil channel), I1–I3 — all documented as PoC-scoped
in the review; no change made.
