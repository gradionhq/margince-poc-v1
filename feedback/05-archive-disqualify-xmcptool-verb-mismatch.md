# 05 — crm.yaml archive/disqualify `x-mcp-tool` verbs contradict interfaces.md §2

**Status:** open

## What the spec says

Two spec documents disagree on which tool verb backs the archive-class
operations:

- `interfaces.md §2` (the AC-MCP-3 family list and the tool table):
  `archive_record` → `archivePerson` / `archiveOrganization` /
  `archiveDeal` / `archiveActivity` / `archiveRelationship` /
  `archiveList` / `archiveTag`; `disqualify_lead` → the DELETE on
  `/leads/{id}`.
- `crm.yaml` annotates `archivePerson`, `archiveOrganization`,
  `archiveDeal` with `verb: update_record, tier: yellow`, and
  `disqualifyLead` with `verb: update_record, tier: yellow` — while
  `archiveActivity`/`archiveRelationship`/`archiveList`/`archiveTag` DO
  say `verb: archive_record`.

## Why it's a defect

interfaces.md §2 declares the crm.yaml annotation set "the authoritative
op→tier map" that the ADR-0055 gate reads. With the annotation naming
`update_record`, a 🟡 archive staged over REST lands in the approval
inbox under the `update_record` kind — which has no decision-grant
mapping (it is a 🟢 verb that never stages), so the staging is
undecidable and the gate must refuse the operation outright. The
same-family asymmetry (person/org/deal vs activity/list/tag) is exactly
the "map built from annotations goes wrong" hazard the red-team review
flagged.

## What this repo did

`backend/api/crm.yaml` aligns the four annotations with interfaces.md §2
(`archive_record` on the three archives, `disqualify_lead` on
`disqualifyLead`); the tiers stay 🟡. Pinned by
`TestContractTierNeverBelowRegistryTier` and the e2e staged-archive loop.

## Proposed spec change

In `spec/contract/crm.yaml`, change the `x-mcp-tool.verb` of
`archivePerson`/`archiveOrganization`/`archiveDeal` to `archive_record`
and of `disqualifyLead` to `disqualify_lead`, matching interfaces.md §2.
