# ADR-0036 — An approval authorizes one exact action, against the row the approver saw

**Status:** Active
**Decided:** 2026-06-23

## The decision

When a human approves a staged agent action, the approval is bound to that action
and to the state of the target row when they saw it. The approval token is signed
and carries the approval id, the tool name and a hash of the staged change, so a
token minted for one action cannot authorize another. It can be spent once and it
expires. Before the action runs, the server re-reads the target row and compares
its `version` column against the version recorded when the change was staged; a
mismatch refuses the run with a version-skew error and the action must be
re-staged. Approving requires the same permissions the action itself would
require, so nobody approves their way past a grant they do not hold.

**The version check does not apply to every kind, and that is deliberate.** Some
approvals name no row: a rate proposal targets the workspace's shared price
sheet, and its effect is an effective-dated insert-or-replace whose outcome is
not knowable when the decision is made. There is no row for a pin to refer to,
so the version comparison is skipped and the binding rests on the token, the tool
and the change hash. What guards those kinds instead is the authority table
demanding **both** create and update on the sheet, so an approver who could
perform only one half cannot release an upsert that does the other.

## Why

An approval sits idle for up to 72 hours, and the world moves during that gap.
Without the version check, approving a stale proposal can advance a deal that
already closed, mail a contact who withdrew consent, or write to a record that
was
merged away — each with a clean audit trail and a wrong result. Without the
binding,
the token is a bare string, so a token minted to approve one action can be
replayed
against a different one.

## What it binds in this repository

- `backend/internal/modules/approvals/token_jws.go` mints the token. Its claims are
  the approval id as `jti`, the tool, `diff_hash`, and `exp`.
- `backend/internal/modules/approvals/redeem.go` spends it. `Service.RedeemInTx`
  enforces single use with a conditional update, `validateRedemption` checks the
  tool and diff hash, and `validateRedemptionTarget` re-reads the target and
returns
  `apperrors.ErrVersionSkew` when the version moved.
- `versionTables` in the same file lists the entity types whose table carries a
  `version` column that every write bumps — person, organization, deal, lead,
  activity, offer, product, list, tag, relationship, project, saved view, offer
  template, webhook subscription.
- `apperrors.ErrVersionSkew` in `backend/internal/shared/apperrors/apperrors.go` is
  the sentinel; the contract surfaces it as `409` with `code: version_skew`, and
  `backend/api/crm.yaml` accepts `If-Match` carrying the version the caller
read.
- `backend/internal/modules/approvals/authority.go` holds the approver's permissions
  to the grants the effect itself needs (`decisionGrants`), and narrows some
kinds to
  the one person they were staged for (`selfOnlyKinds`).
- `backend/internal/platform/database/storekit` bumps `version` in the guarded patch,
  which is what makes the pin trustworthy.

## History

Adopted from the retired specification, decided 2026-06-23. Rewritten in plain
language 2026-08-19. It amends
[ADR-0026](ADR-0026-every-tool-has-a-fixed-tier-and-a-floor-nobody-can-lower.md)
by
supplying the token and staleness mechanics that decision relied on. The source
says an approval against a target
with no version column strands in the inbox unexecuted. The code does not do
that:
a staging with a null pin skips the version check, so the binding rests on the
token, the tool and the diff hash alone.

That is deliberate for the kinds that use it rather than an oversight. A rate
proposal targets the workspace's price sheet, not a row on it — it stages with
the
workspace id as its target, and its effect is an effective-dated upsert whose
insert
or replace outcome is not knowable when the decision is made. There is no row
for a
pin to refer to. What guards those kinds instead is the authority table
demanding
**both** create and update on the sheet, so an approver who could perform only one
half cannot release an upsert that does the other.
