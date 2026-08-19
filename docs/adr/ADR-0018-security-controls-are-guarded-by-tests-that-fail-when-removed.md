# ADR-0018 — Every security control is guarded by a test that fails when the control is removed

**Status:** Active
**Decided:** 2026-06-04

## The decision

Each security control in this system has a test that proves the control's
behaviour, not just its presence. Deleting or weakening the control turns that
test red, so the change cannot merge. The controls covered this way are tenant
isolation, the rule that an agent's permissions never exceed those of the human
who granted them, the single admission point every mutation passes through, and
the audit write that accompanies every mutation. Reviewer assignment and source
comments help a reader notice they are editing a control, but the test is what
enforces it.

Tenant isolation is enforced two different ways, and the difference matters when
reading the tests below. **Core tables** carry the tenant predicate in each
statement, and the one thing binding the setting that predicate reads is
`database.WithWorkspaceTx`; row-level security was retired for them by migration
`0217`. **Extension-owned tables** still use row-level security, because a unit's
own database role owns its tables, and an owner reads past `ENABLE` unless
`FORCE` is also set.

## Why

Both a runtime agent and a build-time agent editing source can weaken this
architecture. A source edit that drops a tenant predicate or widens a permission
scope looks like ordinary code and reviews as ordinary code. A test that only
checks the control exists passes against a control that has been quietly gutted.

The extension case makes this concrete. A table with `ENABLE ROW LEVEL SECURITY`
but not `FORCE` still lets the table's owner read every row, and the owner is
the extension's own role — so an enable-only migration reads as secure and is
not. The gate refuses it by name rather than trusting the migration to be
right.

## What it binds in this repository

- `scripts/check-rls-store-path.sh` is what holds core tenant isolation. Every
  per-workspace statement must address the transaction rather than the bare pool,
  because the pool leaves the setting the tenant predicate reads unbound — and an
  unbound setting resolves against NULL, so the statement answers about nothing
  instead of failing.
- `backend/rlsclaims_test.go` (`TestNoGoSourceClaimsRLSStillScopesARead`) bans a
  Go comment claiming row-level security still scopes a core read. It bans the
  claim rather than the word, because the mechanism outlived its name in several
  places.
- `backend/tools/extmigrategate/` gates extension migrations, where row-level
  security is still the control. `catalog.go` refuses a table carrying `ENABLE`
  without `FORCE`, naming the object;
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
deletes them runs at its own risk.

The source described tenant isolation as row-level security throughout, which
was true when it was written and is no longer true of core tables — migration
`0217` (ADR-0091) retired every one of those policies. This record describes the
two mechanisms as they are, rather than carrying the old one forward.
