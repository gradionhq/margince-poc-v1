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

## Layout

- `src/design-system/` — tokens (Ledger Green canon, pinned by
  `tokens.test.ts`), atoms, the trust primitives (§4 vocabulary:
  EvidenceChip, ConfidenceMeter, ProvenanceTag, StagingCard, ApprovalGate,
  StagedProposal), composed surfaces, and `conformance.test.ts` — the
  drift gates.
- `src/app/` — shell (WorkspaceRail + top bar), hash router, ⌘K palette,
  Ask FAB.
- `src/screens/` — one file per surface; unbuilt routes render the honest
  pending state.
- `src/i18n/` — DE (A24 default) + EN catalogs; key parity enforced at
  compile time and runtime.
- `src/format/` — the presentation edge: money/date/duration formatting,
  IANA-only zones, FX lineage display (consumes the IR base_value
  verbatim, never multiplies).
- `src/api/` — `schema.d.ts` is GENERATED (never hand-edit); `client.ts`
  is the one API seam (session cookie + `X-Workspace-Slug`, `/v1` mount).
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
