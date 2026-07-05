# Margince

**A CRM your AI agents can actually work in. And it's yours: you get
the source.**

CRMs got stuck. You pay per seat, per contact, per feature. You can't
change anything without consultants. And the "AI" is a sidebar that
summarizes what you typed in yourself.

We hit that wall ourselves, so we're building Margince: a fast,
opinionated core for the 80% every sales team needs, plus a governed
agent surface so the AI you already pay for (Claude, Copilot, your own)
works inside your customer data, not next to it.

Three things matter:

**Your agents do the real work.** An agent connects over MCP or plain
REST and gets audited tools. Every action has a risk tier. 🟢 actions
(reading, drafting, normal updates) just run and get logged. 🟡 actions
(sending mail, archiving, merging, closing a deal) stop and wait for a
human to approve them. An agent never gets more rights than the human
behind it, and it can never approve its own actions. Punkt.

**You change it by changing the code.** No config screens, no metadata
engine, no ceiling. Need a custom field or a workflow? That's a normal
code change in your own copy, protected by types, tests, and extension
seams upstream never touches. Made by you, by a partner, or by us.

**It runs where you want.** SaaS, your own servers, or fully local
including the LLM, for teams whose data can't leave the building.
Sub-100ms interactions is the budget, not the marketing line.

Built by [Gradion](https://gradion.com), licensed BUSL-1.1. We replace
our own HubSpot with it first. If it can't carry our pipeline, it
doesn't ship.

---

## This repository

This is the build repo: the running Go code. The full specification
(product, architecture, OpenAPI contract, data model, a ~700-ticket
work breakdown) lives in a separate spec repo at `../margince/specs/`.
We build contract-first, and when code and spec disagree, the spec
wins.

Progress and the pickup point live in **[STATUS.md](STATUS.md)**;
the binding engineering rules in [AGENTS.md](AGENTS.md). User and
operator documentation lives under [docs/](docs/):
[getting started](docs/tutorials/getting-started.md) ·
[how-to guides](docs/how-to/) (MCP server, passports, migrations) ·
[reference](docs/reference/) (every flag/env, make targets) ·
[explanation](docs/explanation/) (architecture, contract-first).
Vulnerabilities: see [SECURITY.md](SECURITY.md) — private reporting via
GitHub Security Advisories. Changes: [CHANGELOG.md](CHANGELOG.md).
Everything below this line is for people (and agents) working on the code.

## Quick start

```
make db-up && make migrate && make dev
```

Toolchain: Go ≥ 1.26, Docker (Postgres 16 + Redis 7 test containers),
and `golangci-lint`. `make -C backend help` lists every target;
`make -C backend hooks` installs the pre-commit hook (gofmt + the
license-header gate). A step-by-step walkthrough and the full flag/env
reference live in [docs/](docs/) (tutorials, how-to, reference,
explanation — see below).

Then open http://localhost:8080 — the embedded web UI (bootstrap a
workspace, people, the deal board, the timeline). It is a hash-routed,
dependency-free SPA served from the binary (`backend/web/`), and a plain
client of the same `/v1` contract as everything else — no backdoors
(ADR-0013).

Connect an agent (Surface A1): mint a passport (`POST /v1/passports`,
session-authed), then

```
MARGINCE_PASSPORT_TOKEN=mgp_… mcp --workspace <slug> --dsn …
```

serves the governed tool surface over stdio (MCP JSON-RPC). The same
token is a REST bearer credential, governed identically (see below).

Deployment note: the login and bootstrap rate limiters key on the direct
peer address (they refuse `X-Forwarded-For` — it is attacker-controlled);
behind a reverse proxy that collapses to one bucket, so enforce
per-client throttling at the proxy.

## How it's built

- **Contract-first.** `backend/api/crm.yaml` (OpenAPI 3.1) is the
  authoritative surface: 3.0-overlay → oapi-codegen types + chi server;
  every operation is mounted (unimplemented ones answer an explicit
  501); regeneration drift is merge-blocking.
- **One governed agent surface, every transport (ADR-0055).** The 🟢/🟡
  autonomy tier of an action is declared once (on the tool spec / the
  contract's `x-mcp-tool` annotation) and enforced below the transport:
  an agent mutation over MCP *or* REST resolves the same tier, stages
  the same approval when 🟡, and **default-denies** any mutating
  operation that carries no tier — fail-closed, drift-linted at build
  time. Governance actions (approving, consent, DSR, pipeline/stage
  config) are human-only at the contract, the gate, and the service.
  Admission re-derives the granting human's seat + RBAC live per call,
  so revocation binds mid-session.
- **The write shape.** Every mutation commits domain row + append-only
  `audit_log` row + `event_outbox` row in one transaction (spelled once
  in `platform/database/storekit`); provenance (`captured_by`) is
  stamped from the authenticated principal, never accepted from a
  request body; publishing is always through the outbox to Redis
  Streams, and consumers dedupe because the bus is at-least-once.
- **Tenancy as structure.** Every tenant table carries `ENABLE`+`FORCE`
  row-level security with deny-on-unset policies, reached only through
  the one workspace-transaction helper; every tenant-local foreign key
  is composite `(workspace_id, col)`, so a cross-workspace reference is
  rejected by the database, not merely hidden. Both invariants are
  fitness functions derived from the live schema, not maintained lists.
- **Layout** (spec ADR-0054/A69, decisions/0011):
  one Go module under `backend/` (`github.com/gradionhq/margince/backend`)
  as the `internal/{modules,platform,shared}` triad —
  `shared/{kernel,apperrors,ports}` (stdlib-only leaves), `platform/*`
  (plumbing, owns no domain), twelve `modules/` (identity, people,
  deals, activities, approvals, agents, ai, search, capture, consent,
  collections, and the `de` jurisdiction pack — no sibling imports),
  `internal/compose` (the one composition seam), and four process-role
  binaries
  `cmd/{api,worker,migrate,mcp}`. The DAG is enforced three ways
  (depguard, go-arch-lint, and architecture fitness tests that derive
  their package lists from the tree).

## What works today

- **Schema**: the full core data model (data-model.md §1–§11) as 19
  reversible migrations — uuidv7 shim, updated_at+version triggers,
  RLS `ENABLE`+`FORCE` with deny-on-unset policies on all 33 tenant
  tables, composite same-workspace foreign keys (0019, decisions/0010),
  append-only audit log, transactional event outbox, and the ADR-0017
  core/custom migration namespaces.
- **Contract pipeline**: OpenAPI 3.1 → 3.0 overlay → oapi-codegen types +
  chi server interface; all operations mounted (unimplemented ones
  answer explicit 501); drift is merge-blocking, and the agent-policy
  generator refuses any mutating operation without an autonomy
  annotation (the ADR-0055 drift-lint).
- **Auth (ADR-0043)**: workspace bootstrap — atomic across identity and
  cross-module defaults (C5, decisions/0010), Argon2id email/password
  login, opaque server-side sessions (SHA-256-stored, idle+absolute
  expiry, revoke-at-lookup), five seeded system roles, and the read/full
  seat ceiling enforced before RBAC on both REST and MCP (C2).
- **Core CRUD**: people (emails/phones, 409 dedupe with existing id),
  organizations (domains), pipelines/stages (seeded default), deals
  (advance with stage-semantic-derived won/lost, FX freeze at close,
  stage history), leads (segregated per ADR-0008, natural-key
  idempotent), activities (idempotent capture, polymorphic links, deal
  last_activity_at). Every write commits audit + outbox atomically;
  If-Match optimistic concurrency; keyset pagination.
- **Lead→person promotion (features/01 §6.4)**: `POST /leads/{id}/promote`
  on genuine engagement only; merges into an existing person via the
  email dedupe path — never a duplicate — else creates one carrying
  provenance/owner/email + `converted_from_lead_id`; one transaction,
  one audit row, `lead.promoted` + the caused `person.*` event.
- **Two-record merge (features/01 §1.3)**: `POST /people/{id}/merge` and
  `/organizations/{id}/merge` fold A→B in one transaction —
  collision-aware relink of emails/phones/domains/relationships/activity
  links/consent with zero orphaned FKs, fill-only survivorship, one-hop
  redirect chains, org hierarchy reparenting + the 1:1 partner
  extension, restrictive consent merge, and `person.merged`/
  `organization.merged` events. Reachable as the 🟡 `merge_records` tool
  (pins the survivor's version). Survivorship rules in decisions/0009.
- **Event bus (EP04)**: the full events.md §2 envelope (actor incl.
  passport/on-behalf-of, per-request `correlation_id`, `causation_id`,
  `audit_log_id` linking event↔audit row) as the Tier-0
  `shared/kernel/events` contract with the §5 catalog + §4.1 stream
  routing; the outbox relay (in-process worker, decisions/0005) shipping
  committed rows to Redis Streams with `FOR UPDATE SKIP LOCKED` + MAXLEN
  trimming; the §4.3 consumer-group subscriber (`XREADGROUP`/`XACK`,
  `XAUTOCLAIM` reclaim, in-process workspace filtering); and the
  `event_id` dedupe wrapper (96h TTL) that makes at-least-once safe.
- **RBAC (EP03 remainder)**: object-level CRUD enforcement per role at
  the store entry points (shared by REST and MCP — no agent bypass),
  own/team/all row-scope predicates over `owner_id` (out-of-scope rows
  answer 404, like cross-tenant), the five system roles seeded with real
  permission-policy documents (decisions/0006), and the governing rule
  recorded in `audit_log.authorization_rule`.
- **MCP/agent surface (EP06 WP4, Surface A1)**: Agent Seat Passports
  (`POST /passports` mints a scoped, expiring, revocable `mgp_` bearer
  token bound to its issuer — "agent ≤ human" structurally, and live:
  the granting human's seat + RBAC are re-derived at every admission
  through the `shared/ports/authz` seam), the `platform/auth` gate
  (scope ∧ seat ∧ tier BEFORE any handler; its own package so nothing
  mints an admitted capability elsewhere), the `agents` registry + the
  🟢 CRUD tool set (`search_records`, `read_record`, `create_record`,
  `update_record`, `log_activity`) plus the 🟡 `advance_deal` (its
  `TierDynamic` resolver: 🟢 open→open, 🟡 to won/lost — the always-🟡
  floor, resolved from the stage's semantic), `archive_record`,
  `promote_lead`, and `merge_records`, all composed over the frozen-v1
  `datasource.SystemOfRecordProvider` seam → the same store entry points
  as HTTP: same RBAC, row scope, audit, events. Served over stdio
  (`mcp --workspace <slug>` + `MARGINCE_PASSPORT_TOKEN`).
- **Transport-agnostic autonomy gate (ADR-0055, decisions/0012)**: the
  same passport rides the REST surface with the same governance — a 🟢
  mutation executes (agent-stamped provenance), a 🟡 mutation stages an
  approval and is redeemed by repeating the identical request with
  `X-Approval-Token`, an un-tiered mutating route is refused
  (default-deny), and the human-only governance surface (approvals,
  consent, DSR, pipeline/stage config, passports) rejects agent
  principals outright — the self-approval bypass is structurally closed.
- **Approval engine (EP07 core, ADR-0036)**: a refused 🟡 action lands
  in the `approval` inbox (`approval.requested`) with a one-line
  summary, the exact proposed change, its content hash, and the target
  row's version; humans decide over `/approvals` — the inbox shows only
  approvals the caller could themselves decide (C3, decisions/0010);
  deciding is human-only, and the approver must hold the RBAC the effect
  itself needs; redemption is single-use, 15-minute window, bound to the
  staging passport and the content hash, refused on target version skew
  (the human's yes was about the world they saw). Full mechanics in
  decisions/0008.
- **Web UI**: login/bootstrap, people, leads (with the
  promote-on-engagement dialog), the stage-column deal board with
  advance, and the activity timeline — embedded static SPA, no build
  chain, design tokens from the spec's design language; security headers
  (CSP, frame-denial, nosniff) on every response.
- **Gates**: golangci-lint (incl. depguard module DAG, default-deny for
  the Tier-0 layer) clean; go-arch-lint as a hard gate; leaf-purity and
  interface-freeze fitness tests; the ADR-0055 contract drift-lint; an
  integration lane proving the RLS ∅-query, GUC-unset deny, pool-safety,
  version-skew and audit-immutability invariants, the two schema fitness
  functions, an HTTP end-to-end sales flow, the governed-agent-writes
  loop (🟢 executes, 🟡 stages → human approves → token retry executes
  once, agent self-approval refused), the read-seat ceiling, the
  permission-filtered approval inbox, atomic-bootstrap rollback, the
  person/org merge suites, and the bus lane (relay exactly-once /
  crash-republish / commit order, subscriber ack+reclaim+tenant filter,
  dedupe, envelope completeness over the wire).

## Deliberately not here yet

The approval edit-then-approve re-gating path (`edited_payload` answers
422 until it re-enters the gate properly), disqualify/enrich/send tools
(their underlying verbs first), the hosted A2 MCP server (OAuth2 + PKCE
+ DCR + the JWS approval-token serialization — until then `/passports`
is the A1 issuance path, decisions/0012), `run_report`/schema
introspection on the SoR seam, capture connectors, search/context graph,
the RLS row-scope backstop (B-EP03.3b), field-level masking (B-EP03.4),
record grants (A52), consent enforcement, the Idempotency-Key replay
store, event versioning/replay/dead-letter (B-EP04.12/.14/.15), and the
River job runner (deferred, decisions/0005). The contract routes for all
of these exist and answer 501.

## Working conventions (where findings go)

Building from the spec is also a test **of** the spec, so findings are
routed, not lost:

- **Implementation decisions** — anything the spec left open that this
  code had to decide — get a numbered record in
  decisions/, so a reviewer can separate "the spec says"
  from "we chose".
- **Spec/ticket defects** — a contradiction, an omission, a vocabulary
  gap, an unimplementable acceptance criterion found while building — get
  a local note in `feedback/`, each naming the spec section and a
  suggested fix. Notes in `feedback/` are git-ignored — local session
  scratch for reconciling defects upstream (only its
  [README](feedback/README.md) is tracked). Once a defect is resolved in
  the spec, its note is deleted — the durable record is the spec's own
  amendment (ADR/DECISIONS), not this folder.
- **Session state** — progress, in-flight work, pickup point — goes in
  [STATUS.md](STATUS.md), updated at the end of every working session.

## Engineering rules learned from the review loop

Two external red-team passes ran against this code (2026-07-03 and
2026-07-04; fully addressed and retired to git history). The rules below
exist because each was violated once here; they are binding for all
future work in this repo (mirrored in [AGENTS.md](AGENTS.md)):

1. **Fix the invariant, not the call site.** Every pass-2 Medium was a
   pass-1 fix applied to the case under the reviewer's finger while an
   adjacent copy stayed broken (open vs. closed deals; person/org but not
   lead; direct read but not idempotent replay). Before closing a finding,
   grep for every mutation/read site of the same column, constraint, or
   record and fix them as one change.
2. **Prefer fitness functions over point fixes.** A hand-maintained list
   (RLS table enrolment, a lint allow-list) rots silently; a test that
   derives the obligation from the system itself (every `workspace_id`
   table must have FORCE RLS; every CHECK violation must map to a 4xx)
   enforces the *class*. When a fix defends an invariant, ask what gate
   proves it stays fixed.
3. **Anything that returns a record is a read** and carries the read
   path's row-scope gate — including error paths, idempotent-replay
   paths, and conflict disclosures. Error paths are disclosure paths.
4. **Comments carry no build-process residue.** No review-ticket numbers,
   no "fixed per finding #N", no changelog narration — a comment states
   the invariant or trade-off so it reads true standing alone, years
   later, to someone who never saw the review. The history lives in git,
   not in the source. (Same for test names:
   name the invariant pinned, not the review that demanded it.)
5. **Don't rationalize a known gap — close it or gate it.** Pass 1's
   dedupe crash-window was answered with a comment arguing it was safe;
   pass 2 showed the argument wrong (the fallback layer prevents double
   effects, not dropped ones). If a design carries a window, either
   restructure so it cannot happen (run-then-mark) or add the failing
   test that documents it honestly.

## License

**Business Source License 1.1** (`BUSL-1.1`) — see [LICENSE](LICENSE). Licensor:
Gradion. Source-available, **not** OSI open source: the full source is public and
free to read, run, and modify.

- **Free** for your own internal production use up to **10 Seats** (a Seat is an
  identified person with credentials; AI agents, service accounts, and external
  data subjects are **not** Seats). From the 11th Seat a commercial subscription
  applies, self-host or partner-hosted alike.
- Hosting or reselling it as a service to third parties requires an **Authorized
  Hosting Partner** agreement.
- **Every release converts to Apache 2.0 on its Change Date — two years after it
  ships** (BUSL body caps this at four years; we hold ours to two, A37/ADR-0029).

The Additional Use Grant fills only BUSL's parameter fields; the license body is
the verbatim canonical text, so SPDX/GitHub detect it as `BUSL-1.1`. The full
model, rationale, and enforcement design live in the spec's
[`business/12-license.md`](../margince/specs/business/12-license.md). The exact
Additional Use Grant wording is **provisional pending counsel** (12-license.md §10).
