# 0001 — Repo layout per ADR-0016; module path `github.com/gradionhq/fable-poc`

Status: **superseded 2026-07-04 by [spec ADR-0054](../../margince/specs/spec/decisions/ADR-0054-backend-layout-modules-platform-shared-and-fork-custom-go-seam.md)** (target layout is now the `backend/internal/{modules,platform,shared}` triad + a fork-custom Go seam; `crm feedback/01` is resolved there). The record below stands as the accepted decision **for the current tree**; it is treated as harvested implementation material, migrated to the triad in gate-green phases per the 2026-07-04 architecture improvement plan (addressed in full; retired to git history — see [0011](0011-triad-restructure.md)). Originally accepted · 2026-07-03.

The spec carries three unreconciled layout prescriptions (see
`fable feedback/01`): ADR-0014/0016's single root `go.mod` + top-level
`crm-*` dirs, doc-14's `crm/` multi-module sketch, and the factory GD-4
`backend/internal/modules` target. **Founder call for this build
(2026-07-03): follow ADR-0016** — it is the normative spec and the layout
the executable backlog (`B-EP01.1a` onward) validates against.

Consequences:
- One `go.mod` at the root; each `crm-*` module fences its guts with its
  own `internal/`; depguard + go-arch-lint declare the DAG.
- Tier-0 seam packages sit at the top level, named for the seam
  (`crmctx`, `sor`, `mcp`, `connector`, `workflow`, `model`, `retrieval`,
  `jurisdiction`); the shared kernel lives under `kernel/`.
- Shared *technical* infrastructure the composition root owns — the pg
  pool + RLS transaction helper, the migration runner, the RFC 7807
  mapper, the contract-surface assembly — lives under the root
  `internal/` (importable repo-wide, invisible outside). This is the
  platform layer ADR-0016 never places; recorded here rather than
  invented silently.
- `go.work` exists only for the future Dispact/`@gradion/contracts` seam.
