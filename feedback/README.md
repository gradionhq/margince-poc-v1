# Spec feedback — flaws found while building

When implementing against the spec (`../margince/specs/`) you hit
gaps, contradictions, or defects in the spec itself (not bugs in this code).
Contract-first means the spec is authoritative — so a spec defect gets
**filed here**, not silently worked around in the source.

**How to file:** add a numbered Markdown file (`NN-short-slug.md`) and a row in
the table below. Keep the source clean — no ticket numbers or fix narration in
code comments (AGENTS.md rule 4); the record lives here and in git.

Each file: what the spec says, why it's wrong/ambiguous/incomplete, the
affected spec path(s), what this repo did in the meantime, and the proposed
spec change.

| # | Title | Spec area | Status |
|---|-------|-----------|--------|
| [04](04-crmyaml-login-duplicate-security-key.md) | `login` op defines `security` twice (invalid YAML) | contract/crm.yaml | open |
| [05](05-archive-disqualify-xmcptool-verb-mismatch.md) | archive/disqualify `x-mcp-tool` verbs contradict interfaces.md §2 | contract/crm.yaml + interfaces.md | open |
| [06](06-license-readme-says-4yr-decision-is-2yr.md) | Spec README says Change Date is 4yr; ratified decision (A37) is 2yr | README + business/12-license.md | open |
| [07](07-list-tag-events-missing-from-catalog.md) | Contract ships list/tag CRUD but the closed event catalog has no list.*/tag.* types | contract/events.md §5 | open |
| [08](08-rbac-objects-for-lists-tags-consent-config.md) | features/04 §1 matrix lacks objects for lists, tags, and consent config | features/04 §1 | open |
| [09](09-meeting-host-has-no-column.md) | scheduling contract names a meeting host the activity table cannot store | crm.yaml /availability vs data-model.md §activity | open |
| [10](10-no-gwui-reuse-founder-decision.md) | Founder decision: no gw-ui/Dispact reuse — amend ADR-0001, re-scope B-EP09.2, revise §3 reuse map | ADR-0001 + design §3 + EP09 | open |
| [11](11-doi-token-has-no-issuance-surface.md) | DOI token is redeemed by `recordConsent` but nothing in the contract mints or delivers it | contract/crm.yaml + data-model §3.4 | open |
| [12](12-lists-tags-catalogs-unpaginated.md) | `listLists`/`listTags` return a page envelope but define no `limit`/`cursor` | contract/crm.yaml | open |
| [13](13-settings-needs-audit-read-and-passport-list.md) | Settings needs GET /audit-log + GET /passports; AuditLogEntry is defined but unreachable | contract/crm.yaml vs EP09.13b | open |
| [14](14-automations-crud-and-public-booking-missing.md) | No automations CRUD ops (blocks EP09.15); booking lacks public access + consent passthrough | contract/crm.yaml vs EP09.14/15 | open |

Statuses: `open` (filed, awaiting spec change) · `accepted` (spec owner agreed)
· `resolved` (spec updated) · `wontfix` (intentional, code adjusted).

**Resolved entries are deleted, not kept** (founder practice — session
artifacts retire to git history). The durable record of an accepted change is
the spec's own amendment note (ADR status line / DECISIONS entry), not this
folder. Retired so far: 01–03 (2026-07-04 — ADR-0054 §2 cmd shape, §9
single-tx exception, `events.md §5.3b` pipeline/stage events).
