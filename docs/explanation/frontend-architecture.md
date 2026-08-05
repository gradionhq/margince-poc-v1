# Frontend architecture — how the web app is put together

The orientation page for anyone touching `frontend/`. The app's conventions are
real and enforced, but most of them are currently discoverable only by reading
source: this page states them once. [`frontend/README.md`](../../frontend/README.md)
is the command sheet (what to run, which switches exist); this is the *why*
behind the structure, the same role [architecture.md](architecture.md) plays for
the Go tree.

## What the app is

A standalone static Vite/React build. `pnpm build` produces a `dist/` served
**separately** from the API binary, which serves `/v1` only and embeds no SPA —
how `dist/` is hosted (static server, CDN, reverse proxy) is a deployment
choice, not baked into the build.

It is a **plain client of the same `/v1` contract as everything else** — there
is no privileged path, no backdoor, no frontend-only endpoint (ADR-0013, the
same rule that makes the agent surface a client rather than an insider). Three
consequences are load-bearing:

- **One API seam.** `src/api/client.ts` is the only place a request is
  constructed: same-origin `location.origin + "/v1"`, `credentials: "include"`
  for the session cookie, and the global `fetch` resolved per call so test stubs
  and the service worker can intercept. Nothing else calls `fetch` against the
  API.
- **No tenant selector on the wire.** One installation serves one organization
  (A107/ADR-0061) and the server resolves it itself. The client sends the
  session cookie and nothing else — `auth.test.tsx` and `preferences.test.tsx`
  assert the absence of a workspace header, so re-introducing one fails the
  build.
- **Generated types, gated.** `src/api/schema.d.ts` and
  `src/api/public-events.ts` are generated from `backend/api/crm.yaml` and
  `backend/api/public-events.yaml` (`pnpm gen:api`). Never hand-edit them;
  `make frontend-check` regenerates and diffs, so a contract change that skipped
  regeneration fails rather than silently stranding the frontend types.

Routing is **hash-based** (`#/deals/01J9ZK` → `{ screen: "deals", id: "01J9ZK" }`)
precisely so any static host serves `index.html` for every entry point with no
server-side SPA fallback. A hash may carry a query of its own; the parser strips
it, so a `?utm=…` never leaks into a screen name.

## The layers

```text
 src/design-system/     tokens.css → brand.css → base.css
                        → atoms → trust → the Core primitive → composed
 src/app/               shell + rail + top bar, hash router, ⌘K palette,
                        Ask FAB, theme, capability, banners
 src/screens/           one file per surface (a directory when the surface
                        is a state machine)
 src/i18n/  src/format/ the presentation edge: copy, money, dates, zones
 src/api/               the one seam + the generated contract types
```

- **`src/design-system/`** — the shared vocabulary, in dependency order.
  `tokens.css` is the canonical Ledger-Green palette (ADR-0040), mirrored
  verbatim from the design source of truth and pinned value-by-value by
  `tokens.test.ts`. `brand.css` is the DERIVED layer: every value there is a
  `color-mix()` of a canonical token, never a new hex. Then `atoms.tsx` (Button,
  Badge, Avatar, Card, DataTable, Modal, …), `trust.tsx` (the trust vocabulary
  of §4 of the design language — `design/00-design-language.md` in the spec
  repo, which the section numbers on this page all refer to:
  `AutonomyDot`, `EvidenceChip`, `ConfidenceMeter`, `ProvenanceTag`,
  `StagingCard`, `ApprovalGate`, `StagedProposal`, `FieldDiff`), the Margince
  Core (`margince-core*`), and `composed.tsx`, which builds on both
  (`RecordView`, `PipelineBoard`, `GroupedTimelineList`, …). `motion.ts` holds
  the reduced-motion rule — reduced motion jumps to the END state, never to
  nothing. `conformance.test.ts` is the drift gate over the whole tree.
- **`src/app/`** — the application chrome and the things that are true on every
  screen: `shell.tsx` (rail + top bar), `nav.ts` (the canonical destination
  list), `router.tsx` (hash routing), `palette.tsx` (⌘K), `fab.tsx` (the
  record-aware Ask panel), `theme.ts`, `capability.ts`, and the shell-level
  advisories (`economybanner.tsx`, `embedreindexbanner.tsx`,
  `resumeconnectbanner.tsx`).
- **`src/screens/`** — **one file per surface.** A surface earns a *directory*
  only when it is a state machine rather than a page, and exactly one has:
  `screens/onboarding-conversation/`, where the conversation machine, its acts,
  its scenes and its restore logic each need their own file. Everything else —
  including surfaces as large as `organizations.tsx` and `deals.tsx` — stays a
  file with co-located `*.test.tsx` and `*.stories.tsx`. A route with no screen
  behind it renders the honest pending state (`App.tsx`'s `PendingScreen`),
  never a blank page.
- **`src/i18n/`** — DE + EN catalogs with key parity enforced twice: `MessageKey`
  is `keyof typeof en` and the German catalog is `satisfies`-checked against it,
  so a missing key fails `tsc`; `i18n.test.ts` re-checks at runtime so a build
  that skipped typechecking still fails loudly.
  Resolution order is the explicit choice → the browser's languages → `en`
  (A100: unconfigured English is `en-GB`, not `en-US`). Locale is presentation
  only: it never participates in storage or math.
- **`src/format/`** — the presentation edge. Money arrives as integer minor
  units + ISO currency and is only *scaled* for display; zones are IANA names
  and a fixed offset is rejected loudly at the edge. No FX math, no calendar
  arithmetic, no locale flowing back into storage.

## The shell

`src/app/nav.ts` holds the canonical navigation and `shell.test.tsx` pins its
order. It is **ten items**: Home standing alone, then three labeled groups.

| Group | Route ids |
|---|---|
| *(ungrouped)* | `home` |
| Records | `contacts`, `companies`, `leads` |
| Work | `deals`, `tasks`, `inbox` |
| Intelligence | `reports`, `automations`, `ai` |

The groups are the **expanded sidebar's** own structure. Collapsed, each group
heading keeps its box and draws a hairline in the same space, so the 64px rail
is the flat ten-item list the design language specifies — the expanded state is
additive rather than a replacement. Collapse is a persisted preference
(`margince.sidebarCollapsed` in `localStorage`, read once at mount); the column
animates between 250px and 64px on one shared `--shellAnim` (0.36s), and a
`SETTLE_MS` of 420ms suppresses hover reveal until the width settles.

At **≤700px** the same `<nav>` element becomes a fixed bottom bar. `MOBILE_PRIMARY`
(`home`, `contacts`, `deals`, `inbox`) rides the bar; everything else lives
behind **More**, which expands the same element into a sheet. One nav element
means one navigation landmark and no second item list to keep in sync — and
because the hidden routes' own rows are `display:none` at this width, **More**
carries `aria-current="page"` for them, dropping it once the sheet is open so
two elements never both claim the current page.

`RAIL_LESS_SCREENS` is the documented layout exception: `onboarding`, `book`,
`client`, `preferences`, `oauth-consent`. These render full-bleed with their own
chrome — a human lending an agent their authority reads that screen apart from
the app, not framed inside it. The pre-session surfaces (login, availability,
the splash) use the same rail-less frame.

### A nav label is presentation and never a route id

This is the convention most likely to be got wrong by the next person adding a
destination, so it is stated in `nav.ts`, again in `palette.tsx`, and here.
`NavItem.screen` is the **route id** — the stable English name in the hash, in
`App.tsx`'s switch, and in every `href`. `NavItem.labelKey` is a **catalog key**
whose rendered text is free to differ, and today three of the ten do:

| Route id | Rendered label |
|---|---|
| `deals` | Pipeline |
| `inbox` | Approvals |
| `ai` | Ask Margince |

`deals` routes to the pipeline surface; `inbox` is a governance surface, not a
mailbox. The command palette leans on the split deliberately: every screen
command carries its route id as a hidden `keyword`, so someone typing "deals" or
"inbox" still finds the relabeled destination, in either locale, without a
hand-kept synonym list. **Never rename a `screen` to match a label** — that
breaks every existing hash URL and every `SCREEN_ENTITY` / `OFF_RAIL_TITLE_KEYS`
lookup keyed on it.

Off-rail destinations (reached from Settings, not the rail — `settings`,
`dedupe`, `products`, `offers`, `offer-templates`, `custom-fields`, `partners`,
`share`, `search`, `design`) resolve their page title through
`OFF_RAIL_TITLE_KEYS`, so a raw screen slug is never shown as a title.

### The badge policy

`BADGE_SCREENS` is `{ tasks, inbox }`, and the rule behind it is narrow: **a
badge counts only what wants a human's attention** — approvals waiting, tasks
due. Ambient totals are deliberately absent, because the list endpoints are
keyset-paginated and do not return one, and a decorative count contradicts the
rule. Today `AuthedShell` supplies only `inbox` (from `usePendingApprovals`);
`tasks` is enrolled but renders **nothing** until a due-count exists behind it,
rather than a fabricated number. A count of zero also renders nothing.

## Colour

**The product surface is white.** This is a founder ruling of 2026-07-23,
recorded at the top of `src/app/shell.css`: the chrome is white rather than the
design language's §2b deep ink-green field, so rail icons read as ordinary theme
tokens instead of white-alpha on a dark ground.

Reading `tokens.css` alone would tell you the opposite, and this is the trap.
The dark-rail family — `--bgRail`, `--railTop`, `--railBottom`, `--railIcon`,
`--railIconHover`, `--railIconActive`, `--railHover`, `--railActive`,
`--overlayScrim` — still exists, still carries its "deliberately unthemed,
white-alpha in BOTH themes" comment, and is still correct **for the ink-green
field**, which now keeps the agent panel at the sidebar foot and the
website/deck surfaces. It is not the app chrome. A contributor styling a new
panel from those tokens is styling the marketing field by mistake.

**Both appearances ship, and the theme resolves before React mounts.**
`main.tsx` calls `applyTheme(resolveTheme())` above `createRoot(...).render(...)`.
The resolution lives in `src/app/theme.ts` and nowhere else: an explicit choice
(`margince.theme` in `localStorage`) wins, otherwise the OS
`prefers-color-scheme`. `useTheme` inside `TopBar` owns *changing* the theme;
`theme.ts` owns *deciding and applying* it.

That split exists because the theme used to be owned entirely by the
authenticated chrome, and the failure was specific: every unauthenticated
surface rendered with no `data-theme` at all, so a dark-mode reader got the
light sign-in page however carefully the dark tokens were authored — and the
effect that set the attribute had no cleanup, so after signing out of a dark
session the *same screen* rendered dark. One screen, two appearances, neither of
them chosen. Applying at boot fixes both, and also removes the light-to-dark
flash on reload.

**Literal colours live only in `tokens.css`.** Everything else — `brand.css`,
every component sheet, every `.tsx` — reads `var(--token)`, and a derived value
is a `color-mix()` of a canonical token rather than a new hex. That is what lets
the dark theme's accent lift carry through automatically with nothing to
re-declare per theme. The rule is enforced twice over (see below), and the
exemptions are **named files, never patterns**:

| Exempt | Why |
|---|---|
| `design-system/tokens.css` | the literals are its job; `tokens.test.ts` pins each one |
| `index.html` | `<meta name="theme-color">` cannot read a CSS custom property |
| `design-system/provider-mark.tsx` | it carries Google's and Microsoft's own sign-in marks — another company's colours are not ours to tokenise, and a provider mark in Ledger Green is a *wrong* mark |

`--overlayLight` / `--overlayDark` (pure white and pure black, unthemed) live in
`tokens.css` for the same mechanical reason: they are material effects rather
than brand colour, and a literal anywhere else fails the gate.

## Provenance

**`EvidenceMark` is THE provenance affordance.** A value that came from
somewhere other than a person typing it carries a dotted underline; opening the
mark says where it came from, how sure the system was, the text it was read
from, when — and offers a way through to that field's full history.

It **replaced a stack of three chips under every value** (`ProvenanceTag` +
`ConfidenceMeter` + `EvidenceChip`, three widgets per field). Three chips under
a value do not read as "this was derived"; they read as clutter, and the value
they describe gets lost among them. The mark keeps the record readable and puts
the receipts one interaction away.

The older primitives survive in two places, both deliberate:

- **Inside the mark.** `EvidenceMark`'s panel renders `ProvenanceTag` itself,
  and states confidence as a word rather than a meter.
- **On the staging surfaces**, where the whole point of the screen is to compare
  a proposal against what is held: the approvals inbox (`screens/inbox.tsx`),
  the onboarding confirm card
  (`screens/onboarding-conversation/confirm-card.tsx`,
  `screens/onboarding-company-form.tsx`), the Company-context settings screen,
  and the record surfaces that show a single provenance line
  (`people.tsx`, `leads.tsx`, `consent.tsx`, `history.tsx`).
  `StagedProposal`/`FieldDiff`/`ApprovalGate` are the composed forms of the same
  vocabulary.

**One open at a time, for pointer *and* keyboard.** A module-level
`closeOpenMark` holds the single currently-open panel; opening one closes the
last. Pointer dismissal would have given that behaviour for free to a mouse
user, and the explicit registry is what makes it true for a keyboard user
tabbing down a column of marked values — they leave a trail of one panel rather
than a stack of overlapping regions. Escape closes and returns focus to the
trigger. The panel is a named `<section>`, not a dialog: it is a disclosure
beside the value, the page behind it stays usable, and nothing traps focus. A
mark with **no source renders as plain text** — an underline that opens an empty
popover teaches the reader to stop opening them.

## The core primitive

`MarginceCoreScene` (`design-system/margince-core.tsx`, WDS-CORE-1..4 /
ADR-0076) is the product's one piece of AI identity, shown by the
unauthenticated surface, the session splash, onboarding and the in-app
workbench. Four things about it are load-bearing rather than stylistic:

- **One implementation.** A caller passes `state` and never restyles. Sizing
  through the documented `--coreSize` / `--coreGlass` custom properties is
  configuration; anything beyond that is a caller restyling a shared primitive.
- **The state list is closed** — exactly eight: `idle`, `listening`, `working`,
  `success`, `attention`, `error`, `quiet`, `unavailable`. Callers use the Core
  as a *status channel* (a sign-in in flight, a server that cannot be reached),
  and a status channel with an open vocabulary is one nobody can test and no
  second caller can reuse. `progress` is optional and draws the ring only when
  passed.
- **Rendering is a fallback ladder, not a technology.** The shader is preferred
  and a non-GPU CSS rendering of *every* state is required
  (`margince-core-liquid.tsx`).
- **It is `aria-hidden`.** Every state it shows is also stated in text by the
  surface around it, which is what makes it safe to be this decorative.

The sidebar's agent orb is deliberately **not** the Core: the Core paints its
interior on a canvas, and permanent chrome on every screen would run a render
loop for the whole session. The orb uses the same technique the Core's shell
does — `color-mix()` over tokens, no literals — with layered radial gradients in
place of a shader.

## The gates

`make check-fe` → `make frontend-check` is the merge lane, and it runs in this
order. Four fail-closed shell greps come first, deliberately: they hold the same
discipline even if the test tree regresses.

| Gate | Where it lives | What fails it |
|---|---|---|
| Token purity | `frontend/scripts/check-ds-purity.sh` | a hex/`rgb()`/`hsl()`/`oklch()` literal outside `tokens.css` (fails closed if it scans zero files) |
| Font lock | `frontend/scripts/check-font-lock.sh` | a `font-family` outside Outfit / DM Sans / JetBrains Mono + the named generic fallbacks |
| Icon glyphs | `frontend/scripts/check-icon-glyph.sh` | an emoji in rendered code (comments are stripped — the 🟢/🟡 tier notation is house style and renders through `AutonomyDot`) |
| Spacing | `frontend/scripts/check-ds-spacing.sh` | a **newly added** inline `margin`/`padding`/`gap` px literal; diff-scoped vs `origin/main`, waived in-line with `// ds:ignore <reason>` |
| Contract type drift | `make frontend-check` | `pnpm gen:api` produces a diff in `src/api/schema.d.ts` / `public-events.ts` |
| Lint | `pnpm lint` (Biome) | formatting and lint findings over `src` + `index.html` |
| Conformance suite | `design-system/conformance.test.ts` | the AST-accurate arm of the same rules, plus: hard-coded user-facing copy outside the i18n catalogs, a class namespace declared in two stylesheets, a service worker that caches or fabricates a `/v1` response, an invalid web-app manifest |
| Token canon | `design-system/tokens.test.ts` | a Ledger-Green value drifting from the design canon |
| Typecheck + build | `pnpm build` (`tsc -b && vite build`) | any type error |
| Unit tests | `pnpm test` (Vitest) | co-located `*.test.tsx` |
| Render UAT | `make fe-uat` → `frontend/scripts/fe-uat.mjs` | a changed component with no co-located story, a changed story the build does not register, or an unclean headless render. **Not** in `make check` — it is the frontend-only UAT lane, artifact at `.tmp/fe-uat/manifest.json` |
| Screen acceptance | `make frontend-e2e` → `frontend/e2e/` | AC-named Playwright cases, axe WCAG 2.2 AA, the 390px no-horizontal-scroll sweep, the perceived-perf budget |

The backend's `craft static` pre-push hook does **not** cover `frontend/` — the
frontend lane is separate from the Go merge gate and needs node + pnpm. Run
`make check-fe` (or `make frontend-check`) before pushing a frontend change.

## Where to look first

| If you are changing… | Start at |
|---|---|
| a destination, a nav label, a badge | `src/app/nav.ts`, then `src/app/shell.tsx` and `shell.test.tsx` |
| what a route renders | `src/App.tsx` (`ScreenView`), then the screen file |
| a colour, a radius, a spacing rung | `src/design-system/tokens.css` (and `tokens.test.ts`) — never a call site |
| a derived colour role | `src/design-system/brand.css` — a `color-mix()`, never a new hex |
| light/dark behaviour | `src/app/theme.ts` + the `[data-theme="dark"]` block in `tokens.css` |
| how a derived value shows its receipts | `src/design-system/evidencemark.tsx` |
| a staging/approval surface | `src/design-system/trust.tsx` + `src/screens/inbox.tsx` |
| copy | `src/i18n/en.ts` **and** `src/i18n/de.ts` — key parity is compile-time |
| money, dates, durations, zones | `src/format/format.ts` |
| an API call | `src/api/client.ts` is the seam; regenerate types with `pnpm gen:api` |
| the Core's appearance or states | `src/design-system/margince-core.tsx` + `margince-core-liquid.tsx` |

## Where the code lives

| | |
|---|---|
| The API seam + generated contract types | `frontend/src/api/{client.ts,schema.d.ts,public-events.ts}` |
| Boot: theme, query client, service worker, 403 handling | `frontend/src/main.tsx` |
| Route → screen, the auth gate, the onboarding gate | `frontend/src/App.tsx` |
| Shell, rail, top bar, account menu | `frontend/src/app/{shell.tsx,shell.css}` |
| The canonical nav, badges, mobile set, rail-less set | `frontend/src/app/nav.ts` |
| Hash router | `frontend/src/app/router.tsx` |
| ⌘K palette, Ask FAB | `frontend/src/app/{palette.tsx,fab.tsx}` |
| Theme resolution and persistence | `frontend/src/app/theme.ts` |
| Tokens (canonical) / derived roles / base controls | `frontend/src/design-system/{tokens.css,brand.css,base.css}` |
| Atoms, trust vocabulary, composed surfaces | `frontend/src/design-system/{atoms,trust,composed}.tsx` |
| The provenance mark | `frontend/src/design-system/evidencemark.tsx` |
| The Core primitive + its renderers | `frontend/src/design-system/margince-core{,-liquid,-feed}.tsx` |
| The AI workbench frame | `frontend/src/design-system/margince-workbench.tsx` |
| Design gates (tests) | `frontend/src/design-system/{conformance,tokens}.test.ts` |
| Design gates (fail-closed greps) | `frontend/scripts/check-*.sh` |
| Change-scoped render UAT | `frontend/scripts/fe-uat.mjs` |
| Screens | `frontend/src/screens/` (one file per surface; `onboarding-conversation/` is the one state-machine directory) |
| Catalogs / presentation edge | `frontend/src/i18n/`, `frontend/src/format/` |

## Where to go next

[company-record-page.md](company-record-page.md) (the biggest
screen this structure carries) ·
[company-context.md](company-context.md) (the onboarding wizard and the
company-profile screens) · [architecture.md](architecture.md) (the Go side of
the same contract) · [../reference/make-targets.md](../reference/make-targets.md)
(every target named above) · [../../frontend/README.md](../../frontend/README.md)
(commands, the UI-preview switches, working agreements).
