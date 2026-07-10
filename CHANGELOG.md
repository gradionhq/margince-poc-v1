# Changelog

All notable changes to this project are documented in this file.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).
This is a pre-release proof of concept: nothing has been versioned or
released yet, so everything that exists lives under Unreleased. Version
numbers appear here when releases start.

## [Unreleased]

### Added

- **Foundation (WP0)**: the full core data model as reversible
  migrations — RLS (`ENABLE`+`FORCE`, deny-on-unset) on every tenant
  table, composite same-workspace foreign keys, append-only audit log,
  transactional event outbox, and the core/custom migration namespaces.
- **Contract pipeline**: `api/crm.yaml` (OpenAPI 3.1) → generated types
  + chi server; every operation mounted; regeneration drift is
  merge-blocking; every `crm.yaml` operation is implemented.
- **Auth and tenancy**: workspace bootstrap, Argon2id login, opaque
  server-side sessions, five seeded system roles, object RBAC +
  own/team/all row scopes, and the read/full seat ceiling.
- **Core CRM spine (WP1)**: people, organizations, leads (with
  promotion), pipelines/stages, deals (stage-semantic advance, FX freeze
  at close), activities and polymorphic links, two-record merge,
  lists/tags, relationships/partners, deal stakeholders, scheduling.
- **Event bus**: the events.md envelope over a transactional outbox →
  Redis Streams relay, consumer groups, and at-least-once dedupe.
- **Governed agent surface**: Agent Seat Passports, the MCP stdio server
  and hosted A2 transport (OAuth), the 🟢/🟡 autonomy tiers enforced
  below the transport on MCP and REST alike, and the approval engine
  (stage → human decision → single-use redemption).
- **AI surfaces**: model routing (`ai-routing.yaml`, Anthropic BYOK /
  Ollama / offline fake), the Surface-B runner + scheduler, search
  (FTS + pgvector hybrid), capture connector seam, cold-start read-back.
- **GDPR arm**: per-purpose consent with default-deny suppression,
  retention evaluator with DE (GoBD) statutory floors, legal hold,
  Art. 17 erasure with re-capture suppression, Art. 15 SAR assembly.
- **Embedded web UI**: login/bootstrap, people, leads, deal board,
  timeline, search, reports, privacy inbox — a dependency-free SPA
  served from the binary.
- **Quality gates**: golangci-lint + depguard, go-arch-lint, tree-derived
  architecture/schema/license fitness tests, contract drift-lint, and a
  real-Postgres integration lane covering the security invariants.
- **Deterministic gate parity** (ported/adapted from the `margince-poc`
  gate suite): the `audit_log` action/actor_type enum-coherence fitness
  test (contract ⇄ DDL CHECK, with a self-cleaning DB-only-verb waiver), a
  contract `$ref` pre-flight that fails on dangling pointers with a readable
  message before codegen, and two DB-free static gates in the deterministic
  lane — `rls-store-path` (no `internal/modules` statement addresses the
  superuser pool directly, `// rls-exempt:` escape for fleet enumeration)
  and `no-jurisdiction` (no country-specific regulatory identifier in core
  code, only in the jurisdiction seam). Plus `check-craft-doc` (the
  `## Craftsmanship` contract stays pinned in AGENTS.md), all wired into
  `make check` + CI; the two static gates also run in the pre-push hook.
- **UAT enforced in CI** — the AC-screen + axe WCAG 2.2 AA acceptance
  harness (`make frontend-e2e`) now runs as a required CI `uat` job, so a UI
  regression that breaks a named acceptance criterion fails the merge.
- **Storybook UAT** (adopted from the foundation skeleton, version-matched
  to this repo's Vite 6 / Vitest 3): Storybook 9 (react-vite) + a11y/docs
  addons + a design-system story catalog, and `make fe-uat` — a
  change-scoped render+capture gate that renders a diff's changed component
  stories in headless Chromium (no live stack) and fails on an unclean
  render or a changed component with no story. CI builds the catalog to
  catch a broken story.
- **Isolated per-worktree UAT env** (adopted from the skeleton) —
  `make uat_env UAT_SLUG=<slug>` boots a full live stack on the shared infra
  with its own `margince_uat_<slug>` database and slug-derived api/FE ports,
  so two worktrees run live UAT concurrently without colliding;
  `uat_env_stop [DROP=1]` tears it down.
- **CI runs only the checks a change can affect** (skeleton parity) — a
  `changes` classifier (dorny/paths-filter) splits the diff into `code`
  (non-docs) and `e2e` (backend/frontend/infra), and every job gates on it:
  a docs-only PR skips every code gate, a change touching none of
  backend/frontend/infra skips the live-boot + UAT jobs, and draft PRs run
  nothing until marked ready. `craft-residue` still fires on any file.

### Changed

- **Integration lane runs in parallel** (skeleton parity) — `make
  test-integration` now gives each `//go:build integration` package its own
  throwaway database (`CREATE DATABASE … TEMPLATE margince_test`, a fast file
  copy) plus a private MinIO bucket, so packages share nothing and run
  concurrently instead of serialized under one shared DB. Within a package it is
  still `-p 1`, so no test file changed. Locally the full lane drops ~45%
  (≈113s → ≈62s); the floor is now the single heaviest package. Same zero-skip
  teeth. New helpers: `test-db-up` (build the template), `test-it DIR=…` (one
  package on a clone), `test-integration-serial` (the old lane, for debugging).
- **Integration schema reset is migrate-once, not per-test** — the shared
  cross-module harness migrates the database a single time per test process
  (`internal/platform/testdb.EnsureSchema`) and resets between tests with a fast
  `TRUNCATE` (`testdb.Truncate`) instead of dropping and re-running every
  migration, which dominated the heaviest package (~180 tests × a full
  re-migrate). Safe because no migration seeds reference data a test depends on.

[Unreleased]: https://github.com/gradionhq/margince
