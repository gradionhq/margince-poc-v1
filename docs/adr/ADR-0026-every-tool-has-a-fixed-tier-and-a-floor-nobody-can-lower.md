# ADR-0026 — Every agent tool has a fixed tier, and the risky ones have a floor nobody can lower

**Status:** Active
**Decided:** 2026-06-17

## The decision

Each agent tool carries one of two tiers, and the server checks the tier before
the
tool runs. Auto-execute covers reads, reports, drafts that never send, and
reversible internal writes such as logging an activity or moving a deal between
open stages. Confirm-first covers anything that leaves the installation or
cannot
be cleanly undone: sending mail or messages, enriching from the internet,
archiving,
merging, disqualifying a lead, and closing a deal won or lost. The dividing line
is
reversibility, not read versus write. An operator may make an auto-execute tool
stricter, but may never lower a confirm-first tool below its floor, and the same
tiers apply to unattended background runs exactly as they do when a human is
watching.

## Why

Without a fixed per-tool tier, the safety story is a principle rather than a
list,
and a tool ships un-tiered by omission. The floor is the part a security-minded
buyer can rely on: sending, deleting, merging and closing cannot be switched to
run
unattended by anyone, including the account owner. It also doubles as defence
against prompt injection — an agent talked into sending mail still stops at the
gate, because the gate lives in the tool rather than in the prompt.

## What it binds in this repository

- `backend/internal/compose/agentpolicy_gen.go` is the generated tier table, derived
  from the `x-mcp-tool` annotations in `backend/api/crm.yaml` by
  `backend/tools/gen-agentpolicy`. It is generated, never hand-edited.
- The tools sitting at confirm-first there today are `send_email`,
  `send_account_email`, `send_message`, `enrich`, `archive_record`,
`merge_records`,
  `disqualify_lead`, `promote_lead`, `book_meeting`, `advance_project_phase`,
  `create_record` and `update_record` — the last two for the record types the
  contract tightens.
- `backend/internal/compose/agenttierfloor.go` applies the floor to a tool call.
  A REST call names its operation and resolves directly; a tool call names a
verb
  that serves several record types, so `contractTierFloors` re-keys the same
table
  by verb and record type. It is derived from the generated policy table, so an
  annotation added to the contract binds the tool door by regeneration.
- `backend/internal/modules/agents/registry.go` (`Registry.tightened`) is where a
  tool call has its tier tightened before it runs.
- `backend/internal/compose/agentegress_test.go`
  (`TestUrlTakingOperationsAreNeverAutoExecuteForAgents`) holds the outbound
half of
  the floor; `TestAnUnregisteredVerbIsRefused` holds the fail-closed half.
- `backend/internal/modules/agents/tierfloor_test.go` and
  `backend/internal/compose/agenttierfloor_integration_test.go` prove the floor
  reaches a real call.

## History

Adopted from the retired specification, decided 2026-06-17. Rewritten in plain
language 2026-08-19. Amended by
[ADR-0036](ADR-0036-an-approval-is-bound-to-one-action-and-one-row-version.md),
which specifies the approval token and the staleness check this tier model
relies on,
and by [ADR-0055](ADR-0055-agent-writes-are-governed-on-every-transport.md),
which
makes the gate apply on REST as well as on the tool surface.
