# frontend/ — the Margince web app

React 19 + Vite + TypeScript (strict) + Tailwind 4 + Biome + Vitest +
Playwright, per spec ADR-0001/ADR-0054. Margince's **own** design system —
no gw-ui/Dispact reuse (founder decision 2026-07-05; the design source of
truth is the design-system spec).

## Commands

```sh
pnpm install
pnpm dev          # Vite dev server; proxies /v1 to http://localhost:8080 (BACKEND_PORT overrides)
pnpm check        # the frontend gate: Biome + unit tests + tsc + build
pnpm e2e          # build + the Playwright screen-acceptance harness
pnpm gen:api      # regenerate src/api/schema.d.ts from ../backend/api/crm.yaml
```

From the repo root: `make frontend-check`, `make frontend-e2e`, and `make dev`
(the full running stack — api + this SPA). The frontend lane is separate from
the Go merge gate (`make check`) — it needs node ≥ 20 and pnpm.

### UI-preview switches (`VITE_UI_PREVIEW_*`)

Presentation scaffolding for design review, off unless the var is set, and
**not** feature flags — they cannot make a flow work, only draw one. They live
in `src/app/ui-preview.ts`; the naming prefix is the contract with the reader.

| Var | What it draws |
|---|---|
| `VITE_UI_PREVIEW_OIDC=1` | The federated sign-in buttons on the login screen, with the second provider marked *not yet available*. |
| `VITE_UI_PREVIEW_RESET=1` | The "Forgot password?" link and the request card it opens. |

```sh
pnpm dev:preview                    # both switches on — the demo entry point
pnpm build:preview                  # the same, built
VITE_UI_PREVIEW_OIDC=1 pnpm dev     # login screen, with the SSO block drawn
```

**A preview build draws controls this installation cannot honour, so it must never
be what ships** — `dev` and `build` stay unchanged and stay honest.

`/auth/capabilities` serves `oidc_providers: []` because the OIDC flow has not
shipped (§19), and `ProviderButtons` correctly renders nothing for an empty
list — so the block is otherwise only visible in Storybook. With the switch on,
two providers are substituted **at the render boundary in `AuthScreen`**, after
the query: the wire is untouched, the query cache still holds the server's real
empty answer, and the buttons **complete no sign-in** — `startFederatedSignIn`
stays inert, because the contract documents no OIDC start or callback path. The
labels are deliberately not in the i18n catalogs: `oidc_providers[].label` is
server-owned copy that §11.5 says is never translated. A preview build logs a
one-time `console.warn` saying exactly this.

The same switch marks the second of those two providers **not yet available** —
`previewedUnavailableProviders()`, a set of keys `ProviderButtons` renders as a
native `disabled` button plus an `.is-unavailable` class the stylesheet draws.
The label is untouched, and that is the point: the marker must not splice words
we wrote onto a label the installation wrote. So the state is carried visually
and by `disabled`, and the trade is stated rather than hidden — a screen reader
hears that the control is unavailable, but not why. It can only ever come from
here:
`oidc_providers[]` items are `{ key, label }` with no availability field, so no
server can produce a marked provider, `ProviderButtons` receives an empty set in
the product, and its shipped behaviour is unchanged. That matters because §3.3
forbids a dead provider control by name (Google, Microsoft, SSO) and ADR-0076
keeps §3.3 load-bearing — a marked button is legal here for the same reason a
Storybook story is, and for no other.

`VITE_UI_PREVIEW_RESET=1` draws the "Forgot password?" link. This flow is
finished on both sides — `POST /auth/forgot-password` and
`POST /auth/reset-password` in the contract, handlers in
`backend/internal/modules/identity/reset.go`, and all four views on this screen —
and `password_reset` is computed live as `h.resetMailer != nil`, so it reports
`false` on the shipped `config/margince.yaml` only because that file has no
`email:` block. The switch draws the link; it wires no mailer. The request form
behind it is the real one, so submitting it against a mailer-less installation
gets a `501 not_implemented` back and shows it as the form's failure note — the
confirmation, the deep-link form and the spent-token refusal stay asserted in
`src/screens/auth.test.tsx` rather than reachable here.

Unset, each switch reads `undefined` and nothing changes. Both positions of both
are pinned by `src/app/ui-preview.test.ts` and the `federated sign-in` cases in
`src/screens/auth.test.tsx`; the e2e lane builds without any of them, so
`offers no identity provider that does not work` still measures the real
default.

## Layout

- **`src/design-system/README.md` is the catalog — read it before hand-rolling a
  control.** Every interactive control comes from that directory; a native
  `<select>` or a hand-rolled dropdown is a defect, and
  `scripts/check-native-controls.sh` refuses one. `pnpm storybook` shows them all.
- `src/design-system/` — tokens (Ledger Green canon, pinned by
  `tokens.test.ts`) plus `brand.css`, the DERIVED layer: every value there is a
  `color-mix()` of a canonical token, never a new hex, so it follows the dark
  theme's accent lift automatically and passes the purity gate. Then atoms, the
  **EvidenceMark** (`evidencemark*`) — the ONE §4 provenance affordance: a dotted
  underline on a value that came from somewhere other than a person typing it,
  opening to where it came from, how sure we were, the text it was read from, and
  when. One mark is open across the page at a time, for pointer and keyboard
  alike. It replaces the stack of three chips that used to sit under every value;
  the older primitives (EvidenceChip, ConfidenceMeter, ProvenanceTag) now live
  INSIDE the mark and on the staging surfaces (StagingCard, ApprovalGate,
  StagedProposal) — never stacked under a field again. The migration is real but
  partial: the company record page consumes the mark today while the other record
  screens still render the older primitives directly. Then the **Margince Core**
  (`margince-core*`, WDS-CORE-1..4 — one primitive, a closed eight-state
  vocabulary, a required non-GPU rendering of every state, `aria-hidden`;
  callers pass `state` and size it through `--coreSize` / `--coreGlass` and
  never restyle it), `motion.ts` (reduced motion jumps to the END state, never
  to nothing), composed surfaces, and `conformance.test.ts` — the drift gates.
- `src/app/` — the shell (a labeled sidebar that collapses to the canonical
  rail, its preference persisted; at phone width the same markup is a bottom bar
  with a More overflow), the top bar, `nav.ts` (the canonical ten items in three
  groups — a label is presentation and never a route id: `deals` presents as
  Pipeline, `inbox` as Approvals, `ai` as Ask Margince), `theme.ts` (light/dark
  resolved and applied BEFORE React mounts, so an unauthenticated screen can be
  dark at all), the hash router, the ⌘K palette, and the Ask FAB. See
  [docs/explanation/frontend-architecture.md](../docs/explanation/frontend-architecture.md).
- `src/screens/` — one file per surface, or one directory when a surface is a
  state machine (`onboarding-conversation/`); unbuilt routes render the honest
  pending state.
- `src/i18n/` — DE (A24 default) + EN catalogs; key parity enforced at
  compile time and runtime.
- `src/format/` — the presentation edge: money/date/duration formatting,
  IANA-only zones, FX lineage display (consumes the IR base_value
  verbatim, never multiplies).
- `src/api/` — `schema.d.ts` is GENERATED (never hand-edit); `client.ts`
  is the seam every typed `/v1` call goes through — the LinkedIn CSV upload is the
  one `/v1` route that bypasses it, because the generated client cannot serialize
  multipart (the OAuth discovery read in `connected-agents.tsx` is a raw `fetch`
  too, but it is not a `/v1` route at all). Also the session cookie and the `/v1`
  mount — no tenant header:
  one installation serves one organization, and the server binds that singleton
  itself, so two tests assert the absence of any workspace header).
- `e2e/` — the Playwright harness: AC-named acceptance tests, the 390px
  no-horizontal-scroll sweep, axe WCAG 2.2 AA on every core screen, the
  PERF-1 <300 ms perceived record-open budget. Runs over a network-edge
  seed mock by default; `BASE_URL=…` points the same suite at a live
  backend.

## The gates (all run by `pnpm check` / `pnpm e2e`)

1. Token canon — every §2 Ledger-Green value pinned to the design canon.
2. Three type families only (Outfit / DM Sans / JetBrains Mono).
3. Literal colours live only in `tokens.css`.
4. No hard-coded user-facing copy — JSX text and user-facing attributes
   must come from the i18n catalogs (TS AST walk).
5. No emoji glyphs in source strings — Lucide only; the 🟢/🟡 autonomy
   semantics render through the `.dot` token component.
6. The service worker never caches or fabricates a `/v1` response.
7. WCAG 2.2 AA (axe) + the perceived-perf budget in the e2e lane.
8. The unauthenticated surface at 390px / 320px / 200% zoom (ADR-0076): no
   horizontal scroll, the primary action reachable, nothing hidden to fit, the
   task region above the identity region below 960px, one h1 and it is the task,
   the Core out of the a11y tree, and axe. The rest of the §3.8 sweep walks
   authenticated routes only, so login had never been measured at any width —
   the first run of this found a contrast defect in the field labels.

## Working agreements

- Copy reaches components via `t()`/props — atoms never hard-code words.
- Anything that renders money/time goes through `src/format/` — locale
  changes rendering only, never a stored value; no FX math, no fixed
  offsets, no calendar diffs.
- Staged / real / human-typed are three distinguishable styles, always.
  Confidence is never hidden. Absent data is omitted, never guessed.
- Packaging: the app is a standalone static `dist/` build (`pnpm build`),
  served separately from the API binary (which serves `/v1` only). How
  `dist/` is hosted — a static server, a CDN, or a reverse proxy in front
  of the API — is a deployment choice, not baked into the build.
