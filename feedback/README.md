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
> **All 04–15 RESOLVED in the spec on 2026-07-05** (foundation commit `667c355`). The durable record is
> each spec amendment (right column). Files kept only until the build re-points its interim workarounds —
> esp. **07**, whose `auditOnlyWrites` waivers must cite `events.md §5.3c` (not this file) before 07 is deleted.

| # | Title | Spec area | Status → resolution |
|---|-------|-----------|--------|
| [04](04-crmyaml-login-duplicate-security-key.md) | `login` op defines `security` twice (invalid YAML) | contract/crm.yaml | **resolved** — duplicate key removed |
| [05](05-archive-disqualify-xmcptool-verb-mismatch.md) | archive/disqualify `x-mcp-tool` verbs contradict interfaces.md §2 | contract/crm.yaml | **resolved** — verbs now `archive_record` / `disqualify_lead` |
| [06](06-license-readme-says-4yr-decision-is-2yr.md) | Spec README says Change Date is 4yr; ratified is 2yr | README | **resolved** — README now "two years" |
| [07](07-list-tag-events-missing-from-catalog.md) | list/tag events missing from the closed catalog | contract/events.md §5 | **resolved** — lists/tags ratified **audit-only V1** (`events.md §5.3c`); relinkActivity drops `lead`; is_done → `activity.updated`. *Build: re-point auditOnlyWrites to §5.3c.* |
| [08](08-rbac-objects-for-lists-tags-consent-config.md) | RBAC matrix lacks lists/tags/consent config | features/04 §1 | **resolved** — `list`/`tag`/`consent_config` matrix + seeded-role migration mechanism |
| [09](09-meeting-host-has-no-column.md) | scheduling names a host the activity table can't store | crm.yaml vs data-model | **resolved** — `activity.host_user_id` (meeting-only) + index; `calendar_delegate` grant. *(duration already existed.)* |
| [10](10-no-gwui-reuse-founder-decision.md) | Founder decision: no gw-ui/Dispact reuse | ADR-0001 + design + EP09 | **resolved** — ADR-0001 **Amendment 2** + ADR-0040 + design language + B-EP09.2 re-scoped + LOC |
| [11](11-doi-token-has-no-issuance-surface.md) | DOI token redeemed but never minted/delivered | crm.yaml + data-model §3.4 | **resolved** — `POST …/consent/double-opt-in` (`issueDoubleOptIn`) + §3.4 |
| [12](12-lists-tags-catalogs-unpaginated.md) | `listLists`/`listTags` define no pagination | contract/crm.yaml | **resolved** — declared bounded **CAP-CATALOG** (cap 1000, `has_more`, no cursor) |
| [13](13-settings-needs-audit-read-and-passport-list.md) | Settings needs GET /audit-log + GET /passports | crm.yaml vs EP09.13b | **resolved** — `GET /audit-log` + `GET /passports` (`PassportSummary`) added; EP09.13b unblocked |
| [14](14-automations-crud-and-public-booking-missing.md) | No automations CRUD; booking lacks public + consent | crm.yaml vs EP09.14/15 | **resolved** — `/automations*`, `/public/booking/{host_slug}`, `CaptureConsent`; EP09.14/15 unblocked |
| [15](15-ledger-green-greys-fail-aa-on-tinted-grounds.md) | text greys fail WCAG AA at §2 roles | design §2 / ADR-0040 | **resolved** — "Contrast law" + `textMeta #5E6C65` blessed into canon (ADR-0040 amendment) |

Statuses: `open` (filed, awaiting spec change) · `accepted` (spec owner agreed)
· `resolved` (spec updated) · `wontfix` (intentional, code adjusted).

**Resolved entries are deleted, not kept** (founder practice — session
artifacts retire to git history). The durable record of an accepted change is
the spec's own amendment note (ADR status line / DECISIONS entry), not this
folder. Retired so far: 01–03 (2026-07-04 — ADR-0054 §2 cmd shape, §9
single-tx exception, `events.md §5.3b` pipeline/stage events).
