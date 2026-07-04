# Status — where this stands and where to pick up

> The session-pickup record for this implementation. Whoever works here
> next (human or agent): read this first, then
> [AGENTS.md](AGENTS.md) for the binding rules. Update this file at the
> end of every working session.

**Last updated: 2026-07-04.** Roughly **17–18 %** of the 701-leaf-ticket
V1 backlog (`../margince/margince specs/spec/product/build-backlog/`) is
implemented and gate-verified.

## Last session: red-team remediation + merge finished

The 2026-07-04 red-team
([REVIEW-craftsmanship-architecture-redteam-2026-07-04.md](REVIEW-craftsmanship-architecture-redteam-2026-07-04.md))
found the top defects were authorization/data-integrity, not style. All of
them are now fixed, with regression tests, and the in-flight merge is
finished. Recorded in [decisions/0009](decisions/0009-two-record-merge-survivorship.md)
(merge survivorship) and [decisions/0010](decisions/0010-red-team-authorization-hardening.md)
(C1–C5):

- **C1** — passport bearer tokens are read-only on REST; agent mutations go
  through the governed MCP tools (one choke point). New sentinel
  `ErrAgentSurfaceRestricted`. Spec reconciliation filed as `../fable feedback/18`.
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

No half-finished slice is in flight. The next backlog blocks, roughly in
priority order:

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

## Milestones completed (in build order)

WP0 repo foundation → WP1 core spine (schema, contract pipeline, auth,
core CRUD) → EP04 event bus → EP03 RBAC remainder → lead→person
promotion → EP06 WP4 MCP surface (passports, gate, tool registry, stdio
server — decisions/0007) → EP07 approval engine (stage 🟡 → human inbox
→ bound redemption — decisions/0008) → the §1.3 two-record merge
(decisions/0009) → red-team authorization & tenancy hardening C1–C5
(decisions/0010) → embedded SPA throughout. Details in
[README.md §What works today](README.md#what-works-today).
