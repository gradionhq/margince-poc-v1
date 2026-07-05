# feedback/08 — RBAC object vocabulary lacks lists, tags, and consent config

**Date:** 2026-07-05 (overnight build session)
**Spec files:** `features/04 §1` (ratified default RBAC matrix), `contract/crm.yaml` (lists/tags/consent operations)
**Build impact:** `modules/identity/internal/policy` (coreObjects), `modules/collections`, `modules/consent`

## The defect

The ratified features/04 §1 matrix governs person, organization, deal,
lead, activity and pipeline — but the contract ships operations for
three more governable surfaces with no named object:

1. **Lists** and **tags** (10 operations). The build extended the
   policy vocabulary with `list` and `tag` objects (admin/manager/ops
   crud; rep create/read/update; read_only read) — needs ratification,
   and existing workspaces' seeded role documents need a refresh
   mechanism when the vocabulary grows.
2. **Consent purposes** (`createConsentPurpose`) are compliance
   configuration; the build gates them on the `pipeline` object as the
   config-surface precedent, which is semantically wrong. A
   `consent_config` (or `compliance`) object should exist.

## The ask

- Ratify the list/tag rows into the §1 matrix (or name different
  grants).
- Name the object that governs consent/compliance configuration.
- Specify how seeded role documents migrate when the object vocabulary
  grows (new workspaces pick up policy.go defaults; existing rows do
  not).
