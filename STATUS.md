# Status — where this stands and where to pick up

> The session-pickup record for this implementation. Whoever works here
> next (human or agent): read this first, then
> [AGENTS.md](AGENTS.md) for the binding rules. Update this file at the
> end of every working session.

**Last updated: 2026-07-04 (ADR-0055 adoption).** Roughly **18 %** of the
701-leaf-ticket V1 backlog
(`../margince/specs/spec/product/build-backlog/`) is
implemented and gate-verified.

## Current session: the spec's red-team fixes landed in code (ADR-0055)

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

## Last session: the triad restructure (ADR-0054/A69)

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

## Pick up here: next big blocks

No half-finished slice is in flight. One small spec-driven follow-up first:

- **Emit `pipeline.*`/`stage.*` events** — feedback/01–03 were resolved in the
  spec on 2026-07-04 (files retired to git history); feedback/03 went
  option (a): the spec's `events.md §5.3b` now defines
  `pipeline.created/updated/archived` + `stage.created/updated/archived`,
  so pipeline/stage mutations must emit, and `createPipelineTx` comes off
  the `TestEveryAuditedMutationEmitsAnEvent` allow-list.

The next backlog blocks, roughly in priority order:

- **EP05 capture connectors** — inbound capture → lead/activity, the
  `connector` principal path (audit CHECK already carries it, fable feedback/13).
- **EP06 Surface-B half** — the model client + the governed proactive
  reasoning loop (the Overnight Agent), on top of the existing tool surface.
- **Search / context graph** — the `run_report`/schema-introspection SoR
  verbs, pgvector retrieval.
- **Consent/GDPR enforcement** — the suppression path that turns the
  now-relinking consent state into actual outbound gating.
- **Hosted A2 MCP server** — OAuth2 + PKCE + DCR + the JWS approval-token
  serialization (the single-binary redemption path becomes a signed token
  when issuer and verifier separate).

Housekeeping (no dependency, do when a session is otherwise idle):

- **Per-file SPDX headers** — the root `LICENSE` (BUSL-1.1) is in place and
  GitHub detects it, but source files carry no license marker. Add a two-line
  header to every hand-written `*.go` (before the `package` clause):
  `// SPDX-License-Identifier: BUSL-1.1` + `// SPDX-FileCopyrightText: 2026 Gradion`.
  Skip generated files (`*_gen.go` — the drift gate owns them) and vendored code.
  Best done as one mechanical sweep + a fitness test asserting the header is
  present, so it can't rot (matches the "prefer fitness functions" rule). Ties to
  12-license.md §5 "honest labeling" / §8 "don't strip notices".

## Milestones completed (in build order)

WP0 repo foundation → WP1 core spine (schema, contract pipeline, auth,
core CRUD) → EP04 event bus → EP03 RBAC remainder → lead→person
promotion → EP06 WP4 MCP surface (passports, gate, tool registry, stdio
server — decisions/0007) → EP07 approval engine (stage 🟡 → human inbox
→ bound redemption — decisions/0008) → the §1.3 two-record merge
(decisions/0009) → red-team authorization & tenancy hardening C1–C5
(decisions/0010) → embedded SPA throughout. Details in
[README.md §What works today](README.md#what-works-today).
