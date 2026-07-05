# 13 — Settings governance UI lacks contract surfaces: audit-log read + passport list

**Where:** `crm.yaml` vs B-EP09.13b (AC-settings-16) and the passports
surface.

**What the spec asks:** AC-settings-16 wants "the human+agent attributable
audit log with live filters" rendered in Settings; the passports card
implies listing/revoking existing passports.

**What the contract has:** `AuditLogEntry` is defined in components but NO
operation returns it (no `GET /audit-log`); `/passports` is POST-only and
`/passports/{id}` DELETE-only — an issued passport cannot be listed, so the
revoke affordance has nothing to enumerate.

**What the build did in the meantime:** Settings (frontend) renders the
governance sections that have live seams (/me roles, passport minting with
show-once token, consent purposes, DSR privacy inbox, the locked autonomy
table) and omits the audit view rather than faking it (evidence-or-omit).

**Proposed spec change:** add `GET /audit-log` (cursor-paginated, filters:
actor, entity_type/id, action, from/to) and `GET /passports` (caller's own,
metadata only — no token re-disclosure).
