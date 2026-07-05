# Status — where this stands and where to pick up

> The session-pickup record for this implementation. Whoever works here
> next (human or agent): read this first, then
> [AGENTS.md](AGENTS.md) for the binding rules. Update this file at the
> end of every working session.

**Last updated: 2026-07-05 (overnight build, closed out).** Roughly **30 %**
of the 687-leaf-ticket V1 backlog
(`../margince/specs/spec/product/build-backlog/`) is implemented and
gate-verified; every `crm.yaml` operation is implemented.

## Last session: the overnight autonomous build (2026-07-04 → 05) — read this first

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

**Open:**
- **B-EP09.15 automations editor — BLOCKED**: no automations CRUD in the
  contract (feedback/14, with the public-booking consent gap).
- Spec-side follow-ups filed: feedback/13 (audit read + passport list for
  Settings), feedback/15 (Ledger-Green greys fail AA — a derived
  --textMeta shade carries small text meanwhile; canon stays pinned).
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

- **Lead-score behavioral recompute** — wire `ScoreLead` signals when the
  engagement substrate (activity→lead linkage / engagement_event) lands;
  fit-only today.
- **Coldstart ACCEPT executor** — approving a coldstart proposal marks it
  accepted + emits the event; actually WRITING the accepted fields onto an
  organization is the follow-on effect path.
- **EP06.23a golden datasets** (`evals/<task>/`, ≥100 cases) + the CI hard
  gates over them; the §5.2 pipeline (EP06.25) is in.
- **EP05 scrape/enrichment** (`scrapeCompany` evidence-or-omit) — reuse the
  coldstart fetcher + stripper.
- **S12b vLLM adapter**; **PERF-7 harness**.

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
