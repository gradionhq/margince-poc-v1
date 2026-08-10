# `src/design-system/` — the Margince design system

Read this before you hand-roll a control.

## The rule

**Every interactive control comes from this directory.** A native `<select>`, a
hand-rolled dropdown, a bespoke menu, a second modal, one more "just this once"
chip — each of those is a defect, not a shortcut. Two reasons, and the second is
the one that bites:

1. A browser-drawn control is drawn by the browser. `<select>` takes our tokens
   on its closed face and none of them on the list behind it (`option` is not
   stylable in any engine we ship to), so it reads as a hole in a product built
   entirely from these files.
2. A copy drifts silently. The primitives here exist because the same row was
   already hand-rolled in eleven places, each with its own gap, its own focus
   ring and its own idea of what a label is — and every one of them looked fine
   in review on its own.

If what you need is genuinely not here, add it **here**, with a story and a
spec, and it becomes the one spelling. Copy never lives in a primitive: words
arrive through props, translated by the caller with `t()`.

## What this directory already gives you

| Primitive | For | File | Story |
|---|---|---|---|
| `Button` | The one button: `primary` / `ghost` / `danger`, `small` | `atoms.tsx` | ✅ |
| `Badge` | A status pill in the six tones | `atoms.tsx` | ✅ |
| `Avatar` | A person's or company's chip: monogram, optional logo, optional per-name tint | `atoms.tsx` | ✅ |
| `TextInput` | The one text field | `atoms.tsx` | ✅ |
| `SearchField` | A text field with the search affordance | `atoms.tsx` | ✅ |
| `Textarea` | The one multi-line field | `atoms.tsx` | ✅ |
| **`Select`** | **The one dropdown: a button trigger plus a portalled listbox. Never a `<select>`** | **`select.tsx`** | ✅ |
| `Checkbox` / `Radio` | A tick with its label as the other half of the click target | `atoms.tsx` | ✅ |
| `Field` | The label-above-control row every form is built from; owns the id and hands the control `{ id, required, aria-describedby }` | `atoms.tsx` | ✅ |
| `MoneyInput` | An amount with its currency, formatted at the presentation edge | `moneyinput.tsx` | ✅ |
| `SegmentedControl` | A small closed set of options, all visible at once | `atoms.tsx` | ✅ |
| `RecordPicker` | Search → candidates → pick, for choosing an existing record | `recordpicker.tsx` | ✅ |
| `PassportSelect` / `ScopeChips` | Which agent passport, and the scopes it carries | `passportselect.tsx` | — |
| `Modal` | The one dialog: portalled, Escape-closing, Tab kept inside. `placement="right"` is the drawer form — full height on the right edge, the record behind still legible; on a phone both are the same full-screen sheet | `atoms.tsx` | ✅ |
| `ConfirmModal` | A dialog that asks before something irreversible | `confirmmodal.tsx` | ✅ |
| `OverflowMenu` | The verbs a record offers but a reader rarely wants | `atoms.tsx` | ✅ |
| `Disclosure` | A section the reader opens when they want it | `atoms.tsx` | ✅ |
| `Card` / `EmptyState` / `SectionHeader` / `Skeleton` / `Kbd` | Page furniture: surface, nothing-here, heading row, loading placeholder, key cap | `atoms.tsx` | ✅ |
| `StatCard` / `AttainmentRing` | One reading with the basis it was drawn from; the server's attainment band as an arc | `atoms.tsx` | ✅ |
| `Meter` / `Sparkline` / `Chip` | A proportion as a bar (pass `value` and `max`, never a percentage), a short series as a bare polyline, and one attribute of a record as an icon pill — a `Chip` is a fact, a `Badge` is a status | `readings.tsx` | ✅ |
| `DataTable` | A simple column/row table with optional row navigation | `atoms.tsx` | ✅ |
| `EvidenceMark` | The ONE §4 provenance affordance: a dotted underline on a value a person did not type, opening to where it came from | `evidencemark.tsx` | ✅ |
| `AutonomyDot` | The 🟢/🟡 autonomy semantics as a token component — never an emoji glyph | `trust.tsx` | ✅ |
| `EvidenceChip` / `ConfidenceMeter` / `ProvenanceTag` | Where a value came from, how sure we are, who captured it. Inside `EvidenceMark` and on the staging surfaces — never stacked under a field | `trust.tsx` | ✅ |
| `ApprovalGate` / `StagingCard` / `StagedProposal` | Staged-not-real state and the Accept / Edit / Dismiss triad | `trust.tsx` | ✅ |
| `FieldDiff` | The inline old→new value diff; a null side reads as a marker, never a blank | `trust.tsx` | ✅ |
| `PassportChip` | An agent passport id, mono so it reads as an identifier | `trust.tsx` | ✅ |
| `RoleBadge` / `FieldGuard` | A principal's role, and a withheld value that reads as withheld rather than absent | `rbac.tsx` | — |
| `ExplainNumber` | A converted aggregate opening into its contributing rows (FX lineage) | `explain.tsx` | — |
| `MarginceCoreScene` | The product's one piece of AI identity, in its closed eight-state vocabulary. `aria-hidden`; callers pass `state` and never restyle. `margince-core-liquid.tsx` / `margince-core-feed.tsx` are its rendering ladder, not a caller's API | `margince-core.tsx` | ✅ |
| `MarginceWorkbench` | The in-app agent workbench: steps, runtime chip, the Core in context | `margince-workbench.tsx` | — |
| `PipelineBoard` / `DealCard` | The pipeline surface and its cards | `composed.tsx` | — |
| `RecordView` | The record page shell: identity, readings, timeline | `composed.tsx` | ✅ |
| `GroupedTimelineList` / `TimelineRow` / `MorningBriefItem` | The activity timeline, grouped, and one brief line | `composed.tsx` | via `RecordView` |
| `ProviderMark` | A federated provider's own sign-in mark — the ONE file allowed literal colours, because another company's colours are not ours to tokenise | `provider-mark.tsx` | — |
| `usePrefersReducedMotion` / `useTypeStream` / `useDocumentIntro` | Motion, with one rule: reduced motion jumps to the END state, never to nothing | `motion.ts` | — |
| `subscribeToWindowFocus` and friends | Whether this window has focus — one signal for the draw loop and the stylesheet alike | `window-focus.ts` | — |

Tokens and sheets, none of which a caller redeclares: `tokens.css` (the Ledger
Green canon, pinned by `tokens.test.ts` — the only file where a literal colour
may appear), `brand.css` (the derived layer: `color-mix()` over a canonical
token, never a new hex), `base.css`, and the per-component sheets a component
imports itself. `interaction.stories.tsx` catalogues the colours the *browser*
owns — caret, checkbox tick, scrollbar thumb, selection — which are set once at
the document root and belong to no component.

## Seeing them

```sh
cd frontend && pnpm storybook      # the catalog on :6006, light and dark
```

The Theme control in the toolbar flips `data-theme` exactly the way the shell
does, so every token re-resolves — check both before you call a surface done.
Stories live beside their component as `<name>.stories.tsx`; that co-location is
also what the change-scoped capture gate keys on
(`frontend/scripts/fe-uat.mjs`).

## Driving a control in a test

`Select` is a button and a portalled listbox, so `userEvent.selectOptions` does
not apply to it. Use the one helper:

```ts
import { pickOption } from "../design-system/select-testing";

await pickOption(user, screen.getByRole("combobox", { name: "Stage" }), "Won");
```

## The gates that enforce this

All of them run in `make frontend-check`, and the script gates are the
fail-closed grep arm on top of the vitest suites — the discipline holds even if
the test tree regresses.

| Gate | What it refuses |
|---|---|
| `frontend/scripts/check-native-controls.sh` | `<select>` / `<option>` / `<optgroup>` anywhere under `src/` except `design-system/select.tsx` — no browser-drawn dropdown |
| `frontend/scripts/check-ds-purity.sh` | A hex literal or `rgb()`/`hsl()`/`oklch()` outside `tokens.css` |
| `frontend/scripts/check-font-lock.sh` | A fourth type family |
| `frontend/scripts/check-icon-glyph.sh` | An emoji glyph in a source string — Lucide only |
| `frontend/scripts/check-ds-spacing.sh` | New raw-px margin/padding/gap outside this tier (diff-scoped) |
| `design-system/conformance.test.ts` | Hard-coded user-facing copy, the same colour and font rules, one stylesheet per class namespace |
| `design-system/tokens.test.ts` | A token whose value drifted from the design canon |
| `e2e/` (axe) | WCAG 2.2 AA on every core screen, plus the 390px no-horizontal-scroll sweep |
