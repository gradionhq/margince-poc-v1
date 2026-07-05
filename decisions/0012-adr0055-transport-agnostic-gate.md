# 0012 — ADR-0055 in code: the transport-agnostic, fail-closed agent gate

**Date:** 2026-07-04. **Status:** adopted.
**Spec basis:** ADR-0055 (A70), interfaces.md §2 (the fail-closed +
human-only rules and the `shared/ports/authz` seam), §3 (the frozen SoR
v1 set), data-model §1.3a (the If-Match↔version reconciliation). This
session implements the spec-side red-team remediation in the source.

## What changed

1. **`ErrAgentSurfaceRestricted` is withdrawn** (it was decisions/0010
   C1, the read-only-REST stopgap; ADR-0055 supersedes it). Agents keep
   REST write access, governed identically to MCP: the identity
   middleware authenticates the passport and binds the principal; the
   contract router's agent gate (`internal/compose/agentgate.go`) then
   admits, stages, or refuses every mutating call.

2. **One tier table, generated from the contract.**
   `tools/gen-agentpolicy` derives `internal/compose/agentpolicy_gen.go`
   from `backend/api/crm.yaml`: every mutating operation's route maps to
   its `x-mcp-tool` verb + tier or its `x-agent-access` class. The
   generator IS the drift-lint — a mutating op with neither annotation
   fails `make gen`, so the gate's default-deny (an unmapped route →
   `403 permission_denied`) can only ever bite an out-of-date binary,
   never a fresh build.

3. **🟡 over REST stages the same approval.** A yellow-tier agent
   mutation stages an approval (kind = tool verb, content-hashed over
   operation + concrete path + canonicalized body) and answers
   `403 approval_required`. After a human decides, the agent repeats the
   identical request with `X-Approval-Token: <approval-id>`; redemption
   re-checks kind, diff hash, passport, and target version, single-use —
   the ADR-0036 mechanics unchanged. The contract annotation may only
   TIGHTEN a verb's tier (archive-by-DELETE stays 🟡 over the 🟢
   `update_record` machinery); a yellow kind with no decision-grant
   mapping is refused instead of staging an undecidable row.

4. **Human-only is enforced at both layers.** approve/reject, consent,
   DSR, pipeline/stage config, and passport issue/revoke carry
   `x-agent-access: human-only` + `security: cookieAuth` in the
   contract, are rejected for agent principals by the gate
   (`TestGovernanceOperationsAreHumanOnly` pins the set), and the
   approvals service keeps its own `humanOnly` check as depth — the
   self-approval bypass is closed at three layers.

5. **The `shared/ports/authz` seam.** `gate.Admit` re-derives the
   granting human's seat + RBAC live at every admission through
   `authz.Resolver` (identity implements it; compose injects it), so a
   seat downgrade or role revocation binds mid-session and the
   platform→modules DAG stays clean. The refreshed authority replaces
   the principal's stamped copy for everything downstream. No resolver,
   no granting human, or a vanished user ⇒ fail closed.

6. **The SoR v1 seam is frozen.** `StageSemantic` and `PromoteLead`
   moved from compose-level extensions into `SystemOfRecordProvider`
   (they are part of the spec's frozen v1 set);
   `TestSystemOfRecordProviderV1MethodSetIsFrozen` is the
   Go-interface-diff gate. Post-v1 verbs go on a `...V2` interface
   behind a capability probe.

7. **Contract sync.** `backend/api/crm.yaml` now mirrors the spec's
   post-remediation contract (If-Match set reconciled with the version
   columns; partner update versioned, pipeline/stage deliberately not;
   `captured_by` read-only and server-stamped; sentinel wire responses
   for scope/seat denials; DDL-aligned enums; `lawful_basis` naming; the
   wire code for a staged 🟡 is `approval_required`) — with ONE
   deliberate divergence: the OAuth2 Authorization-Server surface
   (`/.well-known/...`, `/oauth/*`) replaces `/passports` in the spec,
   but issuing passports over `POST /passports` is the working local/A1
   path and the OAuth2+PKCE+DCR server is the separately scheduled
   hosted-A2 block (STATUS "next big blocks"). The backend contract
   keeps the A1 paths, marked human-only, until that block lands.

## Judgement calls to ratify

- **X-Approval-Token carries the approval id** (the single-binary
  redemption path, decisions/0008); it becomes the signed JWS when A2
  separates issuer and verifier.
- **Annotation-vs-ToolSpec disagreement resolves upward** (tighten-only)
  and an unregistered verb admits at its annotation tier under the write
  scope — both directions can only raise friction, never lower a floor.
- **Archive/disqualify verb mismatch** between crm.yaml and
  interfaces.md §2 is resolved toward interfaces.md; filed as
  feedback/05.
