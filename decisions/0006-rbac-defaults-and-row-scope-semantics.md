# 0006 — RBAC enforcement semantics + the seeded role policies

**Status:** accepted (PoC scope) · 2026-07-03
**Spec refs:** features/04 §1, data-model §2.4, B-EP03.1/.2/.3a

The spec fixes the mechanism (object-level CRUD × row_scope own|team|all,
policy JSONB on `role`, same path for humans and agents) but not the
concrete default policy matrix or several edge semantics. These are the
defaults this implementation chose; each is a P1 "defaults are decisions"
call, revisitable by editing `crm-auth/internal/policy` in one place.

## The seeded policy matrix

| role | person/org/deal/activity | lead | pipeline | row_scope |
|---|---|---|---|---|
| admin | CRUD | CRUD | CRUD | all |
| manager | CRUD | CRUD | read | team |
| rep | create/read/update | CRUD¹ | read | team |
| read_only | read | read | read | all |
| ops | CRUD | CRUD | CRUD | all |

¹ Disqualifying a lead is audited as `archive` and RBAC-gated as
`delete`; it is routine rep work, so rep gets delete **on leads only**.

## Semantics decided here

- **Object denial vs row miss.** A role lacking the action gets `403
  permission_denied` (new sentinel `errs.ErrPermissionDenied` — upstream
  registry gap, `../fable feedback/14`). A row outside the caller's
  row scope answers **404**, indistinguishable from nonexistence — the
  same stance RLS takes for cross-tenant probes.
- **Ownerless rows are workspace-shared.** `owner_id IS NULL` is visible
  at every scope tier; row scope restricts *owned* records. (A CRM where
  an unassigned inbound lead is invisible to everyone is broken.)
- **Team scope** = own records ∪ ownerless ∪ records owned by any member
  of any team the caller belongs to (flat, via `team_membership`; no
  hierarchy — features/04 keeps SF-style sharing trees out of V1).
- **Row scope applies to person/organization/deal/lead** (the
  owner-bearing tables). Activities and pipelines have no `owner_id`;
  they are governed by object-level grants only. Activity visibility
  via its linked records is future work, noted not built.
- **Multiple roles merge** as union of grants + widest row scope; an
  unset/unknown scope resolves to `own`, never silently to `all`.
- **The system principal bypasses RBAC** (workspace provisioning runs as
  `system` before any human exists); it never serves user requests.
- **Zero-value `Permissions` denies everything** — an auth path that
  forgets to resolve roles fails closed.
- **Enforcement lives at the store entry points** (`require` +
  `ensureVisible` + the list `scopeClause`), so the future MCP surface
  rides the identical path — no agent bypass (architecture/06).
- The RLS backstop keyed on `app.user_id` (B-EP03.3b), field masks
  (B-EP03.4) and `record_grant` sharing (A52) are **not** built yet.
