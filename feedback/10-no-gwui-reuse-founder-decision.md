# 10 — gw-ui reuse is withdrawn (founder decision 2026-07-05)

**Where:** ADR-0001 (frontend stack: "reusing `gw-ui` + `gw-design-system`"),
P9 framing in `design/00-design-language.md §3` (reuse map), EP09 tickets
B-EP09.2 ("Port/wire the reused `gw-ui` atom→template library") and the
B-EP09.1/09.2 DoD lines "naming follows existing `gw-ui`/`gw-design-system`
conventions".

**What changed:** Founder decision (Lars, 2026-07-05, in-session):

1. Margince does **not** reuse Dispact's `gw-ui`/`gw-design-system`
   components — Margince has a completely different design system.
2. Dispact itself will be refactored in the future, so it is the wrong
   foundation to inherit from.
3. The current HTML mockups are placeholders and will be redone.

**What exists now:** the Margince Design System v0 lives in the foundation at
`specs/design/design-system/` (committed spec-side) — tokens + base
components + trust primitives, light theme default. The build repo's
`frontend/` re-implements that vocabulary in React against the same tokens.

**Spec-side follow-up needed:** amend ADR-0001 (drop the gw-ui reuse clause),
re-scope B-EP09.2 to "build the Margince atom library (lazily, as screens
need components)", and revise the §3 reuse map + the LOC estimate note that
excluded gw-ui LOC (00-loc-estimate.md — the 20–40k "if Margince had to build
its own" is now the plan, amortized over the epics).
