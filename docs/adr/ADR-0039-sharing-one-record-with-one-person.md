# ADR-0039 — One record can be shared with one person or team by an explicit, audited grant

**Status:** Active
**Decided:** 2026-06-24

## The decision

A single `record_grant` table serves every shareable record type rather than
one grant table per type. A row names the record, the subject (a user or a
team), the access level (read or write), who granted it, an optional reason,
and an optional expiry. A write grant satisfies a read check; an expired grant
matches nothing.

The grant widens the visibility predicate that the application builds: a caller
sees a record when their own row scope covers it, or when an active grant
matches. Granting is human-only — a person holding the sharing permission
grants directly, and no agent path exists. A granter can never share wider than
they themselves hold. Every grant and every revoke writes an audit row, so "who
can see this record, and under whose authority" is answered by reading the
ledger.

## Why

Row scope alone offers three settings — own, team, all — and none of them
shares exactly one deal with exactly one colleague. Sales teams need that
constantly: a deal-desk review, an exec escalation, bringing in a solution
consultant. Doing it by widening somebody's scope grants far more than intended
and leaves no record of why.

Sharing is the one write that changes authority rather than data, which is why
it stays human-only. An agent may legitimately see a record and legitimately
hold write access on it, and still have no basis to decide which humans should
see it.

## What it binds in this repository

- `record_grant` is created in
  `backend/migrations/core/0011_lists_tags_attachments.up.sql`.
- The visibility predicate reads it in
  `backend/internal/platform/auth/rowscope.go`, which adds an `EXISTS (SELECT 1
  FROM record_grant rg …)` clause beside the base scope.
  `backend/internal/platform/auth/writescope.go` handles the write side.
- The grant operations live in `backend/internal/modules/identity/grants.go`
  and `handlers_grants.go`. `refuseWriteGrantToReadSeat` keeps a granter from
  handing out more than they hold; `mayRevoke` gates the revoke.
- The contract exposes one collection, `/record-grants` and
  `/record-grants/{id}`, in `backend/api/crm.yaml`. Create and revoke both carry
  `x-agent-access: human-only`, so an agent principal is rejected outright.
- The audit verbs are `record_share` and `record_unshare`, legal values in the
  `audit_log_action_check` constraint (current spelling in
  `backend/migrations/core/0287_audit_restriction_verbs.up.sql`).

## History

Adopted from the retired specification, decided 2026-06-24. Rewritten in plain
language 2026-08-19.

The original decision required the grant clause in two places: the application
query and a database row-level-security policy underneath it. That second layer
was never built, and row-level security was retired from the product entirely
by `backend/migrations/core/0217_retire_row_level_security.up.sql`. Tenant
isolation is now a per-statement predicate applied through
`database.WithWorkspaceTx`, held by `scripts/check-rls-store-path.sh`.
The cost of the single layer is stated rather than hidden: a grant has effect
only through `platform/auth`, so a fork's own SQL reaching the database
directly observes neither grants nor row scope.

A second amendment on 2026-08-07 removed the agent path. The original design
staged an agent-initiated grant behind an approval gate, which could never
work: the grant verbs refuse a non-human principal at the moment the approval
is redeemed, so an approved share was refused at the exact point it would have
applied.
