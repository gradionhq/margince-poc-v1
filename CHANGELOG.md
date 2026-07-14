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
  Ollama / vLLM / offline fake), the Surface-B runner + scheduler, search
  (FTS + pgvector hybrid), capture connector seam, cold-start read-back.
- **GDPR arm**: per-purpose consent with default-deny suppression,
  retention evaluator with DE (GoBD) statutory floors, legal hold,
  Art. 17 erasure with re-capture suppression, Art. 15 SAR assembly.
- **Web UI**: login/bootstrap, people, leads, deal board, timeline,
  search, reports, privacy inbox — the Vite/React app in `frontend/`, a
  standalone static build served separately from the API.
- **Quality gates**: golangci-lint + depguard, go-arch-lint, tree-derived
  architecture/schema/license fitness tests, contract drift-lint, and a
  real-Postgres integration lane covering the security invariants.

### Changed

- **AI model routing is now per-engineer**: the working dev config moved
  from a committed `backend/ai-routing.yaml` to a gitignored
  `config/ai-routing.yaml`, seeded from `config/ai-routing.example.yaml`
  by `make install` / `make dev`. Engineers bind their own local models
  without touching a committed file; the annotated template stays the
  parse-guarded source of truth.

[Unreleased]: https://github.com/gradionhq/margince
