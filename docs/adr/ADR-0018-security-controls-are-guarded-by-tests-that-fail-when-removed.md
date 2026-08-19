# ADR-0018 — Every security control is guarded by a test that fails when the control is removed

**Status:** Active
**Decided:** 2026-06-04

## The decision

Each security control in this system has a test that proves the control's
behaviour, not just its presence. Deleting or weakening the control turns that
test red, so the change cannot merge. The controls covered this way are tenant
isolation through row-level security, the rule that an agent's permissions never
exceed those of the human who granted them, the single admission point every
mutation passes through, and the audit write that accompanies every mutation.
Reviewer assignment and source comments help a reader notice they are editing a
control, but the test is what enforces it.

## Why

Both a runtime agent and a build-time agent editing source can weaken this
architecture. A source edit that drops a row-level-security policy or widens a
permission scope looks like ordinary code and reviews as ordinary code. A test
that only checks the control exists passes against a control that has been
quietly
gutted. PostgreSQL makes this concrete: a table with `ENABLE ROW LEVEL SECURITY`
but not `FORCE` still lets the table owner read every row, so an enable-only
migration reads as secure and is not.

## What it binds in this repository

- `backend/migrations/migrationrole_integration_test.go` proves tenant tables carry
  `FORCE ROW LEVEL SECURITY` and that the migration role does not bypass it.
- `backend/rlsclaims_test.go` (`TestNoGoSourceClaimsRLSStillScopesARead`) catches a
  comment claiming row-level security still scopes a read where it no longer
does.
- `backend/internal/compose/captureofflinedemo_integration_test.go`
  (`TestTheDirectoryReadsThroughRLS`) runs the read through the real path
against a
  real database.
- `backend/tools/extmigrategate/` gates extension migrations the same way;
  `TestGateRejectsMissingForceRLS` and
  `TestTheRLSProbeRefusesAConnectionRowLevelSecurityDoesNotBind` are its two
halves.
- `backend/arch_test.go` derives its package lists from the tree, so a new package
  cannot slip past the module-boundary rules by not being on a list.
- `backend/tableownership_test.go` holds each module to the tables its `doc.go`
  declares, which is what keeps the single mutation seam single.
- `backend/internal/platform/auth/admit.go` is that seam: `Gate.Admit` is the one
  place a tool or agent call is admitted.

## History

Adopted from the retired specification, decided 2026-06-04. Rewritten in plain
language 2026-08-19. The source records a 2026-06-23 amendment bounding the
guarantee: it holds for builds that keep these tests green, and a fork that
deletes
them runs at its own risk.
