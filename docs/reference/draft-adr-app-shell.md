# DRAFT — ADR-0077 — The product surface is white; the app shell is a collapsible labeled sidebar of floating panels

> **This is a draft, staged in the build repo.** Target home is the spec repo at
> `specs/adr/ADR-0077-white-product-surface-and-collapsible-shell.md`, plus a new
> `DECISIONS` anchor. It is not filed there yet because that repo sits on an
> unrelated branch (`ops/dach-website-at-watchdog`) and the number needs
> confirming — **ADR-0076 is already referenced by the frontend** (the two-region
> unauthenticated surface and the Core primitive, commit `1943902e`) but has no
> file in the spec repo, so it is an open reconciliation, not a free number.

**Status:** Draft (Josh, 2026-07-30).

**Records** the founder ruling of 2026-07-23 (below), which is currently written
down nowhere. **Amends** [ADR-0040](../../../margince-foundation/specs/adr/ADR-0040-margince-visual-identity.md)
(A54, the Ledger Green identity) on the *fill* of the signature panel only — no
token value changes; `corpus/design/design.md:5`, `:20`, `:35`, `:139`;
`corpus/design/00-design-language.md:26`, `:67-79`; and
`specs/architecture/web-design-system.md` **WDS-NAV-1** with screen-acceptance
**AC-shell-1**. **Composes with** A72/ADR-0035 Am.1 (Automations is a primary-nav
surface), A107/ADR-0061 (one installation, one organization), ADR-0026 (autonomy
tiers), ADR-0036 (the approval inbox), and `scope#NEVER-4` (no credit meters).

## Context

### The ruling this ADR exists to record

On **2026-07-23 14:54**, in `#margince-dev`, addressed to Josh and Luitpold, the
founder wrote:

> PLEASE PLEASE stop doing stuff with dark background. Whatever we do must be
> white. Maybe maybe we later add a dark scheme. I understand how cool this is
> but for SME it's super weird. Black on white.... not white on black.....

This postdates ADR-0040 (2026-06-24) by a month and is the later, more specific
instruction. It exists only as a chat message, while the spec still reads
`design.md:35`: "Deep ink-green field | `#13231D` | the signature dark panel **+
app nav rail**". **The spec is therefore stale, and any build that ships a dark
product surface is following a superseded instruction.** Writing it down is the
main purpose of this record.

One scope question was asked the next morning and never answered:

> just to clarify you mean the dark onboarding and dark boxes not the dark mode
> which can be toggled right?

This ADR takes the conservative reading, which the ruling's own words support:
**dark fills in the default (light) surface are out; an opt-in dark *theme* stays
alive** ("maybe maybe we later add a dark scheme"). See the open question in
§Consequences — the unauthenticated surface is not resolved by this ADR.

### The design tension

The design direction prototype established the product's visual language against
a ~250px labeled sidebar with a brand block, counted nav items, a saved-views
section and a persistent agent card. The spec's canonical shell is a **64px
icon-only rail on the ink-green field** (`00-design-language.md:72`,
`design.md:151`). Those disagree structurally, not cosmetically: the rail has no
room for labels, saved views, or an agent presence.

Separately, the nav-item conflict was **already resolved upstream and the
acceptance criteria simply lag**. WDS-NAV-1 and AC-shell-1 enumerate **nine**
items; `00-design-language.md:69` and `design.md:154-156` both call a **ten**-item
nav canonical, inserting Automations. The tiebreak is not recency: **A72**
(`DECISIONS.md:171`, founder, 2026-07-06, ADR-0035 Amendment 1) ratifies the
bounded automation designer as "a first-class, **primary-nav** editing surface",
and AC-shell-1's source line pins it to `margince-poc/docs/architecture/web-design-system.md @ a11d6c08`,
a commit predating that decision.

## Decision

**A. The product surface is white. The signature move keeps its geometry and
loses its fill.**

`design.md:5` names the one thing product, website and decks share: "the deep
ink-green field panel on a matte ground". That gesture has two separable parts —
the **geometry** (an inset panel floating with ~10pt margin on a matte near-white
ground, white cards inside it, hairlines not shadows, `design.md:139`) and the
**dark fill**.

The shell keeps the geometry in full: the sidebar, the top bar and the agent card
are inset panels floating on the matte ground with a ~10px gap, bordered with
hairlines and a slight `--shadow-card`, never edge-to-edge. It drops the dark
fill: every chrome surface is `--bgElevated`.

Consequently **the app interior carries no ink-green field**. The field remains
correct and unamended for the **website and the decks**, which the 2026-07-23
ruling did not address. `design.md:35`'s "+ app nav rail" clause is withdrawn.

Two things this does *not* change: the accent (`--accent #0B7A53`) still carries
brand and primary actions everywhere, and the tonal separation between deep brand
green and bright success green (`#22c55e`) stands, so "green = brand" still never
collides with "green = high confidence".

**B. The nav is a labeled sidebar that collapses to the canonical rail geometry.**

Expanded is 250px with labels, group headings and the agent panel. Collapsed is
**64px** and matches the canonical rail's *geometry* — the logomark chip, the
active indicator bar, one flat list — but not its ink-green field, per §A. A
sidebar that changed colour on collapse would read as two different shells.

Collapse is user-toggled and persisted. Its choreography animates exactly **one**
layout property, the grid column (0.36s); icons hold a single size and labels
cross-fade. Measured: **zero horizontal and zero vertical drift** of every icon
and the logomark across the transition, largest single-frame shift 0.38px.

**C. The ten canonical items stand; they are grouped, and two are relabeled.**

Home ungrouped, then **Records** (Contacts, Companies, Leads), **Work** (Pipeline,
Tasks, Approvals), **Intelligence** (Reports, Automations, Ask Margince). That is
the canonical ten-item set in full — nothing added, nothing removed. Two **labels**
change: `deals` presents as **Pipeline** (nav id unchanged; it already routes to
the pipeline surface) and `inbox` as **Approvals** (nav id unchanged). "Approvals"
names what the surface is and survives being read by an auditor or a works council
on a screenshot; "Inbox" imports a mailbox framing into a governance surface
(ADR-0036).

Group headings keep their box when collapsed, drawing a hairline in place of their
text — swapping them for a shorter rule re-spaced every group.

**D. A badge counts only what wants a human's attention.**

Tasks (due) and Approvals (waiting) carry badges. Pipeline and Leads do not. This
retires AC-shell-1's fixture-derived "Tasks shows badge 4 and Inbox shows badge 3"
as a *normative* expectation — those were prototype values — and removes a
dependency the shell should not have: the list endpoints are keyset-paginated and
are not known to return totals, so ambient counts would have been designed against
data that may not exist.

**E. Chrome rulings.**

1. **One search affordance.** The top bar's search element is an input-styled
   **button** that opens the ⌘K palette on click or focus and never accepts inline
   typing. **AC-shell-7 stands unamended** — the visual treatment changes, the rule
   does not.
2. **The avatar owns Settings and sign-out**, in a menu, showing the principal's
   initials. Dismissal is document-level so Escape and outside clicks both work.
   AC-shell-1's "a user avatar (→ settings.html)" is satisfied; the standalone
   sign-out control in the rail foot is gone, because the foot now carries the
   agent panel.
3. **Saved views carry no marker** — label only. A coloured dot would collide with
   confidence-as-glyph, a reserved trust primitive, and every alternative semantic
   depends on state that does not exist. The section is not built yet: `/views`,
   `/lists` and `/tags` ship 15 handler-backed operations with zero frontend
   callers, but a saved view has no route to open, so shipping the labels would be
   navigation that goes nowhere.
4. **Off-rail routes resolve real titles.** Dedupe, Products, Offer templates,
   Custom fields, Offers, Share and Search no longer render an untranslated screen
   slug. Admin surfaces consolidate under Settings rather than taking nav slots.
5. **The breadcrumb names the record, and links the section.** On a record the
   section is the way back to its list; the record's own name is plain text,
   because you are already on it. The name resolves through `EntityRef`'s cache
   and falls back to the id in mono when it cannot.
6. **The collapsed rail reveals its toggle in the logomark's slot** on hover or
   keyboard focus, costing no row so the icons below keep their positions. It uses
   `:focus-visible`, not `:focus-within` — a mouse click leaves the button focused,
   which held the toggle visible and the logomark hidden until the user clicked
   elsewhere.

**F. At phone width the sidebar is a bottom bar plus a sheet.**

Four captioned primary destinations — Home, Contacts, Pipeline, **Approvals** —
plus **More**, which expands the same nav element into a sheet carrying every
destination. Ten icons on one row would need horizontal scrolling, and a nav you
must scroll is a nav you cannot see. Each tab is captioned: an icon-only tab bar
is a guessing game. Approvals is fixed in the primary four because **the 390px
approval path is required for V1**.

**G. Not decided here:** the right-hand context column. Contextual panels remain
each screen's own concern, as the build already treats them. No shell slot is
introduced and no amendment is needed.

## Conformance findings against the spec

| Spec statement | What we built | Verdict |
|---|---|---|
| `design.md:35` — `#13231D` is "the signature dark panel **+ app nav rail**" | No dark fill anywhere in the product | **Spec stale.** Superseded by the 2026-07-23 ruling. Amend. |
| `00-design-language.md:26` — `bgRail` is "**the left nav rail — deep ink-green** (white-alpha icons on it)" | White sidebar, dark icons | **Spec stale.** Same ruling. Amend. |
| `design.md:151` / `00-design-language.md:72` — rail is 64px, items 44×40px, inactive icon `white/40`, hover `white/10`, active `white/15` | 64px collapsed; 46×38 items; icons `--textContent`; hover `--bgHover`; active `--accentLight` + accent icon | **Partly stale** (the white-alpha values fall with the dark field), **partly a genuine deviation** (row height). Amend both. |
| `00-design-language.md:69` — "Stroke icons, **20px in the rail**" | 18px in both states | **Genuine deviation.** One size in both states is what removes the resize jitter; 20px is retained nowhere. Amend or revert — flagged as the one open build question. |
| WDS-NAV-1 / AC-shell-1 — nine items | Ten, grouped | **Spec stale.** A72 already ratified ten. Amend AC-shell-1. |
| AC-shell-1 — "Tasks shows badge 4 and Inbox shows badge 3" | Badges on Tasks and Approvals, from live counts only | **Spec stale** (prototype fixture values stated as acceptance). Amend. |
| AC-shell-1 — rail contains "a spacer, and a user avatar (→ settings.html)" | Avatar moved to the top bar with a menu; foot carries the agent panel | **Genuine deviation.** Amend. |
| `design.md:147-150` — top bar "white, ~56px, bottom `borderSubtle`. Right = contextual actions only. **Show nothing that isn't true for the current state**" | 52px, floating panel with a full hairline and shadow; right side is search, locale, theme, avatar | **Deviation on the frame** (a panel, not a bottom border) — amend. **The honesty rule is upheld**: nothing renders that isn't true. |
| `design.md:139` — the inset panel gesture, ~10pt margin | Sidebar, top bar and agent card are exactly this | **Conformant** — and this is what the floating chrome *is*. |
| AC-shell-7 — one search affordance, opens the palette, no inline typing | Input-styled button opening the palette | **Conformant.** |
| AC-shell-8 — record-aware Ask FAB, scoped label, persists across navigation | Unchanged by this work | **Conformant, with a known defect predating it**: the panel's Send has no handler. |
| AC-shell-2 — at most one active item | Enforced, pinned by `shell.test.tsx` | **Conformant.** |
| AC-shell-3/4/5/6 — the ⌘K palette | Unchanged; real search | **Conformant.** |
| The contrast law — 11–13px text must use `textMeta`; `textSecondary` ≥16px only; `textTertiary` decorative non-text only | Every new small style uses `--textMeta` or a *darker* token (`--textContent`, `--textPrimary`); neither reserved token is used below its size | **Conformant.** |
| Three families only; Lucide only; emoji only for autonomy tiers | Outfit for the wordmark, DM Sans for body, JetBrains Mono for eyebrows; Lucide throughout; no emoji added | **Conformant.** |
| Elevation is hairlines, not shadows | Chrome panels carry a hairline **plus** `--shadow-card` | **Deviation, minor.** `--shadow-card` is an existing token, so this is a use of the system rather than an invention — but the rule says hairlines. Amend the rule or drop the shadow. |
| WCAG 2.2 AA (frontend README) | Collapsed targets 46×38 | **Conformant** — 2.5.8 (AA) needs 24×24. It gives up 44×44, which is 2.5.5 **AAA**, and which my own earlier AC-shell-1c asked for. That AC is amended below. |
| `acceptance-standards.md` STATE-1..5 | The shell's live regions (badge count, brand block) omit rather than fabricate; the agent panel's unbacked lines are visibly marked example data | **Conformant for what ships.** Saved views not built, so its states are not yet owed. |

**Nothing in the build fails a criterion of the AC-shell set it was written
against.** The failures in that table all run the other way: spec statements the
build has outgrown. Two of the criteria this ADR *adds* below are not met yet and
say so where they are stated — **AC-shell-1j** (the cost line, still marked
example data) and the agent panel's activity and routing lines, which have no
endpoint behind them. They are stated as the target, not as achieved.

## Acceptance criteria (superseding my earlier draft)

- **AC-shell-1a** — The sidebar renders expanded (250px) or collapsed (64px) per the
  user's persisted preference, defaulting to expanded.
- **AC-shell-1b** — Nav renders Home ungrouped, then Records (Contacts, Companies,
  Leads), Work (Pipeline, Tasks, Approvals), Intelligence (Reports, Automations,
  Ask Margince). At most one item is active. Group headings hold their box when
  collapsed.
- **AC-shell-1c** — Collapsed targets are ≥24×24 CSS px (WCAG 2.2 2.5.8, AA).
  *This replaces the 44×44 of my earlier draft*, which asked for AAA and cost the
  compact row rhythm the direction requires.
- **AC-shell-1d** — Collapsed-state tooltips appear on keyboard focus and on hover,
  stay visible while the pointer is over them, and are dismissible with Escape
  without moving focus (WCAG 1.4.13).
- **AC-shell-1e** — Badges render only on Tasks and Approvals, from live counts. No
  badge renders a total.
- **AC-shell-1f** — No chrome surface in the default theme uses a dark fill. The
  opt-in dark theme is unaffected.
- **AC-shell-1g** — Collapsing moves no icon and no logomark on either axis; the
  panel narrows and labels fade.
- **AC-shell-1h** — The agent panel never renders a liveness claim absent a real
  running job; absent one it states configuration.
- **AC-shell-1i** — Any fixture-backed content in the agent panel is visibly marked
  as such.
- **AC-shell-1j** — The agent panel's cost line renders only for principals passing
  the runtime-visibility gate, and renders an honest zero with its estimate quality
  when it renders at all. *(Not yet built — cost is currently marked example data.)*
- **AC-shell-1k** — No authenticated route renders an untranslated screen slug as
  its page title.
- **AC-shell-1l** — On a record, the breadcrumb links the section and renders the
  record's name as text, never as a link to the page you are on.
- **AC-shell-1m** — At ≤700px the nav is a bottom bar of four captioned primary
  destinations plus More; Approvals is always among them; the body never scrolls
  horizontally.

## Consequences

**Spec edits required.** `design.md:35` drops "+ app nav rail";
`00-design-language.md:26` restates `bgRail` as a website/deck surface;
`design.md:5` and `:139` keep the gesture but scope the dark fill away from the
product; the 64px rail spec becomes the *collapsed* state and its white-alpha icon
values fall with the field; WDS-NAV-1 and AC-shell-1 take the ten-item set, the two
labels, and the badge rule; `design.md:147-150` takes the floating top-bar frame.

**Build.** Landed: `nav.ts` (groups, labels, badge and mobile-primary sets),
`shell.tsx`, `shell.css`, `entity.ts` (`SCREEN_ENTITY`), `entityref.tsx`
(`useEntityName`), both i18n catalogues, and `shell.test.tsx` — updated *with* this
record, never ahead of it.

**Contract additions still required.** A list operation behind the agent panel's
activity line (`GET /ai/calls` has no handler); routing and spend reads for the
panel's footer. All three are marked example data until then.

**Open question, not resolved here.** The **unauthenticated surface** is the last
dark surface in the product. Lars proposed it himself on 2026-07-21 and ruled
against dark backgrounds on 2026-07-23 — two days apart, and the ruling names
"dark onboarding" specifically. It is another session's work
(commit `1943902e`, ADR-0076). Somebody must ask him rather than assume, because
the app is now white everywhere and the front door is not.

**Risk accepted.** Two chrome widths mean every screen is verified at both. The
18px rail icon (against the spec's 20px) is the one place where smoothness was
chosen over the letter of the design language, and it is called out above so the
choice is reviewable rather than buried.
