# 10. Red-team authorization & tenancy hardening (C1–C5)

Date: 2026-07-04
Status: accepted — **C1 superseded** by the spec's ADR-0055 (founder decision):
agent REST writes are admitted and gated, not blocked; `ErrAgentSurfaceRestricted`
is withdrawn. See [0012](0012-adr0055-transport-agnostic-gate.md). C2–C5 stand.

## Context

The 2026-07-04 craftsmanship/architecture red-team
(the 2026-07-04 craftsmanship/architecture red-team; retired to git history) found that several load-bearing
claims held on one surface but not uniformly across all of them: the MCP gate was real but the
REST bearer path escaped it, the seat ceiling was modelled but never enforced, the approval
inbox protected the decision but not the read, and RLS bounded row visibility but not
same-workspace FK integrity. The finding was that the top defects were **authorization and
data-integrity**, not style — the invariants needed to be impossible to bypass, not merely
documented. This ADR records the five fixes.

## Decision

**C1 — one agent-mutation choke point across transports.** Agent passports are **read-only on
REST**. A mutating REST call from a passport is refused with `ErrAgentSurfaceRestricted` (403
`agent_surface_restricted`) *whatever the passport's scope*; agent mutations must flow through
the governed MCP tool surface (`internal/gate`), where scope ∧ tier ∧ the 🟡 approval gate all
apply. This makes the MCP registry+gate the single place an agent mutation can happen.
*Reconciliation: ADR-0013's "agents get the same REST surface as everyone else" predates the
gate; the narrowing to read-only is recorded upstream as `../fable feedback/18`.*
*Alternative considered: a REST admission layer mapping operationId+body to the same
ToolSpec/tier/staging flow — deferred; it duplicates the tier table by operationId and is not
warranted while MCP already covers every agent mutation.*

**C2 — the seat ceiling is enforced before RBAC.** `crmctx.Principal` now carries `SeatType`,
bound for both humans (from the session) and agents (the granting human's seat — "agent ≤
human", A62/ADR-0047). A read seat, or an agent acting for one, may read but never
mutate/send/approve: refused with `ErrSeatTierInsufficient` (403). Enforced at the two choke
points — the REST middleware (method-based) and `gate.Admit` (a non-read `RequiredScope`). An
**unset** seat fails closed (treated as read-only): a loader that forgot the seat must not
enable mutation by omission. A seat refusal is never staged for approval — no approval lifts a
licensing ceiling.

**C3 — the approval inbox shows only what you could decide.** `List`/`Get` previously gated on
`humanOnly`, leaking `proposed_change`, target ids, and diffs to any human in the workspace.
They now filter by `canDecide` — the *same* grant check `Decide` enforces — so triage visibility
and the decision gate can never drift apart. An approval you could not decide reads as absent
(404 on `Get`, omitted from `List`), the row-scope existence-hiding convention. `List` fetches up
to a hard cap, filters, then truncates to the display limit.

**C4 — composite same-workspace foreign keys.** Every tenant-local FK (a FK from one
`workspace_id` table to another) is rebuilt as `(workspace_id, <col>) REFERENCES
<ref>(workspace_id, id)`, so the database rejects a cross-workspace target by construction — RLS
bounds *visibility*, not referential *integrity*. Migration `0019` converts all 53 such FKs
(SET NULL FKs use PG15+ column-list `SET NULL (<col>)` so the NOT NULL `workspace_id` is never
nulled). The invariant is pinned by the `TestFK_tenantLocalReferencesAreComposite` fitness
function, so a future FK that omits `workspace_id` fails the build.

**C5 — atomic bootstrap.** Workspace creation seeded core defaults (the default pipeline) in a
*second* transaction after the identity transaction committed, so a seed failure left a tenant
with no pipeline. The seed now runs **inside** the bootstrap transaction via a tx-scoped
callback composed at the edge (crm-auth never imports crm-core); a seed failure rolls the whole
tenant back, and the slug is free for a clean retry.

## Consequences

- New sentinel `ErrAgentSurfaceRestricted` (403 `agent_surface_restricted`), registered in the
  fable-poc error registry ahead of the spec (`../fable feedback/18`), mapped in `httperr`.
- New fitness function `TestFK_tenantLocalReferencesAreComposite` joins
  `TestRLS_coversEveryTenantTable` as a schema invariant guard.
- Integration coverage: `TestEndToEnd_passportCannotMutateOverREST` (C1),
  `TestEndToEnd_readSeatCannotMutate` + `gate` unit tests (C2), the low-privilege inbox
  assertion in the MCP test (C3), the migrations fitness lane (C4),
  `TestBootstrapSeedFailureRollsBackTenant` (C5).
- The mediums (M1–M5) and comment drift the review noted are addressed alongside; the "strong
  comments getting ahead of enforcement" smell is closed by making the enforcement match the
  comments rather than softening the comments.
