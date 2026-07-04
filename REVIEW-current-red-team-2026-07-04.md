# Current Red-Team Review: Margince PoC

**Date:** 2026-07-04  
**Scope:** current `/Users/lars/develop/margince-poc-v1` tree after the triad restructure and prior red-team remediation.  
**Stance:** adversarial engineering review across security, data integrity, architecture, test strategy, clean code, and operational craftsmanship.

## Executive Verdict

This codebase is in much better shape than a normal PoC. The previous critical findings were not merely patched; several became architecture rules and fitness tests. The current tree is build-green, integration-green, and has a serious modular-monolith shape: `shared -> platform -> modules -> compose -> cmd`, RLS is forced and tested, the contract surface is explicit, passport REST mutation is blocked, read-seat ceilings are enforced, and composite same-workspace FKs are now database-backed.

The remaining risks are subtler and therefore more dangerous: several paths enforce the *primary* row scope but not the row scope of referenced target IDs. In this product, a foreign-key argument is often also a read of the target row. Activity links understand that; deal/org fields do not. The approval engine has the same class of bug: decision visibility checks object permissions but not target row-scope visibility. These are not "style" issues; they are authorization-boundary holes that current green gates do not catch.

Short version: **the repo is strong, but its next red-team frontier is target-row authorization for secondary IDs and fitness tests that prove every mutation still emits the promised audit/outbox pair.**

## Verification Run

I ran:

- `make check` from `backend/` — passed.
- `make db-up` from `backend/` — passed, reusing local `fable-pg16` and `fable-redis`.
- `make test-integration` from `backend/` — passed.

Both Go test commands needed normal access to `/Users/lars/Library/Caches/go-build`; the first sandboxed attempts failed with `operation not permitted`, then passed with that permission.

## What Is Genuinely Strong

- The architecture triad is mechanically enforced by depguard, go-arch-lint, and `backend/arch_test.go`.
- RLS coverage and composite tenant-local FK coverage are fitness tests derived from the live schema, not hand lists.
- REST passport mutation restriction and read-seat mutation restriction are now real middleware gates in `identity.Handlers.Middleware`.
- Store entry points consistently start with object RBAC and, for the primary row, row-scope checks.
- The MCP approval loop has a good authority shape: content hash, passport binding, target version, single-use redemption.
- Error mapping is mostly clean: sentinels centralize stable problem codes; CHECK violations map to 422 instead of opaque 500s.
- Generated contract and stub files are isolated and drift-checked.

## Findings

### H1. Secondary FK targets are not consistently row-scope gated

**Severity:** High  
**Area:** Authorization, data integrity, row-scope semantics

Activity logging correctly treats link targets as reads before writing the FK: `LogActivity` calls `auth.EnsureLinkTarget` for each linked person/org/deal (`backend/internal/modules/activities/activity.go:110-114`). The same principle is missing in several other mutating paths:

- `CreateDeal` accepts `OrganizationID`, `OwnerID`, and later `PartnerOrgID` without checking target row visibility beyond same-workspace FK enforcement (`backend/internal/modules/deals/deal.go:65-78`, `245-253`).
- `UpdateDeal` can attach or reattach `organization_id` / `partner_org_id` to rows the caller may not be able to read (`backend/internal/modules/deals/deal.go:245-253`).
- `CreateOrganization` and `UpdateOrganization` accept `parent_org_id` without proving the parent is visible to the caller (`backend/internal/modules/people/organization.go:83-89`, `256-258`).

Impact: a user with create/update rights and a guessed or leaked UUID can persist relationships to out-of-row-scope records. RLS and composite FKs stop cross-tenant corruption, but they do not prove the actor could read the referenced row. This breaks the repo's own rule that "anything that returns a record is a read" and extends it: anything that embeds another record's ID into a returned record is also a read of that target.

Recommendation: add a platform helper for "visible FK target" and require it for every tenant-local FK argument that points at row-scoped business records. Then add a fitness-style integration test that enumerates store inputs or schema FKs and proves out-of-scope target IDs are refused as `ErrNotFound`.

### H2. Approval inbox/approval authority ignores row scope

**Severity:** High  
**Area:** Approvals, RBAC, information disclosure

`approvals.canDecide` delegates to `requireDecisionGrants`, but that function checks only object-level permissions such as `deal.update`; it does not check whether the human can see the target row under own/team/all scope (`backend/internal/modules/approvals/service.go:369-408`). `List` and `Get` use that same predicate as their visibility filter (`backend/internal/modules/approvals/service.go:138-184`, `187-206`).

Impact: a rep/manager with `deal.update` and team scope can see and approve a staged approval for another team's deal if the object grant exists. The final tool handle will still hit store RBAC during redemption, but the approval row itself leaks `proposed_change`, target ID, and summary, and the human may incorrectly approve authority they do not hold over the target row.

Recommendation: make the approval service check target row visibility for `target_entity_type`/`target_entity_id` using the same row-scope helper the underlying store would use. Add tests for "object grant yes, row-scope no" on list, get, approve, and reject.

### H3. Rejecting an approval by known ID requires no decision grant

**Severity:** High/Medium  
**Area:** Approvals, authority control

`Decide` requires `requireDecisionGrants` only when `approve == true` (`backend/internal/modules/approvals/service.go:247-254`). A reject path only requires `humanOnly`, then updates the row to rejected (`backend/internal/modules/approvals/service.go:256-277`).

Impact: any human in the workspace who learns an approval UUID can reject it, even if `List`/`Get` would hide it and even if they could not perform or approve the underlying action. UUID guessing is unlikely, but the staged agent response includes the approval id, and operational side channels are realistic.

Recommendation: require the same visibility/decision predicate for both approval and rejection. If product wants broad rejection, model it explicitly as a separate permission, not as an accidental bypass.

### M1. Pipeline creation violates the stated audit+outbox write shape

**Severity:** Medium  
**Area:** Event architecture, invariant drift

The repo-level invariant says every mutation commits domain row + `audit_log` row + `event_outbox` row in one transaction. `createPipelineTx` writes `pipeline` and `stage` rows and audits, but deliberately emits no outbox event (`backend/internal/modules/deals/pipeline.go:52-79`).

The local comment says pipeline config has no V1 catalog event (`backend/internal/modules/deals/pipeline.go:74-75`), which may be a valid product choice, but it contradicts `storekit`'s package-level invariant that the write shape includes an event row (`backend/internal/platform/database/storekit/storekit.go:1-6`) and the root `AGENTS.md` rule.

Recommendation: either add `pipeline.created` / `pipeline.updated` event catalog entries or explicitly narrow the invariant to "domain-evented mutations" and document pipeline config as audit-only. Prefer adding a small fitness test that flags `storekit.Audit` call sites without paired `storekit.Emit`, with an allow-list only if the spec ratifies audit-only writes.

### M2. Approval list can starve authorized rows behind the hard pre-filter cap

**Severity:** Medium  
**Area:** Correctness, UX, operations

`List` fetches the latest 200 approval rows, filters them in memory by `canDecide`, then truncates to the requested display limit (`backend/internal/modules/approvals/service.go:134-184`). If the latest 200 pending approvals are not decidable by the caller, older decidable approvals vanish from their inbox.

Impact: busy workspaces can produce false-empty approval queues for managers or reps, especially once automated agents stage many actions.

Recommendation: push enough filtering into SQL to paginate correctly, or loop over pages until enough visible rows are found or no rows remain. Include a regression test with >200 hidden rows followed by visible rows.

### M3. Duplicate conflict responses include the zero UUID when the real ID is hidden or unknown

**Severity:** Medium/Low  
**Area:** API correctness, craftsmanship

`httperr.Duplicate` omits `existing_id` only when the passed string is empty (`backend/internal/platform/httperr/httperr.go:117-129`). The people handlers pass `dup.ExistingID.String()` even when `ExistingID` is the zero value (`backend/internal/modules/people/handlers.go:35-48`). The duplicate pre-checks intentionally leave `ExistingID` zero when the row is not visible or a race aborted the transaction.

Impact: hidden/race duplicate responses carry `"existing_id": "00000000-0000-0000-0000-000000000000"` instead of omitting the field. This does not disclose the real row id, but it violates the wire contract and trains clients to special-case a fake ID.

Recommendation: pass an empty string for zero UUIDs, or make `httperr.Duplicate` accept `ids.UUID` and omit zero internally.

### M4. Architecture comments still contain stale pre-triad residue

**Severity:** Low  
**Area:** Craftsmanship, maintainability

`backend/arch_test.go` still mentions old `crm-*` paths as part of "current tree and post-split modules tree" context (`backend/arch_test.go:70-80`). The code is harmless, but this repo treats comments as architecture documentation and explicitly bans build-process residue.

Recommendation: refresh these comments now while the restructure is fresh. The test should describe the invariant, not the migration history.

### M5. Enforcement is strong where tests derive obligations, weak where obligations are social

**Severity:** Medium  
**Area:** Test strategy, architecture durability

The best gates derive obligations from the system: every `workspace_id` table must have RLS; every tenant-local FK must be composite. Comparable obligations still rely on reviewer memory:

- Every mutation with `storekit.Audit` should have an outbox decision.
- Every row-scoped FK argument should prove target visibility.
- Every approval kind should have both object grant and row-scope checks.
- Every generated operation that becomes implemented should be covered by at least one HTTP or store-level security test.

Recommendation: add small meta-tests for the first three. They do not need to be perfect static analyzers; even conservative checks with explicit exceptions would catch the classes above.

## Architecture Assessment

The triad layout is the right direction for this codebase. `internal/compose` owning cross-module wiring is a strong choice, and avoiding sibling module imports is especially helpful for agent-built work. The flat per-module files are still readable at this size, but `people` is already doing person, organization, lead, promote, merge, and shared mapping. The next growth step should not be a generic CRUD engine; it should be modest subpackage structure or file ownership conventions once modules grow past the current shape.

The main architectural smell is not layering. It is "primary row checked, secondary rows trusted." The architecture needs a named concept for target visibility so reviewers and future contributors do not have to rediscover it at every FK field.

## Clean-Code / Craftsmanship Assessment

The Go is generally clean: explicit errors, small helpers where they pay for themselves, no broad `any` use except at JSON/seam boundaries, and comments usually explain invariants rather than syntax. The places to tighten are:

- Remove review-ticket/history residue from source comments.
- Do not let comments soften known invariant exceptions, as with pipeline audit-only writes.
- Turn repeated `pageInfo`/`writeStoreErr`/`sprintf` helpers into shared platform utilities only if they start causing actual drift; they are tolerable for now.
- Add tests before adding more agent tools; the approval surface is subtle enough that untested additions will almost certainly drift.

## Suggested Fix Order

1. Add target-visibility checks for deal/org FK fields and pin with integration tests.
2. Fix approval row-scope filtering and require decision grants for reject.
3. Decide whether pipeline writes emit events; make the invariant mechanical either way.
4. Fix zero UUID duplicate response details.
5. Refresh stale architecture comments.

## Final Read

This is a solid, thoughtfully built PoC. The current issues are not signs of messy code; they are exactly the kind of second-order authorization problems that appear once the first-order architecture is good enough to make them visible. That is good news, but the top two should be treated as security work before expanding capture, search, or Surface-B automation.
