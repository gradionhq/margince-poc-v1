# 0014 — Frontend scaffold (B-EP09.1): top-level `frontend/`, packaging call

**Date:** 2026-07-05 · **Status:** accepted

## Context

EP09 starts. The spec mandates a top-level `frontend/` (ADR-0054 layout tree;
ADR-0016 banner: "the frontend to `frontend/` (not `web/`)") with the Flow
stack (ADR-0001: React 19 + Vite + Zustand + TanStack Query + Tailwind 4 +
Biome, types generated from `crm.yaml`). The spec does NOT specify packaging —
embedded static assets vs. separately served (filed spec-side as a gap).

## Decisions

1. **`frontend/` is the pnpm+Vite source tree**; the Go module stays the only
   thing under `backend/`. Dependencies added only when a ticket uses them —
   Zustand/TanStack Query/lucide-react/the OpenAPI TS client arrive with the
   first screens, not speculatively.
2. **Packaging: single-binary stays the deploy shape.** When the React shell
   reaches parity with the prototype's smoke flows, `make frontend` will build
   `frontend/dist` and copy it under `backend/web/` as a generated artifact for
   the existing `go:embed` + `web.Handler()` (Go constraint: `go:embed` cannot
   reach outside the module). Until then the handwritten prototype in
   `backend/web/static` keeps serving `/`; the prototype is deleted at parity,
   not before.
3. **Separate check lane.** `make frontend-check` (install --frozen-lockfile,
   Biome, Vitest, tsc+Vite build) — NOT part of `make check`, which stays the
   Go merge gate runnable on machines without node. CI should run both.
4. **B-EP09.1 acceptance as fitness functions** in `src/design-system/`:
   `tokens.test.ts` pins every §2 Ledger-Green value to the mockup canon
   (normalized compare, dark block must not orphan tokens, rail stays unthemed
   per §2b); `conformance.test.ts` walks the tree — three type families only,
   literal colours only in `tokens.css`.
5. **Dark palette:** accent `#16A34A` is the ADR-0040 mandate; the dark
   surface/text values are our structured placeholders pending a spec-side dark
   pass (all reads go through the custom properties, so retuning is one block).
6. **gw-ui: withdrawn** (founder decision 2026-07-05, superseding the earlier
   "pends the repo" plan): Margince builds its OWN design system — no
   Dispact/gw-ui reuse (different design system; Dispact will be refactored;
   the mockups are placeholders). The system's v0 lives spec-side in the
   foundation (`specs/design/design-system/`: tokens + base components +
   trust primitives, light theme default) — NOT in the shipped product.
   `frontend/` re-implements that vocabulary in React against the same
   tokens. Components are built lazily as screens need them. Spec amendment
   filed as feedback/10 (ADR-0001, §3 reuse map, B-EP09.2 re-scope).

## Consequences

- `pnpm-lock.yaml` is committed; host needs node ≥ 20 + pnpm for the frontend
  lane only.
- Fonts load from Google Fonts in `index.html` for now; self-hosting is a
  follow-up before any offline/PWA ticket (B-EP09.8).

## Close-out (2026-07-05, end of the EP09 session)

EP09 landed 29/30 leaf tickets in one session (see STATUS.md for the
full list). Standing facts for future sessions:

- `--textMeta` is canon (ADR-0040 amendment via feedback/15) and pinned
  in tokens.test.ts alongside the §2 values.
- The api client mounts the contract paths under `/v1`
  (`${origin}/v1`) — the harness caught the unprefixed version.
- The e2e harness (Playwright) blocks the service worker and mocks /v1
  at the network edge; BASE_URL runs the identical suite live.
- Remaining: backend syncs crm.yaml (automations, public booking +
  CaptureConsent, audit-log, passports list, DOI issuance) → gen:api →
  B-EP09.15 + Settings audit/passport cards + booking consent.
