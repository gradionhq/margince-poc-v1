# 8. The 🟡 approval engine: the staged row is the authority object

Date: 2026-07-04
Status: accepted

## Context

EP07's approval inbox is what turns the gate's `ErrRequiresApproval` from a dead end into a
loop: agents stage confirm-first actions, humans decide them, agents redeem the decision.
ADR-0036 specifies a signed single-use `X-Approval-Token` (compact JWS) bound to
workspace/passport/tool/diff_hash, plus a target-version re-check at execute.

## Decision

**No bearer secret travels; the approval row itself is the authority object.** A refused 🟡
call is staged (`approval` table + `approval.requested`); after a human approves, the agent
re-invokes the **identical call** plus `approval_id`. Redemption then checks, server-side:

- status approved, within a 15-minute post-decision window, never consumed (single-use via a
  conditional UPDATE — racing redemptions cannot both pass);
- the call's **content hash** equals the staged `diff_hash` (args are canonicalized by
  deep-sorted re-marshaling, so "identical" is a property of content, not serialization);
- the redeeming **passport** is the staging passport;
- the **target row's version** is unchanged since staging — the human's yes was about the world
  they saw (ADR-0036); a moved target answers `version_skew`, re-stage.

This is the same guarantee set the JWS token encodes, enforced by lookup instead of signature —
correct for a single-binary PoC where issuer and verifier are one process. The compact-JWS
serialization becomes necessary (and gets added) when the hosted A2 surface separates them.
`Approval.approval_token` stays null until then.

Supporting choices:

- **Deciding is human work**: agents cannot list the inbox or decide (an agent approving its own
  staging collapses the tier model), and the approver must hold the RBAC the staged effect
  itself requires — you cannot green-light what you could not do.
- **`edited_payload` is refused** (422) rather than half-built: the edit path must re-enter the
  admission gate from scratch (ADR-0036) and that machinery isn't here yet.
- **Expiry is lazy**: a pending row past `expires_at` (24h) reads as expired everywhere; no
  sweeper process.
- The forbidden-by-design cases stay forbidden **before** staging: `promote_lead` with a
  non-engagement trigger never reaches the inbox — an approval must never be able to launder an
  action that no human could approve.
- `archive_record` and `promote_lead` join the registered surface now that their refusals land
  somewhere; audit gains the `approve`/`reject` verbs (migration 0018 — the data-model §11
  vocabulary predates the inbox, fable feedback/17).

## Consequences

- The full loop runs today: agent stages over MCP → the SPA inbox shows the one-line summary
  ("Close deal "Hopper renewal" as won") → approve/reject → the agent redeems by repeating the
  identical call with `approval_id`.
- Tampered redemptions (any argument changed), double redemptions, undecided/rejected/expired
  stagings, and version-skewed targets all answer typed errors with zero side effects — each
  pinned by the end-to-end test.
- When A2 lands, `Redeem` grows a JWS verification branch; the binding checks stay identical.
