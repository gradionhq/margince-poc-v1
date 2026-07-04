# Margince CRM — fable-poc

A from-scratch implementation of the [Margince spec](../margince%20specs/)
— WP0 repo foundation plus the WP1 core spine, built contract-first
against `spec/contract/crm.yaml` and `data-model.md`.

Current progress, the in-flight work, and the exact session-pickup point
live in **[STATUS.md](STATUS.md)** — read it first when resuming work.

```
make db-up && make migrate && make dev
```

Toolchain: Go ≥ 1.22, Docker (Postgres 16 + Redis 7 test containers),
`golangci-lint`, and **python3** — `make gen` / `make drift` run the stub
generator (`tools/gen-stubs`) through it. Deployment note: the login and
bootstrap rate limiters key on the direct peer address (they refuse
`X-Forwarded-For` — it is attacker-controlled); behind a reverse proxy
that collapses to one bucket, so enforce per-client throttling at the
proxy.

Then open http://localhost:8080 — the embedded web UI (bootstrap a
workspace, people, the deal board, the timeline). It is a hash-routed,
dependency-free SPA served from the binary (`web/`), and a plain client
of the same `/v1` contract as everything else (ADR-0013: no backdoors).

## What works today

- **Schema**: the full core data model (data-model.md §1–§11) as 19
  reversible migrations — uuidv7 shim, updated_at+version triggers,
  RLS `ENABLE`+`FORCE` with deny-on-unset policies on all 33 tenant
  tables, **composite same-workspace foreign keys** (every tenant-local FK
  is `(workspace_id, col) REFERENCES ref(workspace_id, id)`, so a
  cross-workspace reference is rejected by the DB, not just hidden by RLS —
  0019, decisions/0010), append-only audit log, transactional event
  outbox, and the ADR-0017 core/custom migration namespaces.
- **Contract pipeline**: OpenAPI 3.1 → 3.0 overlay → oapi-codegen types +
  chi server interface; all 81 operations mounted (unimplemented ones
  answer explicit 501); drift is merge-blocking.
- **Auth (ADR-0043)**: workspace bootstrap — **atomic across identity and
  cross-module defaults** (the default-pipeline seed runs inside the
  bootstrap transaction, so a seed failure rolls the whole tenant back — C5,
  decisions/0010), Argon2id email/password login, opaque server-side
  sessions (SHA-256-stored, idle+absolute expiry, revoke-at-lookup), five
  seeded system roles, and the read/full seat ceiling enforced before RBAC
  on both REST and MCP (C2).
- **Core CRUD**: people (emails/phones, 409 dedupe with existing id),
  organizations (domains), pipelines/stages (seeded default), deals
  (advance with stage-semantic-derived won/lost, FX freeze at close,
  stage history), leads (segregated per ADR-0008, natural-key idempotent),
  activities (idempotent capture, polymorphic links, deal
  last_activity_at). Every write commits audit + outbox atomically;
  If-Match optimistic concurrency; keyset pagination.
- **Lead→person promotion (features/01 §6.4)**: `POST /leads/{id}/promote`
  on genuine engagement only (the trigger enum has no
  cold-outbound-no-reply, by design); merges into an existing person via
  the email dedupe path — never a duplicate — else creates one carrying
  provenance/owner/email + `converted_from_lead_id`; one transaction, one
  audit row, `lead.promoted` + the caused `person.*` event.
- **Two-record merge (features/01 §1.3)**: `POST /people/{id}/merge` and
  `/organizations/{id}/merge` fold A→B in one transaction — collision-aware
  relink of emails/phones/domains/relationships/activity links/consent with
  zero orphaned FKs (primary-slot demotion, duplicate-edge archival),
  fill-only survivorship, one-hop redirect chains, org hierarchy
  reparenting + the 1:1 partner extension, restrictive consent merge, and
  `person.merged`/`organization.merged` events. Reachable as the 🟡
  `merge_records` tool (pins the survivor's version). Survivorship rules,
  incl. the two ratifiable judgement calls (restrictive consent,
  both-have-partner), are in decisions/0009.
- **Event bus (EP04)**: the full events.md §2 envelope (actor incl.
  passport/on-behalf-of, per-request `correlation_id`, `causation_id`,
  `audit_log_id` linking event↔audit row) as the Tier-0 `kernel/events`
  contract with the §5 catalog + §4.1 stream routing; the outbox relay
  (in-process worker, decisions/0005 — River deferred) shipping committed
  rows to Redis Streams with `FOR UPDATE SKIP LOCKED` + MAXLEN trimming;
  the §4.3 consumer-group subscriber (`XREADGROUP`/`XACK`, `XAUTOCLAIM`
  reclaim, in-process workspace filtering); and the `event_id` dedupe
  wrapper (96h TTL) that makes at-least-once safe.
- **RBAC (EP03 remainder)**: object-level CRUD enforcement per role at
  the store entry points (the path MCP will share — no agent bypass),
  own/team/all row-scope predicates over `owner_id` (out-of-scope rows
  answer 404, like cross-tenant), the five system roles seeded with real
  permission-policy documents (validator + merge in
  `crm-auth/internal/policy`, semantics in decisions/0006), and the
  governing rule recorded in `audit_log.authorization_rule`.
- **MCP/agent surface (EP06 WP4, Surface A1)**: Agent Seat Passports
  (`POST /passports` mints a scoped, expiring, revocable `mgp_` bearer
  token bound to its issuer — "agent ≤ human" structurally, and live: the
  granting human's RBAC is reloaded per call), the `internal/gate`
  admission gate (scope ∧ tier BEFORE any handler; its own package so
  nothing mints an admitted capability elsewhere), the `crm-agents`
  registry + the 🟢 CRUD tool set (`search_records`, `read_record`,
  `create_record`, `update_record`, `log_activity`) plus the 🟡
  `advance_deal` (its `TierDynamic` resolver: 🟢 open→open, 🟡 to won/lost —
  the always-🟡 floor, resolved from the stage's semantic), `archive_record`,
  `promote_lead`, and `merge_records`, all composed over the
  `sor.SystemOfRecordProvider` seam (crm-core's SoR-mode provider → the same
  store entry points as HTTP: same RBAC, row scope, audit, events). The gate
  also enforces the **read/full seat ceiling** (a read seat, or an agent
  acting for one, may run only read tools — A62/ADR-0047) before tier. Served
  over stdio (`crm mcp --workspace <slug>` + `MARGINCE_PASSPORT_TOKEN`)
  speaking MCP JSON-RPC. The passport token also rides the REST surface, but
  **read-only**: agent mutations must flow through the governed MCP tools, so
  there is exactly one agent-mutation choke point across transports (C1,
  decisions/0010; a mutating REST call from a passport is refused
  `agent_surface_restricted`). A refused 🟡 call is STAGED for human decision
  (see next bullet).
- **Approval engine (EP07 core, ADR-0036)**: a refused 🟡 tool call
  lands in the `approval` inbox (`approval.requested`) with a one-line
  summary, the exact proposed change, its content hash, and the target
  row's version; humans decide over `/approvals` — **but the inbox shows
  only approvals the caller could themselves decide** (list/get filter by
  the same grant the decision requires, so a low-privilege user never sees
  another team's proposed changes — C3, decisions/0010); deciding is
  human-only, and the approver must hold the RBAC the effect itself needs;
  the agent redeems by repeating the IDENTICAL call plus `approval_id` —
  single-use, 15-minute window, bound to the staging passport and the
  content hash, refused on target version skew (the human's yes was about
  the world they saw). `archive_record`, `promote_lead`, and `merge_records`
  are registered now that the loop carries them; audit gained
  `approve`/`reject` verbs (decisions/0008).
- **Web UI**: login/bootstrap, people, leads (with the promote-on-
  engagement dialog), the stage-column deal board with advance, and the
  activity timeline — embedded static SPA, no build chain, design tokens
  from `design/00-design-language.md`; security headers (CSP,
  frame-denial, nosniff) on every response.
- **Gates**: golangci-lint (incl. depguard module DAG, now default-deny
  for the Tier-0 layer) clean; go-arch-lint as a hard Layer-C gate; leaf-purity
  architecture test; integration lane proving the RLS ∅-query, GUC-unset
  deny, pool-safety, version-skew and audit-immutability invariants, the
  two schema fitness functions (RLS-on-every-tenant-table and
  every-tenant-local-FK-is-composite, so the invariants can't rot as tables
  are added), an HTTP end-to-end sales flow, the passport-read-only-on-REST
  and read-seat-ceiling gates, the permission-filtered approval inbox, the
  atomic-bootstrap rollback, the person/org merge (referential integrity,
  survivorship, consent, hierarchy, partner, error paths + the full
  `merge_records` MCP approval loop), and the bus lane (relay exactly-once /
  crash-republish / commit order, subscriber ack+reclaim+tenant filter,
  dedupe, envelope completeness over the wire).

## Deliberately not here yet

The approval edit-then-approve re-gating path (`edited_payload` answers
422 until it re-enters the gate properly), disqualify/enrich/send
tools (their underlying verbs first), the hosted A2 MCP server
(OAuth2 + PKCE + DCR + the JWS approval-token serialization),
`run_report`/schema-introspection on the SoR seam,
capture connectors, search/context graph, the RLS row-scope backstop
(B-EP03.3b), field-level masking (B-EP03.4), record grants (A52),
consent enforcement, the Idempotency-Key replay
store (unspecified upstream — see `../fable feedback/06`), person/org
merge (promotion is in; the general merge flow is not), event
versioning/replay/dead-letter (B-EP04.12/.14/.15), and the
River job runner (deferred, decisions/0005). The contract routes for all
of these exist and answer 501.

## Working conventions (where findings go)

Building from the spec is also a test **of** the spec, so findings are
routed, not lost:

- **Implementation decisions** — anything the spec left open that this
  code had to decide — get a numbered record in
  [decisions/](decisions/), so a reviewer can separate "the spec says"
  from "we chose".
- **Spec/ticket defects** — a contradiction, an omission, a vocabulary
  gap, an unimplementable acceptance criterion found while building —
  get a numbered markdown file in
  [`feedback/`](feedback/) **plus a row in its
  [README table](feedback/README.md)**, each naming the spec section and a suggested fix.
  These are the input for improving the tickets/spec upstream; when a
  defect forces a local workaround, the feedback file records what was
  applied here so the two can be reconciled later.
- **Session state** — progress, in-flight work, pickup point — goes in
  [STATUS.md](STATUS.md), updated at the end of every working session.

## Engineering rules learned from the review loop

Two external red-team passes ran against this code (2026-07-03 and
2026-07-04; see
[REVIEW-craftsmanship-architecture-redteam-2026-07-04.md](REVIEW-craftsmanship-architecture-redteam-2026-07-04.md)). The
rules below exist because each was violated once here;
they are binding for all future work in this repo (mirrored in
[AGENTS.md](AGENTS.md)):

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
   later, to someone who never saw the review. The history lives in git
   and in `feedback/`, not in the source. (Same for test names:
   name the invariant pinned, not the review that demanded it.)
5. **Don't rationalize a known gap — close it or gate it.** Pass 1's
   dedupe crash-window was answered with a comment arguing it was safe;
   pass 2 showed the argument wrong (the fallback layer prevents double
   effects, not dropped ones). If a design carries a window, either
   restructure so it cannot happen (run-then-mark) or add the failing
   test that documents it honestly.
