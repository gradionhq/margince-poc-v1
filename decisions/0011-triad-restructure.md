# 0011 — Adopt the ADR-0054 triad layout; separate cmd/<role> dirs; module path `github.com/gradionhq/margince/backend`

Status: accepted · 2026-07-04 (founder). Supersedes the layout half of
[0001](0001-layout-and-module-path.md) (already banner-superseded by spec
ADR-0054/A69); executes the migration plan in
the 2026-07-04 architecture improvement plan (addressed in full; retired to git history).

## What

1. **Layout** — the `backend/internal/{modules,platform,shared}` triad per
   spec ADR-0054/A69: `shared/{kernel,apperrors,ports}` (Tier-0 leaves),
   `platform/*` (technical plumbing, owns no domain), `modules/{identity,
   people,deals,activities,approvals,agents}` (bounded capabilities, no
   permanent core catch-all), `internal/compose` (the composition layer the
   cmd roles share), `api/crm.yaml` (the contract), `migrations/`. The `web/`
   embedded SPA stays at `backend/web` until a real `frontend/` starts.
2. **Command shape** — **separate `backend/cmd/{api,worker,migrate,mcp}`
   binaries** (founder call 2026-07-04). This deviates from ADR-0054 §2,
   which chose one multi-command `cmd/margince` binary; the spec-side
   amendment was filed as feedback/01 and is now applied (ADR-0054 §2
   amended 2026-07-04; the feedback file is retired to git history). The
   fork-rebuild seam ADR-0054 §2 wanted from a single binary is provided by
   `internal/compose` instead: one composition package, four thin mains.
   `cmd/api` keeps an inline outbox relay behind `--inline-relay`
   (default true) so small installs stay single-process; `cmd/worker` runs
   the relay standalone for split deployments.
3. **Module path** — `github.com/gradionhq/margince/backend` (replaces the
   stale `github.com/gradionhq/fable-poc`). Ratified by the factory 1c
   mapping; survives a later repo rename to `gradionhq/margince` unchanged.
4. **Package renames** with the moves: `sor`→`datasource` (under
   `shared/ports/`), `kernel/errs`→`shared/apperrors`, `kernel/prov`→
   `shared/kernel/provenance`, `crmctx`→`shared/kernel/principal`,
   `internal/pg`→`platform/database`, `internal/gate`→`platform/auth`
   (per ADR-0054 §8), `internal/bus`→`platform/events`. `crm-contracts`
   moves to `internal/contracts` but keeps package name `crmcontracts`
   (generated code stays byte-identical for the drift gate).

## The crm-core split (ownership map)

No permanent core module (ADR-0054 §6). `crm-core` splits by aggregate:

- **`modules/people`** — person, organization, **lead** (promotion writes
  into the person/org aggregates: email-collision promotion merges into an
  existing person and needs dedupe internals, not a port; lead dedupe is
  defined against `person.email`), plus `merge.go` and `promote.go`.
- **`modules/deals`** — deal, pipeline/stage (incl. workspace pipeline
  seeding — the seed IS the default pipeline).
- **`modules/activities`** — the activity timeline. The activity link-walk
  visibility clause lives in `platform/auth` (not the module): both the
  timeline and people's promotion-evidence check enforce it, and scope
  policy has exactly one spelling (ADR-0054 §8).
- Store mechanics (audit+outbox single-tx write shape, keyset cursor codec,
  optimistic-version patch, pg-violation helpers) → `platform/database/storekit`;
  entity-agnostic RBAC (`require`, scope clauses, `ensureVisible`) →
  `platform/auth`, joining `Admit` (ADR-0054 §8).
- The `datasource.SystemOfRecordProvider` entity dispatch becomes a composite
  in `internal/compose` over per-module sub-providers.

**Ratified deviation from a strict ADR-0054 §9 ports reading:** cross-entity
operations stay single-module, single-transaction SQL owned by the primary
aggregate — `people`'s merge relinks `deal`/`partner`/`activity_link` rows,
`people`'s promotion inserts the `deal` row, each inside one
`WithWorkspaceTx`. The merge invariant is single-transaction atomicity over
one shared schema; tx-carrying ports would add indirection without
isolation (RLS + composite FKs are the real fence). The boundary is enforced
at the Go-import level (no module imports a sibling); table-touch ownership:
`people` may write deal/partner/activity_link rows **only inside
merge/promote**, nothing else crosses. Cross-module wiring that stays
port/injection-shaped: identity's workspace-seed callback (implemented by
deals), agents' approvals adapter — both composed in `internal/compose`.

## Why

The PoC is young enough to rework; the spec (ADR-0054/A69) has locked the
triad as the normative open-source layout, and P3 makes the spec win.
Migration runs in gate-green phases (`make check` + `make test-integration`
after each) so every commit is bisectable.
