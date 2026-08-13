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

- **Opening a settings page is a READ, and the eleven entries now ask like
  it.** Each entry's predicate was a WRITE grant, because each was written to
  answer "can you *use* this" — and measured against the live API a read-only
  seat was hidden from eight of eleven entries the server answers 200 on,
  including three surfaces that were ungated routes of their own before the
  merge. One rule now covers all of them: the entry opens if the principal may
  read any part of it, and the write affordances inside say for themselves who
  may use them. A read grant is still a real question rather than a formality —
  a role edited to drop `custom_field:read` loses the Data model row, which
  `true` could never express. The consequence, stated: on a freshly seeded
  installation every role reaches eleven of the twelve entries and only
  Maintenance narrows, so what distinguishes seats has moved inside the pages,
  where cards state their own denials.
- **Connections split in two, and the entry with no predicate disappeared with
  it.** One Organization row used to hold both a reader's own mailbox and
  LinkedIn network *and* the installation's contact-data credential, webhooks and
  HubSpot mirror — which is exactly why it could not be gated: any honest
  predicate took a personal task away from whoever it hid it from. *You →
  Connections* now holds the three per-user surfaces (every one reads a `/me` or
  caller-scoped seam) and *Organization → Integrations* the four
  installation-wide ones, each with a predicate of its own. The seam was never a
  missing group; it was one entry belonging to both. The connector OAuth callback
  keeps its `#/settings/connections` return route, because the connectors are the
  half that stayed; the system-of-record chip follows the mirror to
  `#/settings/integrations`.
- **The member roster is no longer admin-only, and People opens on it.**
  `GET /users` answers 200 to any authenticated principal, and who is on the team
  with what role is not an admin's private question — so every seat reads the
  list, while inviting somebody and changing a role or status stay the admin's
  and withhold themselves. A role nobody may change reads as text rather than as
  a picker that could only be refused. The page also used to open on an empty
  invite form: three blank fields ahead of the answer, for a task most visits are
  not about.
- **Three settings pages were ordered against the reader.** AI opened on spend
  and buried the automations that *cause* it four screens down, past a price
  table — and for manager, rep and read_only the two spend cards are withheld
  anyway, so the page opened on a price sheet; it now reads automations → spend →
  prices → the per-call trace last, that trace being a debugging instrument
  rather than a setting. General claimed in a comment that the base currency and
  its rate sheet were adjacent while the company profile sat between them; they
  are adjacent now. Four card titles stopped repeating the heading above them
  ("You" inside *You*, "Organization" inside *Organization*, "Capture" on
  Capture), "Your agents" dropped a possessive the group heading already carried,
  and "Voice" became "Writing voice" — in a product with mail capture, a bare
  "Voice" reads as call recording.
- **Settings had 24 doors; it has 11 entries.**

### Fixed

- **Absent, disabled and withheld are now decided by CAUSE, and it is written
  down** (`frontend/src/design-system/README.md`). A surface a permission denies
  says so; a precondition the reader could fix disables the control and states
  what would make it live; only "does not apply" is absent. Two cards returned
  `null` on a denial while their neighbours on the same page explained
  themselves — so for an ops seat the Privacy page showed the consent registry
  working, retention silently gone and the subject queue saying why. An absent
  retention card does not read as "not yours"; it reads as "this installation
  keeps nothing". Both now state the denial and still ask the server for nothing,
  because the answer was already known. The installation settings card disabled
  three fields with no reason at all, under a comment claiming parity with the
  card that does it right.
- **The scroll position survived a route change.** The document never scrolls
  here — the content column does, and it is the same element on every route, so
  it carried the last page's offset into the next one. Reading a scrolled AI
  settings page and opening Connections landed the reader partway down Webhooks.
- **No skip link existed anywhere** (WCAG 2.4.1). Every page put the brand,
  search, up to twelve navigation rows, More, the settings door and the account
  menu ahead of the content, and a keyboard reader walked all of it again on
  every page. It is a button rather than an `<a href="#content">` because the app
  is hash-routed and a fragment link would navigate.
- **Entering the settings level dropped focus to `<body>`.** The walk out of a
  section was pinned and the walk in was not — the guard that was supposed to arm
  it fires only for a row with children, and nothing in the tree gives a row
  children. Two comments claimed both directions were covered.
- **A modal returned focus to whatever opened it, even after the mutation
  removed that element** — passport revoke, member deactivate, connection end and
  DSR transition all landed on `<body>`. `Modal` now takes a `returnFocusTo`
  resolver evaluated at restore time, and checks the opener is still connected
  before reaching for it.
- **Every card's load failure was announced to nobody**: `QueryStates`' error
  branch carried no live region and its skeleton no busy state or spoken name.
  One fix covers most surfaces in the product, plus the shared create/edit and
  archive-confirm errors.
- **Two WCAG Level A defects on the AI attempt trail and the tool console.** A
  row's expand affordance was a `<tr onClick>`, unreachable by keyboard; the tool
  console dimmed unreachable rows to `opacity: 0.4`, taking the "scope not
  granted" caption that is meant to be the text equivalent below the AA contrast
  floor — while the passport list 150 lines above it had chosen a strikethrough
  over dimming for exactly that reason. Every credit-pool meter was also
  announced as an anonymous number triple (`label=""`).
- **Heading navigation landed on sections with nothing inside them.** Pipeline
  names, provider names and block titles were styled spans and divs; two dialogs
  opened with a raw `<h3>` under the page's `h1`.
- **Passport scope checkboxes rendered the raw protocol words** (`read`,
  `draft`, `write`, `send`, `enrich`), untranslated, on the page where a human
  decides what to lend an agent — where "write" and "send" read as near-synonyms
  until one of them names the mailbox.
- **Twenty-one German strings addressed the reader as *Sie*** against `de.ts`'s
  own mandated informal *du*, every one of them on a Connections surface — the
  register flipped exactly where a rep does their own personal setup.
- **Retention wrote on flip through a `Checkbox`.** The design system reserves
  `Switch` for a control that *is* the action; the flip also shared Save's
  mutation, so pausing a policy collapsed the edit panel — discarding the panel,
  the focus, and the operator's place — under a comment claiming it existed so
  they would not have to save mid-edit.
- **The 390px and axe sweeps covered two settings pages of twelve**, both of
  them short ones, so the widest page in the product went unmeasured. Data model,
  Integrations and People are swept now.
- **`#/settings/capture` told the reader to change company domains elsewhere
  and gave them no way there**, on a card whose whole point is that it cannot be
  edited in place. Fifteen tabs plus nine
  routes outside them collapsed into eleven, in two groups: Account,
  Voice and Your agents under *You*; General, People & access,
  Connections, Capture, Data model, AI, Privacy & audit and Maintenance
  under *Organization*. What merged, merged because it was one subject
  all along — the installation and the company profile describe the same
  organization, currency rates belong beside the base currency they
  convert to while model prices belong beside the runtime they price, and
  two different surfaces were both called "Capture". Four screens that
  were reached only by a card whose job was to send you there are now
  content on the page that owns them (`#/custom-fields`, `#/products`,
  `#/offer-templates` and `#/automations` are gone as routes;
  Automations' primary-nav slot went to the dedupe queue, which had no
  address of its own outside a home digest card). `#/design` is deleted.
  `GET /admin/job-health` shipped with no UI at all and now has one, on
  Maintenance beside the search-index rebuild and the danger zone. **No
  seat lost a surface it could use**: a merged entry opens on the union of
  what its parts asked for, never the intersection, and the parts that
  are narrower than their page gate themselves inside it. The connector
  OAuth callback's return route moved with the page it lands on: the
  server redirects to `#/settings/connections`, which is a hard-coded
  string in `internal/compose/connectors_outcome.go` and the one part of
  this change that a rename alone would have broken silently.
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
