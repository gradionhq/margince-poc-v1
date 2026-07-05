# 9. Two-record merge: non-lossy relink and the survivorship rules

Date: 2026-07-04
Status: accepted

## Context

features/01 §1.3 (data-model §3.2) specifies merging two duplicate people or organizations
A→B: everything that points at A relinks to B, A is archived carrying `merged_into_id = B` so
it stays fetchable, and the whole thing is one audited, reversible transaction. The spec names
the shape but leaves several concrete survivorship questions to the implementation — every
child table has unique indexes and check constraints the surviving record must still satisfy,
and "combine two records" has to resolve every collision those constraints can produce.

## Decision

**Relinking is collision-aware, not a blind `UPDATE ... SET fk = B`.** One transaction folds A
into B and returns B; a `person.merged` / `organization.merged` event carries the relink counts
so downstream consumers re-home their own references. The rules:

- **Fill-only survivorship.** A never overwrites a field B already has; A only fills B's gaps
  (`first_name`, `title`, `legal_name`, `industry`, social, the lead-conversion pointer). B is
  the survivor the caller chose, so B's data is authoritative.
- **Primary-slot demotion.** At most one primary email/phone per (record, type), one primary
  domain per org, one current-primary employer per person. When A and B both fill a slot, A's
  relinked row **demotes** (keeps the row, drops the primary flag) — no data lost, invariant held.
- **Duplicate edges/links archive or drop.** A relationship edge A already shares with B (same
  kind + same far end) **archives** rather than relinking (a duplicate edge is noise; the
  archived row keeps provenance). Pure link rows (activity_link, list_member, taggable) that B
  already holds **drop** A's copy — they carry no provenance of their own, so deletion loses
  nothing.
- **One-hop redirect chains.** Rows that were already merged into A repoint to B, so following
  `merged_into_id` is always exactly one hop and always lands on a live record.
- **Org hierarchy reparents; self-ancestor guarded.** A's children become B's; if B itself sat
  under A, B lifts to A's parent first so absorbing A's children cannot make B its own ancestor.

Two choices are **judgement calls, not spec-forced — flagged here for ratification:**

1. **Restrictive consent (RATIFIABLE).** A merge may only ever *reduce* what the workspace is
   allowed to do with the survivor, never expand it: A's *withdrawal* propagates to B, but A's
   *grant* never upgrades B. Expanding contact rights must come from a captured consent event,
   not a data-hygiene action. The append-only proof log (`consent_event`) relinks in full so the
   evidence trail survives on the one person. *Alternative considered: "most recent event wins" —
   rejected because a merge is not a consent capture and must not be able to re-enable outreach.*
2. **Both-have-partner survivorship (RATIFIABLE).** The 1:1 `partner` extension moves into B
   only when B has *no* partner row; when both A and B carry partner-program state, **B's stands
   and A's rides its archived org untouched** (recoverable, never silently blended). The survivor
   flips to `classification = 'partner'` (the A41 invariant) whenever it ends up with a partner
   row. *Alternative considered: field-level merge of the two partner rows — rejected: program
   state (tier, cert, health) is not safely averagable, and silent blending destroys the audit
   story.*

## Seam & tool wiring

- `sor.SystemOfRecordProvider` gains a `Merge(MergeInput)` verb (person/organization only; deals
  and leads leave through their own lifecycle). REST `mergePerson` / `mergeOrganization` are
  wire-only handlers over the store.
- The agent surface is the `merge_records` **🟡** tool: collapsing two records is destructive and
  hard to reverse, so an agent stages it for human confirmation. `StageInfo` **pins the
  survivor's version** — the human's yes is a judgment about merging into B *as it is now*, so a
  change to B before redemption invalidates the approval (version skew, re-stage).
- Approving a merge requires `<target_type>.update` — consistent with the store mapping the merge
  verb to `update` (rewriting where records point is curation, not deletion).

## Consequences

- Typed errors: `MergeSelfError` (422), `AlreadyMergedError` (409 + `merged_into_id` pointer, the
  AlreadyPromoted disclosure precedent), `MergedTargetError` (422, the survivor must be live).
- Covered by `crm-core/internal/store/merge_integration_test.go` (referential integrity, primary
  demotion, fill-only, restrictive consent, hierarchy reparenting, partner-vacancy move, all three
  error paths) and the `merge_records` leg of the MCP approval-loop test.
- If either ratifiable rule is overturned, only `mergeConsent` / the partner block in
  `merge.go` and their tests change; the relink/redirect machinery is independent.
