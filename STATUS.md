# Status — where this stands and where to pick up

> The session-pickup record for this implementation. Whoever works here
> next (human or agent): read this first, then
> [AGENTS.md](AGENTS.md) for the binding rules. Update this file at the
> end of every working session.

## Last session: the onboarding funnel made real + runnable + a real IMAP connector (2026-07-05)

A product-facing session: make the onboarding funnel genuinely testable in a
browser, rebuild it to the design source of truth, and connect a real mailbox.

- **One-command runnable stack (`make dev-tls`)** — kills the curl friction that
  made a browser session impossible. `dev/dev.sh` boots db + migrate + api
  (:8081) + a stdlib Go **HTTPS front door** (`dev/frontdoor`, :8080,
  in-memory self-signed cert) + Vite (:5173), and injects the `.env.local`
  Anthropic key into a scratch routing file. Single Secure-cookie origin at
  `https://localhost:8080`, prod-like. `dev/` is its own go.work module, out of
  the product module. Memory `margince-local-run` updated.
- **The 5-step onboarding funnel rebuilt to the mockup** (`spec design/mockups/
  index.html`) on the Ledger-Green tokens: Read · Confirm · Voice · Results ·
  Connect, rail-less, DE/EN i18n (no-inline-copy + token conformance gates
  green). Step 1 drives the **real** `/coldstart` read-back (verified in-browser
  against stripe.com: grounded fields, evidence snippets, confidence dots, the
  honest omit card for the ungrounded buyer). Step 3 is the Voice-DNA corpus
  builder (opt-in gate, source tiles with the locked sent-email tile, word
  meter + quality bands, starter-voice card). New `onboarding.css` ports the
  mockup verbatim onto tokens. `frontend/src/screens/onboarding.tsx` fully
  rewritten.
- **Auth screen redesigned** (`auth.tsx` + `auth.css`) — a split hero (brand +
  three product promises) beside the form card, replacing the bare centered
  card. `auth.test.tsx` (8) still green.
- **A real IMAP connector** (built by a scoped subagent, reviewed + integrated):
  `POST /v1/connectors/imap/connect` (human-only, cookie-authed) dials a
  mailbox over TLS 1.2+, captures the last N messages as email activities
  through the existing capture Sink (audit + outbox in one tx), returns
  `{connected, mailbox, captured, skipped, contacts}`. Credentials are
  transient — used for the one call, never persisted, never logged. Errors map
  to clean RFC 7807 (login→422 `imap_login_rejected`, unreachable→502
  `imap_unreachable`) with the cause logged server-side, never leaked.
  `capture/imap/` (connector + pure RFC822→activity mapping + unit tests),
  `compose/imapconnect.go` (handler), `capture.Registry.RunTransient` (one-shot
  pull under the caller's live authority). Smoke-tested live against
  imap.gmail.com (bad creds → 422, unreachable → 502). Connect step (step 5)
  wired to it; enter a real email + app-password to pull your inbox.
- **Fixed a pre-existing e2e break**: the auth gate (added the prior session)
  short-circuits to signup without a workspace slug, so every authed-screen e2e
  rendered auth — 24 red. The seed now seeds the slug in localStorage
  (`e2e/seed.ts`); full AC suite green again (AC-onboarding-1 now verifies the
  new funnel).
- **Backlogged** (feedback/18, git-ignored): real speech-to-text as an optional
  cold-start entry accelerator (client-side Web Speech API; distinct from the
  Voice-DNA writing-tone step). Founder-requested.

Gates at close: `make frontend-check` (lint + 89 unit + build) · `make
frontend-e2e` (AC suite) · backend `make build vet lint arch-lint test`
(lint 0 issues) · `make test-integration` (real-PG RLS + HTTP e2e) — all green.
`make drift` passes once the contract + generated files are committed together
(this commit). Deps added: `emersion/go-imap` + `emersion/go-message`.

## Prior session: EP05 scrapeCompany + first-run auth + two pre-existing fixes (2026-07-05)

A working session that shipped the enrichment surface, closed a real
first-run gap, and repaired two pre-existing integration failures:

- **EP05 `scrapeCompany` (B-EP05.13a/b)** — the `enrich` verb on an
  EXISTING org: `POST /organizations/{id}/enrich`, x-mcp-tool
  `enrich`/yellow. Reuses the cold-start fetch + no-guess evidence gate,
  now extracted into ONE shared `evidenceExtractor`
  (`internal/compose/enrichextract.go`) that BOTH coldstart and scrape
  call — no duplicated fetch/extract/gate. Resolves the URL from the
  org's domain (or a `url` override), row-scoped (a hidden org is a 404
  before any egress), stages a 🟡 approval bound to the org, and on
  accept fills only the org's EMPTY fields as `agent:scrape`
  (`people.ApplyEnrichment`, sharing `applyEvidenceFields` with the
  read-back). Integration-tested (stage-bound-to-org, existence-hiding
  404, honest 422 on unreadable / no-domain, accept fills-empty-only +
  exactly-once + reject-writes-nothing) and driven end-to-end against a
  real model (stripe.com → evidence-backed staged proposal).
- **Fixed a pre-existing coldstart-accept breakage** (from last
  session's L8 fix): `approvals.Redeem`'s `PassportID == nil` refusal
  correctly blocks an AGENT from redeeming an unbound authority, but it
  also blocked the HUMAN inbox accept-effect (human-staged coldstart has
  no passport). Redeem now scopes the passport-binding checks to AGENT
  actors; a human reached it through `Decide` (human-only +
  decide-authority + pending→approved once), so an unbound approval is
  theirs to consume. Heals coldstart AND enrich accept; agent-path L8
  protection intact (agent e2e green).
- **Fixed two pre-existing retention/GoBD failures** (from decisions/0017
  M5): the commercial-correspondence floor was broadened to `kind <>
  'task'`, which over-shielded internal `note` and note-kind
  `transcript` bodies from erasure. GoBD §147 correspondence is EXTERNAL
  comms (email/call/meeting/whatsapp/telegram), never an internal note;
  the floor clause is now single-sourced
  (`commercialCorrespondenceFloor`) and excludes `('task','note')`.
- **First-run auth screen (frontend)** — the app had no login/signup UI
  (STATUS's known gap), so a first-time user couldn't start a session in
  the browser. Built `frontend/src/screens/auth.tsx` (signup →
  `POST /v1/workspaces`, login → `/v1/auth/login`) + an auth gate in
  `App.tsx` that probes `/v1/me`. i18n DE/EN, a slug-derive parity test
  (mirrors the server `slugify`), `make frontend-check` green (89 tests).
  Verified the full first-run in a browser: signup → workspace → onboarding
  wizard → coldstart evidence-backed staged proposal (real model).

**Local-run notes (also in memory `margince-local-run.md`):** two dev
gotchas cost real time — the api needs `MARGINCE_ENV=dev` or the
`X-Workspace-Slug` header is ignored (every request 401s "unknown
workspace"), and the session cookie is `Secure` so the SPA must be served
from an HTTPS origin (a dev TLS front door on :8080 → api :8081 + Vite
:5173 gives one secure origin, prod-like). `make dev` sets neither. These
are spec/impl discrepancies worth a `feedback/` note.

Gates at session close: full `make test-integration` green (incl. the two
repaired retention tests + the new scrape suite), `make build vet lint
arch-lint test` green, `make frontend-check` green, `craft static` clean,
and the craft + security review agents clean over three rounds.
**Committed + pushed to origin/main as `b75c6d7`** (contract + generated +
code together, so `drift` is green). The pre-push craft gate passed;
its two MAJOR `long-func` warnings (`server.New`,
`TestColdStartAcceptWritesProfileOntoOrganization`) are advisory-only and
pre-existing (both functions I only added a few lines to). Follow-ups:
frontend `pnpm gen:api` NOT yet run (no scrape UI built yet — run it before
wiring an enrich button); the `MARGINCE_ENV=dev` + Secure-cookie/HTTPS-origin
dev gaps deserve a `feedback/` note.

## Prior status

**Last updated: 2026-07-05 (contract-sync batch closed).** Roughly a
**third-plus** of the 687-leaf-ticket V1 backlog
(`../margince/specs/spec/product/build-backlog/`) is implemented and
gate-verified; every `crm.yaml` operation is implemented — including the
eleven the spec's feedback-04–15 resolution defined — and **EP09 is fully
closed** (the automations editor included). Frontend docs:
`frontend/README.md` + `docs/how-to/run-the-frontend.md`.

## Last session: security red-team remediation (2026-07-05)

Closed `review_opus_security-redteam_2026-07-05.md` (decisions/0017 records
every call). The isolation/authz core held up under review; the work is on the
compliance surface and on making the existing guards a gate that runs:

- **C1/H1/H2 (GDPR erasure completeness) as one invariant.** Art. 17 erasure
  now redacts subject-only activity `subject`/`body` (tsvector refreshes) and
  deletes their attachments; SAR gained an attachments section; a new
  `backend/piicoverage_test.go` fitness test asserts erasure WRITES and SAR
  READS every registered PII table — a new PII table that skips either fails.
- **M3–M7:** HSTS header · RFC-7807 `ErrorHandlerFunc` for param-parse errors
  (no more `text/plain` leak) · GoBD correspondence floor decoupled from
  `kind='email'` (all non-task kinds) · egress tools gated on `ScopeSend`
  (not `write`), draft on `ScopeDraft`, with an `agents/scope_fitness_test.go`
  guard · the false "read-only on REST (C1)" claim retracted per ADR-0055.
- **L1/L2/L5/L8/L10:** list members SQL-row-scoped · DSR queue admin-only
  (`Unbounded`) · unbound approval stagings unredeemable · `govulncheck`
  pinned · RLS coverage includes partitioned tables.
- **M1/M2:** `.github/workflows/ci.yml` runs `make check` + `make
  test-integration` (Postgres/Redis) + `make vuln` as required checks, so the
  RLS-coverage and erasure-reach fitness tests finally block a bad merge.
- **Deferred (ADR-scoped, in 0017):** M8 redeem→execute TOCTOU needs a
  `datasource`-seam `IfVersion` guard on Archive/Merge/PromoteLead; the GoBD
  8y/10y classes await their (not-yet-existing) accounting/books record types.

## Prior session: the feedback-04–15 contract-sync batch (2026-07-05)

One session consumed the spec's feedback resolution end to end
(decisions/0016 records every judgement call; migrations now at **0038**):

- **Contract synced slice-by-slice** (each slice gate-green + committed):
  `GET /passports` (metadata list, token never re-disclosed) ·
  `GET /audit-log` (privacy module's first transport surface; unbounded
  HUMAN only — the agent gate fronts mutating routes, so the human check
  binds at the store) · `issueDoubleOptIn` (supersede-by-expiry, plaintext
  once, audit-only) · `/automations*` (0035: closed in-code catalog,
  instance CRUD with If-Match, created-paused, soft archive, the engine
  fires one run per ENABLED instance with instance params — bootstrap
  seeds the two starters enabled; `automation` RBAC object mirrors
  pipeline) · `/public/booking/{host_slug}` (0036: `booking_page` is the
  ratified second non-RLS resolver table; anonymous edge = slug→tenant +
  per-IP/per-slug throttles + `system:public_booking` principal; consent
  passthrough verbatim into `consent_event`; idempotent booker on email;
  409 slot_taken; `platform/ratelimit` extracted from identity). OAuth AS
  paths deliberately stay OUT of the generated contract (decisions/0016
  §1). gen-agentpolicy now emits gofmt-clean output.
- **Commit security review remediated same-day**: the anonymous consent
  hijack (a booking naming a known email could re-grant a WITHDRAWN
  purpose — closed with `RecordInput.NeverOverrideExisting`, enforced
  in-tx, silent so the page is no consent-state oracle) and booking
  provenance (`source=public_booking`, never `manual`). Both pinned in
  the public-booking integration test.
- **EP09 closed (frontend lane, parallel agent)**: B-EP09.15 automations
  editor at `#/automations` (anti-DSL guard pinned; params form generated
  only from `params_schema`; If-Match enable flip), Settings audit-log +
  passport-list cards, public booking at `#/book/<slug>` with the
  consent-wording byte-equality e2e pin. 81 unit / 35 e2e green.
- **Coldstart ACCEPT executor** (0037): approvals gained compose-injected
  per-kind effects (redeem-then-execute = exactly-once); accepting a
  proposal writes the org (resolve-by-domain or create), fills EMPTY
  columns only, lands an evidence row per field in
  `organization_profile_field` as `agent:coldstart` — the seven
  non-column fields have no data-model home → feedback/16.
- **Lead-score behavioral recompute** (0038): `activity_link` gained the
  lead arm (feedback/17 files the data-model omission), the workflow
  engine gained always-on SYSTEM handlers (invariants are not pausable
  automations), and the §3 formula now recomputes from lead-linked
  replies/meetings on every activity event, exactly-once under
  redelivery, emitting `lead.updated {delta:{score}}`.
- **cold_start golden dataset** (B-EP06.23a): `evals/cold_start/` — 106
  recorded-fixture cases (50/30/26 happy/long-tail/adversarial) emitted
  by the deterministic `tools/gen-evals`; the runner drives the REAL
  shape + no-guess gates in the plain test lane, so `make check` is the
  hard gate; `make eval` runs it verbosely.

Also on disk, untracked: `review_opus_security-redteam_2026-07-05.md` — a
separate whole-repo red-team (headline: Art. 17 erasure misses the
activity timeline + attachments, C1/H1/H2; RLS fitness gates not in the
non-integration merge gate). NOT addressed by this batch (pre-existing
findings, separate remediation) — that file is the next session's
highest-value pickup.

All gates green at session close: `make check` (incl. the new eval
gate), `make test-integration` (full serialized lane),
`make frontend-check`, `make frontend-e2e`.

## Last session: the craftsmanship red-team + full remediation (2026-07-05)

A full red-team against the spec's craftsmanship dossier
(`../margince/specs/research/craftsmanship-loved-and-anti-patterns.md`,
sections A–R) ran seven parallel review passes, then EVERY finding — bad
and okay-ish alike — was fixed (commits `ba713dc`, `7849581`, `e4fb216`).
The interim review file was addressed in full and deleted per instruction;
this list is the durable record:

- **Contract integrity**: `Idempotency-Key` is now implemented per the
  contract (migration 0033, insert-first claim, replay, 409 on digest
  mismatch, integration-tested) instead of silently ignored.
- **Security**: consent double-opt-in tokens are minted server-side,
  hashed at rest, verified + consumed single-use (0034); the MCP tool
  surface no longer leaks raw internal error text (generic message +
  server-side slog); the hosted MCP listener got full timeouts + a body
  cap; SECURITY.md added.
- **Approvals**: clock injected (`now func()`); the pending→expired and
  redemption-window transitions are unit- AND integration-proven via
  backdated timestamps (no sleeps anywhere in the suite now).
- **Structure**: erasure/SAR/retention moved out of compose into
  `modules/privacy`; compose is wiring again. New root fitness gates:
  table-ownership (AST-parsed SQL writes vs a declared ownership map,
  waivers need rationale), errmatch (no `err.Error()` string matching),
  FORCE-RLS coverage (schema-derived), writeshape widened to compose and
  waivers re-keyed by package path.
- **Errors/API**: malformed cursors 4xx centrally; constraint sniffing by
  message text replaced with `storekit` SQLSTATE/constraint-name helpers;
  httperr suppresses infrastructure error text on the wire; multi-statement
  tx bodies wrap errors uniformly across deals/people.
- **Operability**: `/readyz` (pg+redis), `/metrics` (hand-rolled Prometheus
  text: outbox backlog, relay published, pool stats), access log +
  correlation_id-aware slog (one shared `LogHandler` for api/worker/mcp),
  `--log-level`/`--log-format` flags, worker WaitGroup drain, DSN pool
  params no longer clobbered.
- **Tooling/docs**: gen-stubs ported python3→Go (host requirement dropped);
  codegen tooling split to `backend/tools` module (app go.mod lost the YAML
  zoo); depguard collapsed to tree-derived enforcement; jurisdiction ports
  shrunk to `Retention()`; docs/ Diátaxis tree, CHANGELOG.md, .editorconfig,
  .tool-versions, renovate.json, pre-commit hook; decisions/ + feedback/
  re-tracked; AGENTS.md/CLAUDE.md now name all 13 modules + both spine
  shapes.
- **craft gate**: `cli/craft static --strict` is clean over the FULL repo
  (was 83 blockers / 70 majors — every finding fixed or reason-waived
  inline); the LLM arm (`craft review`, five slices over the session diff)
  returned PASS on all slices and its nine findings are closed.
- **craft gate single-sourced** (follow-up the same day): the gate is now
  developed ONLY in the foundation (`../margince/skeleton/cli/craft`,
  commit `893c73d` there) and vendored here verbatim, hash-pinned by
  `cli/craft/craft-manifest.sha256` — `make craft-drift` (a `make check`
  prerequisite) fails any local edit; `make craft-sync` pulls the current
  gate. The gate identity tuple gained `code_version`
  (`p1+r1+e1+c1+<model>`, docs 15 §4 / 17 §1 amended), so a verdict names
  the exact gate source that produced it. The stale fork in the superseded
  `margince-poc` repo is retired (its commit `6b40f0d`).

Not done, deliberately: GitHub CI (owner is adding it; the five failing
`cli/craft/wiring` tests that expect `.github/workflows/ci.yml`,
CONTRIBUTING.md and branch-protection.json will go green with it).

**Incident, recorded honestly**: mid-session a subagent's `git stash`
verification collided with the parallel frontend session's commits and
briefly wiped the uncommitted backend work from the tree; everything was
recovered from the dropped stash's unreachable commit (`63532ff`) and both
gates re-verified before the checkpoint commit. Lesson: agents in a shared
tree must never touch git state; commit checkpoints early.

All gates green at session close: `make check`, `make test-integration`
(serialized real-PG lane), `craft static --strict` (0/0/0), five-slice
`craft review` PASS.

## Previous session: the overnight autonomous build (2026-07-04 → 05)

One agent session built and merged, slice by slice (each gate-green, pushed
immediately). **Every contract operation in `crm.yaml` is now implemented** —
the compose stub fallback is gone; a regenerated contract adding an operation
nothing implements fails the build. Landed, in order:

1. **The five planned blocks**: `modules/ai` (Anthropic BYOK + Ollama +
   offline fake, tiered router, seat-budget guardrail, metering),
   the Surface-B runner + scheduler (suspend→approve→resume),
   `modules/search` (FTS + pgvector + RRF + context graph + Retriever),
   `modules/capture` (connector seam), `modules/consent` (default-deny +
   DOI), the A2 OAuth AS + hosted MCP + ADR-0036 JWS tokens.
2. **Stub closures**: lists/tags, relationships/partners, activity
   lifecycle, pipeline/stage config, record grants, DSRs, deal
   stakeholders, workflow engine + starter library (route_lead,
   stage_change_create_task).
3. **Scheduling** (0030 `activity.host_user_id`; availability is
   row-scoped, cross-host booking is admin-gated — decisions/0013).
4. **Cold-start read-back** — the LAST stub: SSRF-guarded fetch → routed
   extraction → server-side no-guess gate → staged `coldstart` approval
   (the staged row IS the proposal). api role needs `--ai-routing` or
   `--ai-fake`, else explicit 501.
5. **GDPR arm**: retention evaluator (worker-ticked nightly, §3.4 seeded
   defaults at bootstrap), legal hold (never auto-acted, transitive for
   activities), Art. 17 erasure (normalized+raw+vector purge, PII-free
   tombstone, `erasure_suppression` (0031) so re-capture skips — DSR
   fulfillment EXECUTES the erasure), Art. 15 SAR assembly (admin-only).
6. **Runner grounding** (T2 seed retrieval under the agent's own
   principal), intent tools (`catch_me_up_on`, `prep_for_meeting`), MCP
   comms verbs (`draft_email`/`check_availability` 🟢,
   `send_email`/`book_meeting` 🟡) — the send path is spelled once for
   both transports.
7. **Formulas** (`IsStalled` stamps deal reads + backs the `stalled`
   filter; `ScoreLead` reproduces the spec's worked example), seat-derived
   AI budget, capture dedupe → 🟡 merge staging, the §5.2
   structured-output retry/escalation pipeline, the DE jurisdiction pack
   (GoBD floors under the retention engine), and an SPA sweep (search,
   reports, privacy inbox, booking).

Three background security reviews plus a closing adversarial self-review
ran during the night; every confirmed finding was fixed and pushed
(scheduling row-scope/authz, coldstart SSRF hardening + a Unicode
panic in the tag stripper, erasure LIKE-injection + the missed lead
table + SAR admin gate, a DB-level double-booking exclusion constraint
(0032), idempotent dedupe staging, DSR fail-closed fulfillment).

**Operational notes:** migrations are at **0032**; db-up uses
`pgvector/pgvector:pg16` — recreate a stale dev container once
(`docker rm -f fable-pg16 && make db-up && make migrate`). The worker now
also ticks retention (`--retention-interval`) and the api role takes
`--ai-routing`/`--ai-fake`. Spec path note: the sibling spec lives at
`/Users/lars/develop/margince/specs/spec/` and the backlog counts 687
leaves per the validator (older notes said 701).

Session records: decisions/0013 (all build decisions of the night),
feedback/07–09 (spec defects found), README review-loop rules unchanged.

Codex review closure (2026-07-05): all gate-relevant findings fixed.
The last one was the write-shape waiver test citing the gitignored
`feedback/07` file via `os.Stat` — it now carries inline rationales, so
`make check` survives a clean checkout. Remaining accepted risk: OAuth
discovery's `requestIssuer` trusts the raw `Host` header (fine only
behind a Host-sanitizing proxy; revisit before any direct-exposure
deploy).

## EP09 (frontend): 29 of 30 leaf tickets DONE (2026-07-05)

One session built the entire epic in `frontend/` (pnpm + Vite + React 19 +
TS strict + Tailwind 4 + Biome + Vitest + Playwright), gate-green commit
per slice. Done: 09.1 tokens (canon-pinned, dark via data-theme) · 09.2
re-scoped Margince atom library (founder decision: NO gw-ui/Dispact reuse
— feedback/10; foundation v0 committed spec-side at
specs/design/design-system/) · 09.3a trust primitives + 09.3b composed
surfaces · 09.4 shell (canonical 9-item rail, contextual top bar,
data-screen, rail-less exceptions) · 09.5 ⌘K palette · 09.6 Ask FAB ·
09.7 responsive/390px bottom-nav · 09.8 PWA (SW never caches or fakes
/v1) · 09.9 onboarding wizard (connect LAST, honest read-failure) ·
09.10 people/companies/leads lists + 360s on live /v1 (lead segregation,
promote gating) · 09.11 deal Kanban drag-to-advance (terminal = 🟡
confirm) + table + deal 360 · 09.12 approval inbox (edit-then-send via
edited_payload) + Morning Brief (live signals only) + Tasks + Reports
(plan-based explain) + Ask AI (two-tier, no fake chat) · 09.13 client
chrome + Settings governance · 09.14 booking shell · 09.16 i18n DE/EN
(AST no-inline-copy gate) · 09.17/18/19 presentation-edge formatting
(IANA-only zones, IR-verbatim FX) · 09.20 drift gates (tokens, fonts,
colours, Lucide-only glyphs, SW discipline) · 09.21 axe WCAG 2.2 AA ·
09.22 e2e harness (AC-named tests, 390px sweep, PERF-1 <300ms) — 27/27
e2e green, 76 unit tests green.

**Open (updated 2026-07-05, contract-sync batch): NONE — the epic is
closed.** The sync landed, `pnpm gen:api` ran, and B-EP09.15
(automations editor), the Settings audit-log + passport-list cards, and
the public booking consent passthrough are built and gate-green (see
the session block above).
- Follow-ups from the resolution are DONE build-side: writeshape
  waivers re-pointed to events.md §5.3c / the §5 closed-verb law (no
  more feedback-file citations); textMeta is canon (ADR-0040
  amendment) and pinned in tokens.test.ts; foundation design-system
  synced.
- Deviations recorded: no Storybook (the #/design screen + tests are the
  showcase); e2e runs over a network-edge seed mock by default (BASE_URL
  points the same suite at a live backend); auth/login screen not yet
  built (dev flow: session cookie + Settings workspace slug).

Lanes: `make frontend-check` (lint+unit+build) and `make frontend-e2e`
(harness). Packaging (decisions/0014): at prototype parity copy
`frontend/dist` under `backend/web/` for the existing go:embed; the
handwritten prototype still serves `/` until then.

## Pick up here: next blocks (backend)

No half-finished backend slice is in flight. Highest-value next, in order:

- **The 2026-07-05 security red-team file**
  (`review_opus_security-redteam_2026-07-05.md`, untracked at repo root) —
  above all C1/H1/H2: Art. 17 erasure must reach the activity timeline
  (subject/body + FTS) and attachments, via a PII-table registry fitness
  test, and the RLS/schema fitness lanes should gate merges (M1/M2 = CI).
- **EP05 scrape/enrichment** (`scrapeCompany` evidence-or-omit) — reuse the
  coldstart fetcher + stripper.
- **S12b vLLM adapter**; **PERF-7 harness**.
- Spec-blocked, waiting on upstream: feedback/16 (coldstart profile-field
  home), feedback/17 (activity_link lead arm ratification + the lead-score
  override surface).

Done this session:

- **Per-file SPDX headers** — every hand-written `*.go` now carries the locked
  BUSL-1.1 SPDX header (`// SPDX-License-Identifier: BUSL-1.1` +
  `// SPDX-FileCopyrightText: 2026 Gradion`), enforced by
  `TestEveryHandWrittenGoFileCarriesTheLicenseHeader` in `backend/license_test.go`
  (walks the tree; a new file is enrolled the moment it exists). Generated
  `*_gen.go` and the drift-frozen `internal/contracts/` package are exempt.

## Previous session: the spec's red-team fixes landed in code (ADR-0055)

The spec repo fixed the 2026-07-04 design-review findings (fail-open
gate, self-approval bypass, DAG-illegal RBAC read, overloaded SoR seam,
contract mismatches) in commits `b322372` + `47da93d`; this session
implements them here — full record in
decisions/0012:

- **Agents keep REST writes, governed** — the C1 read-only stopgap
  (`ErrAgentSurfaceRestricted`) is withdrawn per ADR-0055. A generated
  route→policy table (`tools/gen-agentpolicy`, drift-linted: every
  mutating contract op MUST carry `x-mcp-tool` or `x-agent-access`)
  drives the compose agent gate: 🟢 admits, 🟡 stages the same approval
  the MCP tool would (retry with `X-Approval-Token`), unmapped routes
  default-deny, tighten-only when annotation and ToolSpec disagree.
- **Self-approval closed at three layers** — approve/reject (+ consent,
  DSR, pipeline/stage config, passport issue/revoke) are
  `x-agent-access: human-only` + cookieAuth-only in the contract,
  rejected by the gate, and re-checked in the approvals service
  (`TestGovernanceOperationsAreHumanOnly`, e2e self-approval test).
- **`shared/ports/authz` seam** — identity implements, compose injects,
  `gate.Admit` re-derives seat + RBAC live per admission (revocation
  binds mid-session) without a platform→modules edge.
- **SoR v1 frozen** — `StageSemantic`/`PromoteLead` lifted into the
  interface; `TestSystemOfRecordProviderV1MethodSetIsFrozen` is the
  interface-diff gate; post-v1 verbs go on `...V2` + capability probe.
- **Contract synced to the spec** (If-Match↔version reconciled,
  `captured_by` readOnly/server-stamped, DDL-aligned enums,
  `approval_required` wire code, scope/seat 403 responses), keeping the
  A1 `/passports` surface in place of the not-yet-built OAuth2 AS
  (deliberate, recorded in decisions/0012). Spec defects found while
  syncing: feedback/04,
  feedback/05.

All gates green at session close: `make check`, `make test-integration`
(cold cache), incl. the new e2e loop: agent 🟢 create lands
agent-stamped → 🟡 archive stages → agent self-approve refused → human
approves → token retry executes once.

## Previous session: post-restructure red-team, all findings fixed

A current-state red-team pass ran after the triad restructure (its
review file is addressed in full and retired to git history). Every
finding is fixed with a regression or fitness test:

- **H1** — an FK argument naming a row-scoped record is now a READ of
  the target: deal organization/partner and organization parent
  references go through `auth.EnsureLinkTarget` (the rule activity links
  already followed), pinned by `TestFKTargetsRequireRowScopeVisibility`
  and made mechanical by the schema-derived
  `TestFK_rowScopedTargetsHaveVisibilityDecision` — every FK to a
  row-scoped table must carry an explicit gated/child-row/server-derived
  classification or the suite fails.
- **H2/H3** — the approval surface now applies the target row's
  own/team scope AND the decision grants uniformly across List, Get,
  approve and reject (`decidable` = grants ∧ target visibility; an
  undecidable approval reads as absent, so a leaked UUID buys nothing —
  a reject is a decision too). `TestApprovalAuthorityHonorsTargetRowScope`.
- **M1** — the write shape is now a fitness function:
  `TestEveryAuditedMutationEmitsAnEvent` (AST scan) fails any module
  mutation that audits without emitting; pipeline config was the one
  ratified audit-only exception (filed as feedback/03, since resolved —
  see the pickup item below).
- **M2** — the approval inbox pages past the scan window until the
  display limit fills, so a burst of undecidable stagings can't starve
  older decidable rows (`TestApprovalListPagesPastUndecidableBurst`,
  220 hidden rows over one visible).
- **M3** — duplicate 409s omit `existing_id` when the dedupe pre-check
  hid the row (no more zero UUID on the wire).
- **M4/M5** — stale pre-triad comment residue removed from the arch
  tests; the "every 🟡 tool kind has a decision-grant mapping"
  obligation is now derived from the live registry
  (`TestEveryYellowToolHasADecisionGrantMapping`).

## Previous session: the triad restructure (ADR-0054/A69)

The whole tree was reworked to the spec's `backend/internal/{modules,
platform,shared}` triad in seven gate-green phases (each its own commit,
`make check` + `make test-integration` after each — no behavior change):

- Module path is `github.com/gradionhq/margince/backend`; everything Go
  moved under `backend/`; the contract is `backend/api/crm.yaml`.
- `crm-core` is dissolved: `modules/{people,deals,activities}` own the
  domain; store mechanics went to `platform/database/storekit`, the
  RBAC/row-scope clauses (incl. the activity link-walk) to
  `platform/auth` (joining `Admit`); `internal/compose` owns all wiring
  (HTTP surface, composite datasource provider, MCP registry) and the
  cross-module integration suites.
- `crm-auth`→`modules/identity`, `crm-approvals`→`modules/approvals`,
  `crm-agents`→`modules/agents`; the ai/search/capture doc-stubs are
  deleted (modules are added when they own real code).
- `cmd/crm` split into `cmd/{api,worker,migrate,mcp}` — a founder
  amendment to ADR-0054 §2 (separate role dirs over one binary), filed
  as feedback/01; the §9 cross-entity-tx question was feedback/02. Both
  are resolved in the spec (ADR-0054 amended 2026-07-04) and the
  feedback files retired to git history.
  Full record: decisions/0011.
- Enforcement rewritten to the triad DAG (depguard per-module sibling
  denies, go-arch-lint components, and `backend/arch_test.go` fitness
  tests that derive package lists from the tree).

All gates green at session close: `make check`, `make test-integration`
(13 suites — RLS, composite-FK, authz matrix, merge, promote, approval
loop, MCP e2e, passport lifecycle, bus lane, HTTP e2e), plus binary
smoke (api healthz + 401, migrate idempotent, mcp/worker fail loudly).

## Previous session: red-team remediation + merge finished

The 2026-07-04 red-team
(the craftsmanship/architecture red-team, now fully addressed — the review file lives in git history)
found the top defects were authorization/data-integrity, not style. All of
them are now fixed, with regression tests, and the in-flight merge is
finished. Recorded in decisions/0009
(merge survivorship) and decisions/0010
(C1–C5):

- **C1** — passport bearer tokens are read-only on REST; agent mutations go
  through the governed MCP tools (one choke point). New sentinel
  `ErrAgentSurfaceRestricted`. Spec reconciliation filed as `../fable feedback/18`.
  *(Superseded: ADR-0055 withdrew the stopgap — agent REST writes are now
  admitted and gated, decisions/0012.)*
- **C2** — read/full seat ceiling now on `crmctx.Principal` (human + agent),
  enforced before RBAC in the REST middleware and `gate.Admit`; unset fails closed.
- **C3** — the approval inbox (`List`/`Get`) filters by the same grant the
  decision needs, so it no longer leaks `proposed_change` workspace-wide.
- **C4** — every tenant-local FK rebuilt composite `(workspace_id, col) ->
  ref(workspace_id, id)` (migration 0019), pinned by the new
  `TestFK_tenantLocalReferencesAreComposite` fitness function.
- **C5** — workspace bootstrap is atomic: the core-defaults seed runs inside
  the bootstrap transaction, so a seed failure rolls the whole tenant back.
- **H1 (merge)** — the §1.3 two-record merge is complete end to end: store
  layer (`merge.go`) → REST handlers → `sor.Merge` verb + provider → the 🟡
  `merge_records` tool → integration tests (`merge_integration_test.go` +
  the MCP loop) → decisions/0009. The two ratifiable judgement calls
  (restrictive consent, both-have-partner survivorship) are flagged in 0009.
- **M1/M2/M5 + comment drift** — quota language corrected to match
  enforcement, the "InputSchema is documentation, validate in typed decode"
  reality is noted at the seam, Go 1.26 floor documented. M3's mechanical
  targets (cursor codec, visibility helper) were already shared; a generic
  CRUD engine is deliberately avoided (per the review's own caution). M4's
  core (same-workspace FKs) is C4.

All gates green at session close: `make check`, and the integration lane
(`make db-up` then `make test-integration`).

## Milestones completed (in build order)

WP0 repo foundation → WP1 core spine (schema, contract pipeline, auth,
core CRUD) → EP04 event bus → EP03 RBAC remainder → lead→person
promotion → EP06 WP4 MCP surface (passports, gate, tool registry, stdio
server — decisions/0007) → EP07 approval engine (stage 🟡 → human inbox
→ bound redemption — decisions/0008) → the §1.3 two-record merge
(decisions/0009) → red-team authorization & tenancy hardening C1–C5
(decisions/0010) → embedded SPA throughout. Details in
[README.md §What works today](README.md#what-works-today).
