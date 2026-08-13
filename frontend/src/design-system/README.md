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
| `AvatarStack` | A group of people as overlapping monograms, folding past `max` into a "+N" chip rather than running the row wide. Expects a non-empty list: every caller guards `people.length > 0` before rendering it, so there is no "0 people" state to draw | `avatarstack.tsx` | ✅ |
| `TextInput` | The one text field | `atoms.tsx` | ✅ |
| `SearchField` | A text field with the search affordance | `atoms.tsx` | ✅ |
| `Textarea` | The one multi-line field | `atoms.tsx` | ✅ |
| **`Select`** | **The one dropdown: a button trigger plus a portalled listbox. Never a `<select>`** | **`select.tsx`** | ✅ |
| `Checkbox` / `Radio` | A tick with its label as the other half of the click target | `atoms.tsx` | ✅ |
| `Field` | The label-above-control row every form is built from; owns the id and hands the control `{ id, required, aria-describedby }` | `atoms.tsx` | ✅ |
| `FieldGrid` / `FieldRow` | The two-column label/value grid around a record's fields: the grid AROUND a value, not the value itself. Fixed-width label column (not `auto`), so every value's left edge sits at the same x on every record and every locale — a `FieldRow`'s `label` is always required and always drawn there. A read-only row takes a plain node; an editable one wraps `InlineText`/`InlineChoice` as `FieldRow`'s children rather than this component reimplementing hover-to-edit. `InlineText` draws no visible label of its own (its `label` prop is screen-reader- and aria-only), so pass `FieldRow`'s own `label` as usual there. `InlineChoice` DOES draw its own "label: value" inline, closed or open — pass it `hideLabel` to suppress that visible half so `FieldRow`'s label is the only one on screen, while the value still sits in the grid's shared value column rather than escaping it | `fieldgrid.tsx` | ✅ |
| `MoneyInput` | An amount with its currency, formatted at the presentation edge | `moneyinput.tsx` | ✅ |
| `SegmentedControl` | A small closed set of options, all visible at once | `atoms.tsx` | ✅ |
| **`Switch`** | **A setting that writes when you flip it. A `Checkbox` states an intent something later submits; this IS the action, which is why it announces `role="switch"` and takes a pending state. A filter over a list is neither — that is a pressed button** | `switch.tsx` | ✅ |
| **`Callout`** | **What a surface says about ITSELF, in four closed tones: `info` carries no urgency, `warn` says something will go wrong if you do nothing, `danger` says something is wrong or about to be irreversible, `success` confirms it landed. Never content, and never the only signal — the words carry the meaning** | `callout.tsx` | ✅ |
| **`FactList`** | **Label→value pairs a reader scans rather than edits. Rows arrive as an array so a caller drops absent facts: an empty value claims we know it and it is blank, which is not the same as not knowing** | `factlist.tsx` | ✅ |
| `RecordPicker` | Search → candidates → pick, for choosing an existing record | `recordpicker.tsx` | ✅ |
| `PassportSelect` / `ScopeChips` | Which agent passport, and the scopes it carries | `passportselect.tsx` | — |
| `Modal` | The one dialog: portalled, Escape-closing, Tab kept inside. `placement="right"` is the drawer form — full height on the right edge, the record behind still legible; on a phone both are the same full-screen sheet | `atoms.tsx` | ✅ |
| `ConfirmModal` | A dialog that asks before something irreversible | `confirmmodal.tsx` | ✅ |
| `OverflowMenu` | The verbs a record offers but a reader rarely wants | `atoms.tsx` | ✅ |
| `Disclosure` | A section the reader opens when they want it | `atoms.tsx` | ✅ |
| **`Card`** | **The one card surface. `as` picks the element (`section` by default, also `div` / `article` / `form` / `li`), `inset` is the recessed variant, and `title` / `sub` / `actions` render its `SectionHeader` — a hand-rolled `<div className="card">` is a second card the moment one of the five chrome values moves** | `atoms.tsx` | ✅ |
| `SectionHeader` | The heading of a block: title, description on its own line under it at full width, and the block's actions beside the pair | `atoms.tsx` | ✅ |
| `EmptyState` / `Skeleton` / `Kbd` | Page furniture: nothing-here, loading placeholder, key cap | `atoms.tsx` | ✅ |
| `Panel` / `PanelBody` / `PanelRow` | The record page's titled-card shape, which `Card` does not offer: a fixed-height header (a title alone, or a title with a badge or a button, all the same height), full-bleed rows under it, and an optional footer band for a figure that belongs to the whole panel. `PanelBody` is its own component rather than a prop on `Panel`, because the header's rhythm, the body's padding and a row that wants to touch the panel's own edges are three different things living in one box: a caller needing both padded text and full-bleed rows nests `PanelBody` and `PanelRow` as siblings instead of fighting one slot that tries to be both. It is a `Card` with rows, not a rival surface — when `Card` grows a row and a footer band, this folds into it | `panel.tsx` | ✅ |
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

## Absent, disabled, or withheld — decided by CAUSE

A surface a reader cannot use is in one of three states, and **which one is not a
style choice — the cause picks it.** Getting this wrong is not a cosmetic bug: an
absent card and an empty card make the same shape on screen and mean opposite
things.

| Cause | State | What the reader gets |
|---|---|---|
| It does not apply here — a posture, a rollout flag, a capability this installation does not have | **absent** | Nothing. There is no fact to report. |
| A precondition the reader could fix is unmet — nothing selected yet, a write in flight, delivery not configured | **disabled** | The control, inert, **and what would make it live**. |
| A **permission** denies it | **withheld** | The surface keeps its place and **says that it is withheld**. |

The third row is the one that gets broken, because returning `null` on a denial
is the shortest code and looks tidy in review. It is a false statement. A
retention card that vanishes for an ops seat does not read as "not yours" — it
reads as "this installation keeps nothing", and an absent audit trail reads as
"nothing has happened here". Both are claims about the DATA, made by accident,
in place of a claim about authority.

Three consequences worth stating, because each was a real defect:

- **A withheld card asks the server for nothing.** The answer is already known,
  so keep `enabled: canRead` on the query. Withholding is about what the page
  SAYS, not about issuing a request in order to be refused.
- **Gate on the probe, not on its absence.** `/me` in flight is not a denial;
  branching before it answers flashes the notice at every reader.
- **A write affordance inside an otherwise readable surface may be absent**,
  provided the surface states its read-only posture once (`auto.readOnly`,
  `cf.noPermission` are the pattern). Withholding twelve buttons individually is
  noise; withholding the page's one explanation is the defect.
- **A surface that is only an ACTION may be absent on a denial.** The rule above
  is about not making a false claim, and a card holding no fact cannot make one —
  there is nothing for a reader to misread as "zero" or "nothing happened". The
  danger zone is the case: an absent Reset-data card says nothing about the
  installation, while "you may not reset this installation" is noise on every
  page that renders it. A surface that reports anything at all is not this.

Two things carry this properly today and are worth copying: `Switch`'s `reason`
prop, which renders the explanation **and** points the control at it with
`aria-describedby` — the only accessibility-wired denial in the tree — and
`FieldGuard`, for a withheld VALUE rather than a withheld surface. Everything
else hand-rolls `<EmptyState><p className="t-small">{t(…)}</p></EmptyState>` as
the card body, which is the shape to match until a primitive earns its place.

`Switch` versus `Checkbox` follows from the same honesty: a `Checkbox` states an
intent that something later submits, a `Switch` **is** the action. A control that
writes when you flip it and announces itself as a checkbox has told the reader
the wrong thing about what their next click does.

That pairing is also the answer for a **stateful** control a permission denies —
one that is the only place a reader can see the setting's current value. Absent
would hide a granted read; withholding the surface would hide the fact. A
disabled `Switch` carrying `reason` shows the state, refuses the change, and says
why, with the explanation attached to the control rather than sitting beside it.

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
