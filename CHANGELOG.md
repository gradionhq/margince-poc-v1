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
- **Governed agent surface**: Agent Seat Passports, the remote MCP
  connector at `/mcp` (OAuth 2.1 + PKCE, dynamic client registration,
  refresh rotation; the A1 stdio server is retired, SCR-9), the 🟢/🟡
  autonomy tiers enforced below the transport on MCP and REST alike, and
  the approval engine (stage → human decision → single-use redemption).
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
- **AI surfaces**: model routing (`ai-routing.yaml` — BYOK cloud via the
  native Anthropic / OpenAI / Gemini adapters or the generic
  `openai_compatible` wire, local Ollama / vLLM, an offline fake), the
  Surface-B runner + scheduler, search (FTS + pgvector hybrid), capture
  connector seam, cold-start read-back.
- **Model certification** (ADR-0074): a hand-authored fixture corpus
  driven through each site's own production request builder and
  validator, scored by a pinned judge and committed as a JSON record —
  plus `make ai-probe`, which runs one site against operator-supplied
  input through the same case.
- **The job contract** (`api/jobs.yaml`): every River job kind is declared
  before it is written — a kind not in the file does not compile, and a
  kind with no chosen timeout fails generation rather than running on
  River's silent default. Each kind declares its role: a *dispatcher*
  enumerates the fleet and enqueues, a *workspace worker* does one
  tenant's work and fails its own job row. Job args name rows and never
  carry content, so Art. 17 erasure reaches an in-flight job through the
  row it names.
- **Fleet observability** (OPS-MET-2): `/metrics` carries the job-runtime
  section, `GET /v1/admin/job-health` is the same table read for an admin
  (human-session-only, workspace-scoped, failure reasons drawn from a
  vetted vocabulary rather than the raw error column), and
  `cmd/worker --observe-addr` gives the worker its own `/healthz`,
  `/readyz` and `/metrics` — which *process* is wedged is a question the
  fleet-wide gauges structurally cannot answer.
- **Messaging channels**: a workspace-level Telegram bot binding
  (`/channel-connections`) with pull ingress — the installation
  long-polls, so it needs no public address and no inbound route — and a
  governed reply (`POST /activities/{id}/send-message`) whose recipient
  is the channel identity of the person the conversation is with, never
  named by the caller.
- **Outbound mail**: a Gmail send surface bound to the same consent as
  capture, staged as a durable `comms_outbound` row, re-checked against
  the staging human's live seat at transmit time, consent-gated, and
  keyed on the identity the provider stamped rather than the one we asked
  for — Gmail rewrites `Message-ID`, and bookkeeping keyed on the id we
  requested loses the receipt.
- **The relationship graph**: activity participants projected into an
  interaction edge, answering who on our team already knows a contact —
  with per-user warmth (never summed into a workspace score), deal
  coverage and its risk rules, and a LinkedIn `Connections.csv` import
  whose rows are graph substrate: invisible to search, lists, people
  screens and agent record tools, and private to their owner.
- **Company record page**: one gated 360 read behind the whole page, a
  per-viewer account brief that degrades to a deterministic summary with
  no model lane, Ask, record-derived next-step suggestions that each
  carry their why, a dwell-gated visit baseline, and the one-hop
  connections graph.
- **Overlay mode (HubSpot as system of record)** and the **ADR-0071
  overlay→native cutover**: mirror-backed reads behind the frozen
  datasource seam, incumbent-first write-back on `Update`/`Archive`, a
  conjunctive preflight that seals the mirror when green, and a
  confirm-first flip behind a typed phrase. Both flip operations are
  human-only — a one-way, estate-wide cutover must not collapse to a
  single approval click. Reversibility is reconstruction from the
  pre-flip bundle, not rollback.
- **Supply chain**: three source-tree SBOMs (CycloneDX + SPDX 2.2.1 +
  SPDX 3.0) generated from a clean export of HEAD, normalized to one file
  set and parity-gated, license-gated against an explicit allowlist, and
  keyless-signed on `main` from a job isolated from all PR-controlled
  code — a keyless signature lands permanently in a public transparency
  log and cannot be retracted.
- **Self-hosting materials**: deployment-target-agnostic
  `Dockerfile.{api,web,worker}` (all non-root), the entrypoints, the
  one-time `db-bootstrap.sql` that creates the two non-superuser roles
  `FORCE ROW LEVEL SECURITY` requires, and a runbook
  ([docs/deployment.md](docs/deployment.md)).
- **Non-production data reset**: `POST /v1/admin/reset-data` wipes
  workspace data back to the bootstrapped state behind a four-gate chain
  (non-production posture → human-only → `admin` role → typed
  organization-name confirmation). In production the operation does not
  exist: the posture check runs before auth, so it 404s rather than 403s.
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
- **The installation's own company is not one of its prospects**
  (ADR-0082/A127). The anchor organization is excluded by default from the
  surfaces that answer "which companies are we selling to" — the
  organization list, lexical and vector search, dynamic segments and their
  exports, duplicate detection, and signal candidate resolution — with
  `include_anchor` as the opt-in on the NATIVE list, shaped like
  `include_archived`; an overlay-mode list refuses it with 422, because the
  mirror holds the incumbent's accounts and the anchor is a native row that
  is never among them. It stays reachable by id everywhere, and stays
  deliberately available where naming it is the point, such as recording
  that a person works there. `is_anchor` is on the wire so a client can
  tell it apart, and the governed agent surface learns the id through its
  company context, since the company operation is human-only. Archiving it
  or merging in either direction is refused in the schema as well as in the
  service: losing the anchor makes the company read answer not-found, and
  the application reads that as a workspace that was never configured.
- **An administrator can see and change the workspace's own email
  domains**: `GET`, `POST` and `DELETE` on `/capture/email-domains`,
  admin/ops and human-only, since the set decides which mail the
  installation may hold at all. Adding a domain IS the human vouching for
  it, so it stores verified and takes effect on the next message; adding
  one a connected mailbox already contributed confirms that candidate
  rather than failing. The list reports what the company profile claims
  separately from the registry, because only the second is editable here.
  The screen states what removal does and does not undo: capture resumes
  from that point on, and mail skipped meanwhile is never offered again by
  any mailbox.

### Changed

- **Every dropdown in the product is the design system's, not the
  browser's.** `Select` is a button trigger plus a portalled listbox with
  the full keyboard contract (arrows, Home/End, typeahead, Escape without
  committing, focus back to the trigger), so the option list takes the
  product's tokens instead of the platform's — including inside a
  scrolling toolbar, where it anchors to the trigger and flips when the
  room below runs out. Callers pass `options` and receive the VALUE in
  `onChange`; a `<select>`, `<option>` or `<optgroup>` anywhere under
  `frontend/src` outside that one component now fails
  `make frontend-check`. `frontend/src/design-system/README.md` is the
  catalog to read before hand-rolling any control.
- **The sign-in screen is two halves of a page, not a card in a pane.** The
  identity region runs full-bleed and is divided from the form by one
  hairline; the wordmark sits in the page's top-left corner on the split
  layout and above the form when the layout stacks; each half reads down its
  own centre line, and both carry the same inset at every width above the
  phone. The form is a single 400px measure — heading, provider buttons
  (stacked full width, so every way in presents the same target), fields,
  locale row and fine print — with the fields the one thing that stays left,
  because a label centred over a line of typing points at nothing. On phones
  (≤560px) the identity region drops **entirely**: the sphere, the limits and
  the AI's own sentence with them, because the form is the only thing that
  screen is for. That leaves the phone surface disclosing nothing about the
  AI behind the installation, which is a deliberate departure from
  ADR-0076 Decision 1 at that width, tracked as issue #562; every wider
  layout still makes the disclosure in full.
- **The sign-in screen's entry animation belongs to the page load.** The
  staggered fades and the typed statement ran again on every React remount
  of the surface, which reads as the page reloading under the reader. The
  choreography is now marked spent on the document once it has run its
  course, so a remount renders the surface already arrived while a real
  page load still gets the introduction.
- **The Core holds its position.** The sphere's 11-second vertical drift is
  gone everywhere; it still breathes, and the beat is still what carries
  its state.
- **The Core goes still while the window does not have focus.** Both halves
  of it — the WebGL liquid and the CSS rhythms (breath, sheen, halo, feed) —
  stop off one document-level `focus`/`blur` signal and resume with it, so
  a Core behind another window costs nothing and the sphere and its glow
  can never disagree about whether it is moving.
- **The passport cap an operation spends comes from the contract.** Every
  `x-mcp-tool` annotation now declares its `scope` alongside its tier, and
  the REST agent gate spends that declared cap instead of a hardcoded
  `write`. Scopes are exact membership, so `write` does not imply `send`
  or `enrich`. A passport minted with `read`+`write` and no
  `enrich`/`send` is now **refused** `POST /organizations/{id}/enrich`,
  `/deep-read`, `/coldstart`, `POST /offers/{id}/send` and
  `POST /overlay/reconcile` with `scope_exceeded`, where before it was
  admitted. Re-mint with the caps you mean to grant; the per-tool cap is
  tabled in [docs/reference/agent-tools.md](docs/reference/agent-tools.md).
- **Connected agents are listed apart from passports you minted.**
  `GET /v1/passports` carries a `connection` object on grant-issued rows
  and groups the list one row per connection rather than one per
  credential, so a connection appears once however many times its
  credential has rotated. Revoking a grant-bound passport ends the whole
  connection, not just the current credential.
- **Language and theme moved into the account menu.** The top bar carried
  an icon button for each beside the avatar; both are this person's
  preferences rather than screen actions, so the bar is down to search and
  one account affordance, and the menu reads Settings · Language · Theme ·
  Sign out with the two preferences stating what they are set to. Changing
  one keeps the menu open, so the theme you pick is visible from the
  control that picked it, and dismissing the menu hands focus back to the
  avatar. The language row is the three-locale menu itself, nested: one
  Escape closes the language list, the next closes the account menu.
- **One orb in the product.** The agent panel at the sidebar foot drew a
  CSS lookalike of the Core because the real primitive held a render loop
  for the whole session. The Core now costs what it displays — it draws at
  the size it is shown at, spends its 24fps budget on a timer instead of a
  callback per display refresh, and stops entirely on a hidden tab or an
  off-screen canvas — so the shell shows the same sphere as sign-in and
  onboarding, and the duplicate is gone.
- **AI model routing is now per-engineer**: the working dev config moved
  from a committed `backend/ai-routing.yaml` to a gitignored
  `config/ai-routing.yaml`, seeded from `config/ai-routing.example.yaml`
  by `make install` / `make dev`. Engineers bind their own local models
  without touching a committed file; the annotated template stays the
  parse-guarded source of truth.

[Unreleased]: https://github.com/gradionhq/margince
