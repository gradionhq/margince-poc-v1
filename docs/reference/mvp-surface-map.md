# MVP surface map — every page, what's on it, what's already built

Working reference for the design build-out. Derived 2026-07-30 from three audits:
the spec repo's screen canon, this repo's frontend build state, and the backend's
endpoint readiness.

This file answers "what is the state of this page and what is the job". The
per-screen features and numbered acceptance criteria stay in the spec repo
(`margince-foundation`), which is their source of truth — transcribing them here
would only give them a second, drifting copy.

---

## Read this first: three things that change the plan

**1. The page list already exists, and it is not in `specs/`.** The spec repo holds
`corpus/design/mockups/` — 63 full-fidelity interactive HTML screens plus a shared
token/shell layer, described in-repo as "the design source-of-truth until the React
build replaces it". It went unnoticed because `specs/` was renamed to `corpus/` in
commit `565d1c07`, so every cross-reference to `specs/design/mockups/` now points at
a dead path. There is also a 1279-line normative per-screen acceptance document
recoverable from git history (`git show bf49c8f1^:docs/product/30-screen-acceptance.md`).

**2. This repo is much further along than "a few pages".** 103 files under
`frontend/src/screens/`, 24 routed screens, of which 21 fetch live `/v1` data and
render it. Exactly one screen is a genuine stub (`ai.tsx`, deliberately). There are
no orphan files and no routes without a screen. The shared `QueryGate` / `ListGate`
ladder already gives ~40 screens one loading → error-with-retry → empty treatment,
i18n covers 100 of 103 files, and overlay-mode refusal is handled across every record
surface. **The design job is mostly upgrade, not greenfield.**

**3. Backend coverage is near-total, and the trap is the middle state.** 279 contract
operations, 277 handler-backed. Only 4 are truly dead: `GET /ai/calls`,
`PUT /onboarding/state`, and both `/overlay/flip` operations. But ~53 operations
answer `501` when the deployment did not wire a dependency, which means *whether they
work depends on how the server was started*. On a plain `make dev`: custom-field
creation, password reset, Gmail connectors and site-reads are all 501. Do not read
the spec's subsystem status board — it contradicts the chapters' own front-matter and
lags the build badly in places (it calls projects and the whole HubSpot overlay
"planned" when both ship). Use the endpoint table in
`backend-readiness.md` section 2 instead.

### How the pages sort

| Group | Meaning | Count |
|---|---|---|
| **A — upgrade** | Screen exists, wired to live data. Design work only. | 30 |
| **B — build, backend ready** | Endpoints ship with zero frontend callers. Highest value per hour. | 9 |
| **C — fixture-only** | No backend. Design against fixtures, or defer. | 16 |
| **Excluded** | Fast-follow or retired. Do not design. | 5 |

---

## Start here: the shell

You said you'd start with the shell. It is real and close — the gaps are small and
specific.

### What `frontend/src/app/` implements today

Symbols, not line numbers: line references rot within a day of being written.

**Sidebar** (`WorkspaceRail` in `shell.tsx`, items in `nav.ts`) — a ~250px labeled
sidebar that collapses to the canonical 64px rail, the preference persisted in
`localStorage`. Ten items in a pinned order asserted by `shell.test.tsx` and by
`e2e/ac.spec.ts`, grouped records / work / intelligence: Home, Contacts, Companies,
Leads, Pipeline, Tasks, Approvals, Reports, Automations, Ask Margince. `deals` and
`inbox` are the route ids behind the last two relabelings; no route id changed.
Badges render for Tasks and Approvals only, from live counts. Active state is real
(`aria-current="page"` plus an `active` class off `route.screen`). Below the nav sits
the agent panel. At phone width the same element becomes a bottom bar of four
captioned destinations plus More, which expands it into a sheet.

**Top bar** (`TopBar` in `shell.tsx`) — a breadcrumb that names the record and links
the section back to its list, falling back to the raw id in mono when the name cannot
be resolved. Right side in order: caller-supplied actions, `SorModeChip` (renders
nothing in native mode; in overlay mode an accent badge linking to
`#/settings/overlay`), the search affordance, EN/DE locale toggle, light/dark theme
toggle, and the account menu (which carries sign-out, wired to
`POST /auth/logout`).

**Banners** — `EconomyBanner` (AI budget, only when the band is off-normal) and
`EmbedReindexBanner`, both ops-gated and both deliberately rendering `null` on error so
an advisory probe can never block the shell.

**⌘K palette** (`palette.tsx`) — real search, not a placeholder. Merges builtin
commands, live `GET /search` hits (2-character floor, `useDeferredValue` rather than a
timer), a "see all results" row and an "Ask AI" row. Full keyboard handling. A search
failure degrades to builtin commands rather than breaking the palette.

**Rail-less routes** work: `{onboarding, book, client, preferences}` render an
`app railless` frame, and `App.tsx` has a matching frame for pre-session screens.

### Shell gaps still open

The badge counts, the off-rail titles, theme persistence and the nine-vs-ten nav were
the shell's four gaps and are closed; `draft-adr-app-shell.md` records what was ruled.
Two remain:

1. **The Ask FAB looks interactive and isn't.** `fab.tsx` renders a textarea with no
   `value`/`onChange` and a Send button with no `onClick`. Either wire it or visibly
   disable it — right now it silently does nothing.
2. **The palette's most prominent row leads nowhere.** Choosing "Ask AI" stores the
   query in `sessionStorage` and navigates to `#/ai`, which echoes it back and stops.

And one naming question is still open rather than settled:

**"Approvals" or "Assistant (n)"?** Eric's 2026-07-28 review argues the approval count
belongs in the nav label — "Assistant (12)" — because the autonomy panel is the
differentiator and a visible approval count is what convinces a works council. The
shipped label is **Approvals** with the count as a badge, which names the surface for
an auditor reading a screenshot. Moving the count into the label is an IA change and
needs a ruling, not a quiet edit.

The rail visual spec in `corpus/design/00-design-language.md` still describes it on the
deep ink-green field (gradient `#183028 → #13231D → #0E1B16`, icons and indicator in
white). **That fill is superseded**: the founder ruling of 2026-07-23 makes the product
surface white, and the shipped sidebar is white with the accent carrying the active
state. What survives from that spec is its geometry — a panel inset on the matte ground,
rounded nav rows, a left indicator bar, the M logomark on an emerald `#0B7A53` chip.
`draft-adr-app-shell.md` has the reasoning and the upstream amendment this needs.

---

## Group A — pages that exist and are wired (design upgrade only)

These all fetch live data. The work is visual and interaction quality: match the
Ledger Green system, harden the five states, apply the trust primitives.

| Page | Where it lives | Backend | Design job / caveat |
|---|---|---|---|
| **Onboarding (conversational)** | `onboarding.tsx` + `onboarding-conversation/` (24 files) | `/onboarding/*` live; `PUT /onboarding/state` **dead**; messages need a bound model; site-reads need a crawl runner | Wired end to end with a real state machine, restore-on-reload and derived narration. The mockup `index.html` shows the **classic 5-step stepper, which was deleted** — design against the conversational arc, not the mockup. Open polish: RevealText, orb choreography, reduced-motion audit |
| **Read a company** | `company-context.tsx`, `onboarding-read.tsx` | 501 without a crawl runner | Needs the scrape-unreadable and robots-disallowed → manual-paste fallbacks. Never an error wall |
| **Home / Morning Brief** | `home.tsx` (534 LOC) | `/brief`, `/digest` live | Four independently gated sections, no hardcoded figures, digest 404 renders nothing rather than a fake zero. Needs empty-queue **and** honest-short-week states |
| **Contacts list** | `people.tsx` | live | Per-row provenance chip exists here but not on companies — part of ruling #5 below |
| **Contact 360** | `people.tsx` + `strength`, `consent`, `compose`, `history`, `context`, `relationships`, `customfields.card` | live | Richest composed surface after the org 360 |
| **Companies list** | `organizations.tsx` | live | |
| **Org 360** | `organizations.tsx` (1565 LOC) + `company360.tsx` | `/organizations/{id}/360` live, native-SoR only | The best-built screen in the app, and the only real no-permission UI: a per-section `ready/empty/withheld/unavailable` matrix driven by `sections_omitted`. Use this as the pattern everywhere else |
| **Leads list + Lead 360** | `leads.tsx` (889 LOC) | live | Known defect: rows navigate to `person.html`, contradicting lead segregation (ADR-0008). Fix as part of the pass |
| **Pipeline (board + table)** | `deals.tsx` (1711 LOC), `PipelineBoard` | live | Drag-to-advance is optimistic and snaps back on 409/403/422; terminal stages raise a 🟡 confirm. Won/Lost are drop zones, not standing columns. No 403 UI yet — server 403 surfaces as raw mutation text |
| **Deal 360** | `deals.tsx` `DealScreen` | live | |
| **Tasks** | `tasks.tsx` | `/activities?kind=task` live | Genuinely unavailable in overlay mode (the mirror cannot honour `kind`) — the honest refusal is already there. Charlotte's asks land here: own to-dos, explicit due dates, assigning to a colleague |
| **Approval Inbox** | `inbox.tsx` (726 LOC) | `/approvals` live | **Read the caveat below — the edit-then-approve path has no backend.** Row-level 409 version-skew and already-decided are both surfaced honestly |
| **Reports + forecast** | `reports.tsx`, `quotas.tsx` | `/reports` live; `422 unsupported_by_sor` in overlay | Every report is live server-truth including the "explain this number" derivation drill-down. Nothing hardcoded. The **report builder / dashboard composer / schedule config** are a separate spec gap with no backend (group C) |
| **Quota & attainment** | `quotas.tsx` (in Reports) | `/quotas` live | Attainment ring, pace and numbers all server-computed. Consider promoting to its own route — the canon has it as a screen |
| **Voice profile** | `voice-dna.tsx`, `voice-versions.tsx`, `voice-insights.tsx` | 20 ops live; builds stay `queued` on an unbound model lane | Lives as a Settings card; the canon has it as a screen. Open upstream: structured Voice builder + automatic learning |
| **Settings (12 tabs)** | `settings.tsx` (1401 LOC) | live | No tab is a placeholder. Four "door" link-cards are chrome by design. Tabs: account, voice, ai, company, users, data, catalog, rates, privacy, audit, integrations, overlay |
| **Users & roles** | `users-admin.tsx` | live | Roster read is **first-page only**, `User` carries no per-user role so the control writes a role without showing the current one, and an invite delivers nothing without a mailer |
| **Audit log** | in `settings.tsx` | `/audit-log` live | Keyset infinite query, 6 filters. Charlotte asked for auditor-grade filtering — mostly there |
| **Privacy / consent / DSR queue** | `privacy.tsx` (973 LOC), `consent.tsx` | live | Full state matrix, typed-"ERASE" destructive confirm, legal-hold 409 and stale-transition 422 branches. This already covers the "DSR queue" and "erasure confirmation" the canon lists as missing |
| **Offer builder** | `offers.tsx` (1403 LOC) | live; render/PDF need a blobstore (dev has MinIO) | All money math is server truth. Ungrounded lines must show "set price" placeholders — we don't guess a number. A 501 on render is treated as honest unavailability, not an error |
| **Products / rate card** | `products.tsx` | live | |
| **Offer templates** | `offertemplates.tsx` | live | Only i18n offender: `LOCALE_OPTIONS` labels are literal `"de-DE"`/`"en-US"` |
| **Partners** | `partners.tsx` (627 LOC) | live | |
| **Automations + detail** | `automations.tsx` (520), `automationdetail.tsx` (408) | live | Must stay an honest bounded When/If/Then recipe — no canvas, no branching, no custom-code step (NEVER-2). Run history and dry-run preview are live |
| **Custom fields** | `customfields.tsx` (797 LOC) | **`POST` is 501 on plain `make dev`** (no `--schema-dsn`) | Real columns by migration, so the live DDL preview and structural-refusal banner stay. Strip the stale describe-to-PR routing copy (retired by A39/ADR-0002 Am.1) |
| **Dedupe queue** | `dedupe.tsx` (227 LOC) | live | **Least design-system-conformant screen in the app** — loading, empty and error are plain `<p>` text. Cheapest visible win in the repo |
| **Field history** | `history.tsx` | live | Exists as a record tab; canon has it as a screen too |
| **Integrations** | `connectors.tsx`, `capture-settings.tsx`, `capture-exclusions.tsx`, `webhooks.tsx`, `backfill.tsx` | connectors 501 per-provider without config | Gmail needs `.env.local` credentials or it stays an honest 501 |
| **Share a record** | `share.tsx` (647 LOC) | `/record-grants` live | A 403 `approval_required` maps to honest copy rather than a generic error |
| **Preference center** | `preferences.tsx` | live, public token surface | Anonymous; the token *is* the capability |
| **Public booking** | `book.tsx` | live, session + anonymous | |
| **HubSpot overlay connect + budget** | `overlay.tsx`, `overlay-health.tsx`, `overlay-usermap.tsx` | 501 without a keyvault (dev wires one) | Needs consent-denied, token-refresh-failure, revocation-failure and admin-consent-blocked states, all starting from zero mirror rows |
| **Search results** | `search.tsx` | live | |
| **Client surface (extension)** | `client.tsx` (118 LOC) | only `GET /search` | Thin: unknown-sender empty card, no loading visual. The actual browser extension does not exist — treat the rest as group C |

### Two caveats in group A that are really product decisions

**The Approval Inbox cannot edit-then-approve.** `POST /approvals/{id}/approve` answers
**422** when given an `edited_payload` — the re-gating path is deliberately not built.
But the spec names the Approval Inbox the canonical 🟡 surface and says its Edit editor
must be designed *first* because every other screen references it. So the single most
load-bearing interaction in the product has no backend. Decide: design the editor and
raise the contract change upstream, or scope V1 to approve/reject only and say so.

**`aicalls.tsx` is built against a dead endpoint.** The AI-observability list calls
`GET /ai/calls`, which has no handler at all (the detail read `/ai/calls/{id}` works).
That screen will 501 in any deployment. Concrete bug, not a design question.

---

## Group B — backend ships, nothing consumes it

STATUS.md states the standing check itself: *"a handler-backed, routed endpoint with
zero frontend callers is not done, and the gap is invisible from the green gates."*
These are that gap. Highest value per hour of design work, because the data is already
there.

| Page to build | Endpoints ready | Notes |
|---|---|---|
| **Projects: list, detail, link-review queue** | 9 ops — `GET/POST /projects`, `GET/PATCH/DELETE /projects/{id}`, `POST /projects/{id}/advance`, `GET/PUT/DELETE /projects/{id}/stakeholders` | **Top priority.** Eric's biggest gap ("the German mid-market revenue driver"), E21 has full ACs, the backend ships in the `deals` module — and the spec board still calls it "planned". No screen IDs exist, so this is genuine net-new design. Not Gantt or Jira: relationship signals only (response-time decay, new procurement contact, postponed meetings, contact left, contract end approaching, dates slipped), sources opt-in and always cited |
| **Filters, saved views, lists & tags** | 15 ops — `/lists`, `/lists/{id}/members`, `/tags`, `/tags/{id}/apply`, `/views` | Zero UI today. Also the substrate under the E09 saved-view management gap. Known limit: lists and views are not custom-field aware (CF-T05) |
| **Attachments on records** | 7 ops; blobstore wired in dev | **Design the `scanning` state first — in dev it is the default, not the exception.** No scanner product is integrated anywhere, so a fresh upload stays `scanning` and `GET /attachments/{id}` answers 409 until someone drives `MarkScanResult` by hand. Also needs the blocked-download and request-access states |
| **Filtered export** | `POST /exports` | No UI. Pairs naturally with saved views |
| **Activation / "watch it fill itself"** | `POST /coldstart`, `POST /coldstart/preview` (501 without a bound model) | The canon's `activation.html`. Progressive streaming, ≤8s p95, the user watches it read — never spinner-then-dump |
| **Signals / warm room beyond the org 360** | 8 ops incl. `/signals/{id}/intro-path`, `/warmth`, `/resolve` | Only `company360.tsx`'s `SignalsCard` consumes any of this. Consent-gated and company-level only (NEVER-8, the Pat guard) |
| **Account hierarchy view** | `GET /organizations/{id}/hierarchy-rollup` | Rendered inside the org 360 today; the canon has it as its own screen |
| **Personal profile & personal security** | `/me`, `/passports`, `/auth/*` | Settings → account has a minimal `IdentityCard`. The canon wants member-facing profile and security sections |
| **Localization admin** | locale toggle + full i18n catalogs exist | Thin, but DACH matters: Eric's review insists on German throughout (`2,04 Mio. €`, `1.340`, `06:52 Uhr`, `23.07.2026`) because an English demo undercuts the sovereignty positioning. DE is already the default catalog |

---

## Group C — no backend; fixture-only or defer

Design these against fixtures if the demo needs them, but nothing will light up. Each
line names why.

| Page | Why there's nothing behind it |
|---|---|
| **Ask AI hub** (`ai.tsx`) | No chat operation in the contract at all. The screen deliberately "never pretends a chat backend exists". When you do design it, follow the redline: one hub, three zones — *Ask Margince* / *Connect your own agent* (not a chat box) / *Working on its own*. It is **"Ask Margince"**, never "your Claude agent" (the agent loop lives in the user's own client, ADR-0005) |
| **Deal room** (buyer-facing) | No module, no endpoints, nothing in the contract. Copy is pinned verbatim in the spec ("Sent, awaiting your decision" / "Offer accepted. Thank you, Anna." / "Change requested" / "Tracking on"). Asked for by both Joshua and Niraj |
| **Sequences / cadence** | The outbound sequence engine does not exist. Deliverability does |
| **Import + rollback** | No importer, no migration engine, no import endpoints. So no wizard, no mapping screen, no rollback |
| **Bulk actions** | Bulk operations unbuilt. Note UC-UX-04: there is **no universal toast undo** — reversibility is exactly three things, batch-undo on bulk ops, archive/restore on records, import rollback |
| **Field-level security / masking** | Unbuilt |
| **SCIM provisioning** | Unbuilt |
| **Data quality per record** | Quality score unbuilt |
| **Formula fields** | No formula operations in the contract |
| **Sandbox workspace** | No backend |
| **Security screen** (SSO/MFA/sessions) | The OIDC redirect flow is not in the contract; `startFederatedSignIn` is an intentional no-op. Password reset needs a mailer. Deferred by decision: reset, all OIDC, SAML, passkeys, MFA, JIT provisioning, account linking, workspace chooser |
| **License & seats** | Licensing lives in the separate `margince-constellation` repo |
| **Operator console** | No backend. Eric's CTO ask (model/version/connection table, deliberately-restricted vs failed, one connection off so all-green means something) partly overlaps the live overlay + AI usage surfaces — consider reshaping those instead of a new screen |
| **Public inbound lead form** | No form-submission operation has a module home (CS-GAP-2). The public-surface *pattern* is proven by booking and preferences |
| **Dispact cross-link** | `conversation_link` is write-only; no read, resolution or events |
| **Mode flip** (overlay → SoR) | Both flip operations are **unconditional 501s**. Fixture-only. Requires typing "FLIP TO SOR" against a 4-condition gate |
| **Notification center + delivery prefs** | No notification-center persistence. The approval gate is built, the notification centre is not |
| **Trust pack / GoBD** (Germany V0.5) | No endpoints. `extensions/de` ships GoBD calendar-year retention floors. `gobd.html` has a "try to delete a retained record" card that demos the denial |
| **Profiler, weekly report, asset library, LinkedIn draft, coaching, coverage** (Tier 3 AI-native) | Transcript, dossier and profiler are unbuilt; `transcript` is one of four AI tasks with no production call site. `linkedin-draft.html` deliberately has **no send control** — only "Copy text" and "I sent this on LinkedIn". Drafting and voice endpoints exist and could partly back it |
| **Report builder / dashboard composer / schedule config** | Spec gap F3, no backend. Saved views (group B) are the closest live substrate |

---

## Excluded — do not design

- **`telephony.html`** — S-E15.6 retired outright, 2026-07-14, A104.
- **`e-invoice.html`, `e-signature.html`** — removed from V1 *and* V0.5 to backlog under A94.
- **`salesforce-connect.html`, `dynamics-connect.html`** — all six Salesforce and all six
  Dynamics overlay stories are explicitly fast-follow, not V1.

Mockup HTML exists for all five. Its presence is not scope.

Also out of scope by structural rejection (the "NEVER" list), so don't design around
them even if asked: dynamic-schema interpreter, no-code visual workflow builder or rules
DSL, hard delete (archive *is* the delete), per-AI-seat pricing or credit meters,
live-call surfaces and any biometric/emotion inference, covert profiling of external
prospects, app/API marketplace.

---

## Five cross-cutting rulings to make before the per-page work

The spec asks for these explicitly and wants **one** answer each, not per-screen
handling. Everything downstream depends on them, so do these in one sitting.

1. **One confidence vocabulary.** Band vs dot vs percent are all in use. Pick one.
2. **One "explain this number" mechanic.** "Explain this number", "show the evidence"
   and "how is this computed" are three names for one interaction across reports, home,
   person, company and client surfaces. Unify the component.
3. **The yellow staging editor.** Currently specified as toasts, i.e. undefined. The
   Approval Inbox is the canonical yellow surface, so its Edit editor and per-row
   approve/reject must be designed first — and see the 422 caveat above.
4. **List-row provenance.** Contacts carries a per-row source chip, companies doesn't.
   Decide globally.
5. **Which controls actually recompute.** In the prototype, report scope/period, task
   scope and every sort/filter are toasts. In this repo most are real. Pin which controls
   hit the server.

Plus one open item with no owner: STATUS.md asks for a **record-360 panel audit
converging on one shared overlay-unavailable affordance** instead of per-panel error
states. That is a design task.

---

## Per-page checklist

Apply to every page, group A included. These are gated, not advisory.

**All five states must ship as real rendered states, not toasts** (STATE-1..5):
empty (honest, no fabricated counts, says what and why), loading (skeleton or
progressive; chrome renders immediately and content streams in), error (honest failure
card with cause and retry; one panel failing never blanks the screen), no-permission
(denied content **absent from the payload**, not merely UI-hidden; the control omitted,
not a dead button), and nothing-grounded (omitted or "not found", never fabricated).
`company360.tsx` is the reference implementation.

**The six trust primitives, where they apply:**
- *Evidence-or-omit* — every AI value carries an evidence chip expanding to a verbatim
  mono snippet plus source link. No evidence means **the value is not shown**. A blank
  field is honest; a guessed field is a defect.
- *Confidence-as-glyph* — styled dot, never emoji. Low confidence is shown **as** low.
- *Staging-before-persist* — staged / real / human-typed are three visually unmistakable
  styles, asserted by a cross-layer test.
- *Accept / Edit / Dismiss* — the universal triad on any AI proposal. Edit flips
  provenance to human-typed while **retaining** the original snippet.
- *Provenance tag* — quiet, agent- vs human-authored, never competing with content.
- *AI-assisted disclosure* — EU AI Act Art. 50, mandatory on any generative surface. A
  missing one is a failing test.

**Token discipline** (all gated, a violation is a build failure not a review nit):
- The contrast law — small text 11–13px **must** use `textMeta #5E6C65` (dark `#8FA099`).
  `textSecondary #68756E` is reserved for ≥16px. `textTertiary #9AA6A0` is decorative
  non-text only.
- Exactly three font families: Outfit (display only, weight 500–600, never 700),
  DM Sans (everything), JetBrains Mono (evidence, IDs, eyebrows).
- Elevation is hairlines, not shadows. Flat fills only; the rail gradient is the one
  exception.
- Lucide icons only. The only sanctioned emoji anywhere are the autonomy-tier glyphs
  🟢 auto-execute and 🟡 confirm-first.
- Brand green is deep (`#0B7A53`), success green is bright (`#22c55e`) — kept tonally
  distinct so "green = brand" never collides with "green = high confidence".

**Use the canonical fixture data** from `corpus/design/seed-fixtures.md`, or the
on-screen arithmetic will contradict itself between screens: Anna Weber, VP Operations at
Brandt Automotive, relationship strength 86 = 100 × recency 0.91 × frequency 0.97 ×
reciprocity 0.97, Champion on a single-threaded deal; Brandt org strength 78 = MAX over
contacts, normalised after decay.

**Verify with** `make check-fe` (biome + vitest + tsc + build). Remember the api binary
does **not** hot-reload — any backend change needs `make dev` again, and a stale binary on
:8080 is indistinguishable from a broken feature.

---

## Suggested order

1. **Shell** — the chrome has landed, which is not the same as fully accepted: the
   two gaps above are open, the agent panel's activity/routing/spend lines have no
   endpoint behind them yet (AC-shell-1j), and the Approvals-vs-Assistant naming
   call is unmade.
2. **The five cross-cutting rulings** as ADRs.
3. **Approval Inbox / Assistant** — the canonical yellow surface; defines the trust
   language every other screen reuses.
4. **Core spine upgrade pass** — home → contacts/person → companies/company →
   pipeline/deal → leads → tasks → reports. Cheap wins alongside: `dedupe.tsx`'s plain-text
   states, off-rail titles, the leads→person defect.
5. **Group B, Projects first** — the data is already there and it's the loudest gap in
   the feedback.
6. **Group B remainder** — saved views/lists/tags, attachments (scanning state first),
   exports, activation.
7. **Group C only as the demo needs it**, Ask AI hub first since ⌘K and the FAB both
   point at it.
8. **Mobile** — the 390px approval path is V1-required and completely unrepresented in
   the prototype. Do it after the Assistant is stable.

## Sources

- Per-screen features and numbered ACs: the spec repo's own chapters (see below)
- Frontend build state, file by file: the screen-state audit in the session scratchpad
- Backend endpoint readiness: the readiness map in the session scratchpad
- Spec canon: `margince-foundation` → `corpus/design/` (mockups, design system,
  `00-design-language.md`, `seed-fixtures.md`), `specs/architecture/web-design-system.md`,
  `specs/quality/acceptance-standards.md`, `specs/design/concepts/` (auth)
