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
- **Consent lends a passport**: `GET /oauth/authorize` redirects to an SPA
  consent screen where the human selects one of their own existing agent
  passports; the connection receives exactly that passport's scopes,
  carried by a *new* grant-bound passport, so revoking a connection never
  touches the human's own credential. Deny
  answers the client `access_denied`. A human with no passport is guided
  to mint one and brought back to finish connecting, which means
  `claude mcp add` no longer completes unattended for a fresh account.
  The lend is recorded in the audit trail. Deactivating a member ends
  their consents that no client redeemed yet, alongside the connections
  that already exist — so reactivating them later cannot hand out a
  connection on authority an admin took away.
- **AI surfaces**: model routing (`ai-routing.yaml`, Anthropic BYOK /
  Ollama / vLLM / offline fake), the Surface-B runner + scheduler, search
  (FTS + pgvector hybrid), capture connector seam, cold-start read-back.
- **Embedding drift self-heal** (ADR-0069 §3a): a periodic worker sweep
  re-embeds entities whose embed event the at-least-once bus lost, with
  no operator confirm; the preview → confirm reindex remains solely for
  a changed embed binding, and the ops banner fires only on that case.
- **GDPR arm**: per-purpose consent with default-deny suppression,
  retention evaluator with DE (GoBD) statutory floors, legal hold,
  Art. 17 erasure with re-capture suppression, Art. 15 SAR assembly.
- **Web UI**: login/bootstrap, people, leads, deal board, timeline,
  search, reports, privacy inbox — the Vite/React app in `frontend/`, a
  standalone static build served separately from the API.
- **Quality gates**: golangci-lint + depguard, go-arch-lint, tree-derived
  architecture/schema/license fitness tests, contract drift-lint, and a
  real-Postgres integration lane covering the security invariants.
- **Craftsmanship gate, strict** (ADR-0045): `craft static` now fails on
  MAJOR findings as well as BLOCKER ones, in the pre-push hook (diff-scoped)
  and in CI's `craftsmanship` job (whole tree). MINOR stays advisory. Test
  files carry their own size ceilings — 160 body lines / 1000 file lines,
  against 80 / 500 for product code — because a long scenario test that sets
  up, acts and asserts once is not the god-function smell the product
  thresholds hunt. Arming this meant clearing the whole backlog first: every
  swallowed error, bare `any` in a signature, boolean-trap signature,
  assertion-free test and over-long function in the tree.
- **Company 360**: `GET /organizations/{id}/360` serves the whole company
  record page in one transaction — profile, contacts with §4 relationship
  strength and per-purpose consent, deals, timeline, tags, list
  memberships, decidable approvals, open next steps, and what changed
  since the caller last visited. Authorization is per section: a section
  the caller may not read is omitted and named in `sections_omitted`,
  never returned empty. `POST /organizations/{id}/view-ack` is the
  explicit, human-only, monotonic visit baseline those counts run against.
- **Company page verbs**: the record page opens a deal on the company it is
  showing (open stages only, the organization implied rather than asked for),
  and applies a tag or a list membership by typed name, creating either when
  the name is new. Each verb renders only on a section the caller's grants let
  them read, and an already-applied tag or membership is treated as the asked-
  for state rather than reported as a collision.
- **Company connections**: `GET /organizations/{id}/graph` serves the
  account's one-hop neighbourhood as nodes and edges the client lays out —
  its contacts by employment (weighted by §4 strength), its open deals and
  the stakeholder seats on them, its parent, children and partner
  companies, and which contact the active signal's warm-intro path routes
  through. Authorization is per group, the same posture the 360 takes for
  its sections; node selection is deterministic and `dropped_count` says
  what the caps left out. The rail's connections card draws it as an ego
  diagram over a keyboard-reachable node list, and the diagram is
  decorative — everything it shows is in the list.

### Changed

- **AI model routing is now per-engineer**: the working dev config moved
  from a committed `backend/ai-routing.yaml` to a gitignored
  `config/ai-routing.yaml`, seeded from `config/ai-routing.example.yaml`
  by `make install` / `make dev`. Engineers bind their own local models
  without touching a committed file; the annotated template stays the
  parse-guarded source of truth.

[Unreleased]: https://github.com/gradionhq/margince
