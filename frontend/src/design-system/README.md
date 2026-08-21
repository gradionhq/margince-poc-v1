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
| **`Button`** | **The one button: `primary` / `ghost` / `danger`, `small`, `iconOnly`. It owns its GEOMETRY, which is why none of it arrives as a prop: the height is `--control-h`, shared with `TextInput` and the `Select` trigger so a button beside a field sits on the field's line; an icon child is sized by the button (16px, 14px small) rather than by a `size=` at the call site, because lucide's own default is 24px and 31 call sites shipped that way; and a labelled button carries a width floor so "No" and "Add" still read as buttons. `iconOnly` is the one opt-out — square, no floor, and the caller still owes it an accessible name. `reason` / `reasonId` refuse it and say why, and the caller cannot defeat either by passing `disabled` or an `aria-describedby` of its own. `pending` is the OTHER unavailability and must never be spelled as the first: a write in flight sets `aria-disabled` rather than `disabled`, so the reader keeps focus on the control they just pressed, and it draws in full ink with a turning mark instead of the dimmed, barred treatment that everywhere else means “not yours to do”. The label does not change while it is set — `busyLabel` is where a sentence about the wait goes, because it lands in `aria-describedby` rather than renaming a control the reader is focused on. Both spellings of refusal outrank it: a control nobody may press cannot also be mid-press** | `atoms.tsx` | ✅ |
| `BusyMark` | The turning mark a control shows while a write it started is in flight. Sized by whatever hosts it, and decorative — `aria-busy` on the control is the fact, so a glyph that announced itself would say it twice. Under reduced motion it stops turning and stays visible: a spinner has no end state to land on, and removing it would leave a reader who asked for less motion with no signal at all | `atoms.tsx` | via `Atoms → Buttons` |
| `.iconbtn` | A bare icon affordance that is NOT a `Button`: no fill, no border, no label — the verb a row offers where a full button would out-shout the row. A class rather than a component, because it is chrome a row applies to its own `<button>`; it carries the `--control-h-sm` hit target, the hover, and the focus ring | `base.css` | via `Atoms → Buttons` |
| `.card-actions` | A card's TRAILING action row: the verbs under a card's body, with the air above them and the gap between them. Not `.form-actions`, which is a form's submit row and carries no top margin because the field above it brings its own — a card whose body is a `FactList` and prose has nothing that does (`.factlist` sets `margin: 0` on purpose), so its buttons sit against the last line of text and against each other. The spacing rides on the row so it holds for every body the card can have above it | `atoms.css` | ✅ |
| `.link-button` | A secondary TEXT affordance — accent-coloured, no fill, no border — for an action that must not compete with the primary button beside it: a download, a "view existing", an in-row verb that is a link rather than a command. A class, like `.iconbtn`, because it is chrome a caller applies to its own `<a>` or `<button>`. **Not `.co-rowlink`**, which is for a row TITLE whose position already says it is a link and which therefore only underlines on hover — inside a form or a toolbar that reads as a value somebody typed. It carries its own hover and a solid accent focus outline; the outline is solid rather than the filled controls' low-alpha shadow ring because this class has no fill for the ring to read against. A lucide glyph may lead the label — the class sizes it to 16px and puts the pair on one line, exactly as `.btn` and `.iconbtn` do, so nothing about an icon is the caller's to spell | `atoms.css` | ✅ |
| `Badge` | A status pill in the six tones. `quiet` is the same status without the fill — a dot in the tone's colour and the label as plain text — for a column that carries one per row, where a stack of pills reads as decoration a reader learns to skip | `atoms.tsx` | ✅ |
| **`Avatar`** | **A person's or company's chip: monogram, optional logo, and a tint that is ALWAYS drawn. The tint was opt-in once, and the result was that a company was a coloured chip in the list it was found in and a neutral accent chip on the record page that list opened — one click apart, same company. `identity` is what the tint is keyed on when the displayed name is not stable (a record id, an address): without it a rename moves a record to a different colour on every screen at once, which reads as a different record. `size` is the whole scale — `xs` dense table, `sm` list row (the default), `md` record header, `lg` wide record header — because four sizes were being rendered by three stylesheets for a prop that admitted two. The monogram splits on address punctuation as well as whitespace, so a reader known only by `jane.doe@…` is "JD" rather than "J"** | `atoms.tsx` | ✅ |
| `AvatarStack` | A group of people as overlapping monograms, folding past `max` into a "+N" chip rather than running the row wide. Expects a non-empty list: every caller guards `people.length > 0` before rendering it, so there is no "0 people" state to draw. Its separating ring is a `box-shadow`, not a border, so a stacked face is the same size as the lone chip beside it | `avatarstack.tsx` | ✅ |
| `TextInput` | The one text field | `atoms.tsx` | ✅ |
| `SearchField` | A text field with the search affordance | `atoms.tsx` | ✅ (`Atoms → Search`) |
| `Textarea` | The one multi-line field | `atoms.tsx` | ✅ |
| **`Select`** | **The one dropdown: a button trigger plus a portalled listbox. Never a `<select>`.** An option whose label is not in the page's language carries `lang` (a BCP 47 tag), which reaches both the option and the trigger face — WCAG 2.2 AA 3.1.2, and the reason a language picker is buildable here at all | **`select.tsx`** | ✅ |
| **`DateInput`** | **The one date field.** A native `type="date"` — the deliberate exception to the `Select` rule, because a date input's CLOSED FACE is an ordinary text box that takes our tokens completely, and what the platform draws is only the picker popover, which appears on request and which users already know how to drive. It owns the FORMAT: `value` is always `YYYY-MM-DD`, the same shape the contract's `format: date` fields carry, so a value round-trips wire→control→wire with no parse step to get wrong | `dateinput.tsx` | ✅ (`Value inputs`) |
| **`TokenInput`** | **A set of short values built one at a time — the control the filter engine's `in` operator needs, where the operand is a LIST (`region ∈ {DE, AT, CH}`) rather than a scalar.** Not a multi-select `Select`, because `in` is used where the candidate set is not enumerable in advance (nobody can list every city). Enter or comma commits, Backspace on an empty box removes the last, a duplicate is dropped silently (`in` is a set, so a refusal would be noise), and blur commits so clicking away never discards what was typed | `tokeninput.tsx` | ✅ (`Value inputs`) |
| **`TokenList`** | **The same token as `TokenInput`, without the text box: a set assembled somewhere else — by a picker, a search, a selection in another control — drawn as a set rather than as a run of sentences.** Same file and same stylesheet on purpose (one token, one home); it takes `{id, label}` because the value a caller stores is not the words a reader should see. `removeLabel` is a function so each control's accessible name carries WHICH token, and omitting `onRemove` renders a read-only set rather than disabled buttons | `tokeninput.tsx` | ✅ (`Value inputs`) |
| `Checkbox` / `Radio` | A tick with its label as the other half of the click target | `atoms.tsx` | ✅ |
| **`Field`** | **The label-above-control row every form is built from. Owns the id and hands the control `{ id, required, aria-describedby, aria-invalid }`, so a call site never mints an id or repeats one. Four slots beyond the label: `hint` is the rule that always applies, `error` is a refusal — its own slot, because putting a refusal through `hint` renders it in the same meta-grey as neutral help and, on one card, as the SUCCESS line four elements above it — and `icon` / `trailing` are affordances INSIDE the control's outline, which is what `auth.tsx` forked its own field component to get. The shell appears only when `icon` or `trailing` is passed, so a plain field's markup is unchanged. `labelEnd` puts a "Forgot?" beside the label without it joining the control's accessible name** | `atoms.tsx` | ✅ (`Atoms → Fields`, and `Fields — affordances and refusal`) |
| **`usePasswordReveal`** | **A password field's reveal control AND the input `type` that goes with it, returned together because they are one fact — a caller handed only the button holds the other half itself and can get the two out of step, which is a control announcing the opposite of what it does. Pass `trailing` to `Field` so it sits inside the focus ring. Labels arrive translated, like all copy here** | `passwordreveal.tsx` | ✅ (via `Atoms → Fields — affordances and refusal`) |
| `FieldGrid` / `FieldRow` | The two-column label/value grid around a record's fields: the grid AROUND a value, not the value itself. Fixed-width label column (not `auto`), so every value's left edge sits at the same x on every record and every locale — a `FieldRow`'s `label` is always required and always drawn there. A read-only row takes a plain node; an editable one wraps `InlineText`/`InlineChoice` as `FieldRow`'s children rather than this component reimplementing hover-to-edit. `InlineText` draws no visible label of its own (its `label` prop is screen-reader- and aria-only), so pass `FieldRow`'s own `label` as usual there. `InlineChoice` DOES draw its own "label: value" inline, closed or open — pass it `hideLabel` to suppress that visible half so `FieldRow`'s label is the only one on screen, while the value still sits in the grid's shared value column rather than escaping it | `fieldgrid.tsx` | ✅ |
| `MoneyInput` | An amount with its currency, formatted at the presentation edge | `moneyinput.tsx` | ✅ |
| **`SettingList` / `SettingRow`** | **One settings DECISION per row: what it is and what it does on the LEFT, what it is set to on the RIGHT, at one x down the whole page. Reach for it in every settings card — the shape is what lets a reader audit a page by travelling one column instead of finding the answer somewhere new in each card, which is what this tree had. `control` takes a node OR a function; the function form receives `{ id, aria-labelledby, aria-describedby }` so the label the row draws and the name the control announces are the same string with the caller writing it once (a `Switch` keeps its own `labelHidden` label, because it owns its NAME by design, but still takes the row's `aria-describedby` through `describedBy` — otherwise the sentence saying what the setting does reaches nobody who cannot see it). `value` is the current answer standing beside the verb that changes it — "marek@gradion.com [Edit]" — and is passed ONLY where the control does not already show it. `layout="stack"` gives the control the full width below the naming, and what picks it is COMPLEXITY, not size: a control that ANSWERS the row's question sits beside it, a control that IS the subject (a table, a toggle matrix, a log) goes below, and a control needing two inputs submitted together belongs behind a verb in a `Modal` so the row stays an answer. The hairline between rows rides on the list, not on each row — a row that spaced itself would space itself differently in the next card** | `settingrow.tsx` | ✅ |
| **`FileDropzone` / `FileDropzoneControl`** | **Choosing ONE file, by drop and by click, from a single control. The `<input type="file">` is the control and the zone is chrome stretched over it, so the keyboard path is the real one rather than a handler imitating it — a bare drop target excludes everyone not holding a mouse. Nothing else in the zone is interactive and only the label text is `pointer-events: none`; inheriting that onto the input made the whole zone unclickable while every jsdom test still passed. `emptyLabel` belongs to the caller because only the caller knows what it is asking for. An empty selection never fires `onPick` (cancelling a picker is not the act of clearing a field), and the input's value is cleared after every pick, or re-choosing the same path fires no event at all. **`FileDropzone` brings its own `Field`; a caller that renders more inside one — the contract form lists the paper already filed above the zone — takes `FileDropzoneControl` and passes the `Field`'s control props down, so one input keeps one label** | `filedropzone.tsx` | ✅ |
| `SegmentedControl` | A small closed set of options, all visible at once | `atoms.tsx` | ✅ |
| **`ChoiceList`** | **One question, every answer readable AT REST — the radio GROUP the `Radio` atom never had. A native `fieldset` and `legend` with NO `role` on top (the browser already exposes the grouping of native radios, and an ARIA role would take the element out of the mapping that gives the `legend` its meaning), a legend that can be hidden without going unnamed, and an optional `description` per answer that is part of the label (so the help line is clickable and is read with the option it belongs to).** Reach for it over `Select` for a decision a reader must weigh — a dropdown over two options makes somebody open a menu to discover the alternative — and over `SegmentedControl` the moment an option is a sentence rather than a tab name | `choicelist.tsx` | ✅ |
| **`Switch`** | **A setting that writes when you flip it. A `Checkbox` states an intent something later submits; this IS the action, which is why it announces `role="switch"` and takes a `pending` state — the same refusal semantics as `Button`'s, `aria-disabled` and a mark rather than a dimmed control, with `disabled` and `reason` both outranking it. `reason` refuses the flip on its own and no `disabled={false}` beside it takes that back, exactly as on `Button`: it is the sentence for a change this reader may not make, so the control under it is one they cannot flip. `describedBy` names an element the CALLER owns, alongside the switch's own hint and refusal rather than instead of them: inside a `SettingRow` the sentence saying what the setting does is the row's `description`, and a node-form control cannot see the id the row minted — so a row holding a switch passes it through the function form of `control`. No `busyLabel` here: no surface has needed one, and a prop with no caller is an API nobody has tested. A filter over a list is neither — that is a pressed button** | `switch.tsx` | ✅ |
| **`Callout`** | **What a surface says about ITSELF, in four closed tones: `info` carries no urgency, `warn` says something will go wrong if you do nothing, `danger` says something is wrong or about to be irreversible, `success` confirms it landed. Never content, and never the only signal — the words carry the meaning** | `callout.tsx` | ✅ |
| **`FactList`** | **Label→value pairs a reader scans rather than edits. Rows arrive as an array so a caller drops absent facts: an empty value claims we know it and it is blank, which is not the same as not knowing** | `factlist.tsx` | ✅ |
| `RecordPicker` | Search → candidates → pick, for choosing an existing record | `recordpicker.tsx` | ✅ |
| `PassportSelect` / `ScopeChips` | Which agent passport, and the scopes it carries | `passportselect.tsx` | — |
| `Modal` | The one dialog: portalled, Escape-closing, Tab kept inside. `placement="right"` is the drawer form — full height on the right edge, the record behind still legible; on a phone both are the same full-screen sheet | `atoms.tsx` | ✅ (`Dialog` centred, `Drawer` right) |
| `ConfirmModal` | A dialog that asks before something irreversible | `confirmmodal.tsx` | ✅ |
| `NamePrompt` | A write whose only input is a name: trigger, dialog, name box, save. It owns the name, the rule that an empty one cannot be saved, and clearing the box on success — the chrome is `ConfirmModal`'s, which is what keeps it from being a second dialog. `onSave` takes a `done` callback rather than returning a promise, because a refused write must leave the dialog open with what was typed still in it; the caller calls `done` from its own success path. Reach for it instead of hand-rolling a `Modal` with a `Field` — the saved-view action did that, and the second surface needing the same question would have asked it differently | `nameprompt.tsx` | ✅ |
| `OverflowMenu` | The verbs a record offers but a reader rarely wants | `atoms.tsx` | ✅ |
| `Disclosure` | A section the reader opens when they want it | `atoms.tsx` | ✅ |
| **`Card`** | **The one card surface. `as` picks the element (`section` by default, also `div` / `article` / `form` / `li`), `inset` is the recessed variant, and `title` / `sub` / `actions` render its `SectionHeader` — a hand-rolled `<div className="card">` is a second card the moment one of the five chrome values moves** | `atoms.tsx` | ✅ |
| `SectionHeader` | The heading of a block: title, description on its own line under it at full width, and the block's actions beside the pair. `level` is `1` / `2` / `3` — `1` for the one header that IS the page's name, `3` for a section INSIDE a section, and the type steps down with the outline rather than leaving a nested heading the same size as its parent. `Card` passes it straight through | `atoms.tsx` | ✅ |
| `EmptyState` / `Skeleton` / `Kbd` | Page furniture: nothing-here, one placeholder bar, key cap. `Skeleton` is the BAR — reach for `PendingBody` below unless you are drawing a shape (a ring, a chart) that a column of lines cannot stand in for | `atoms.tsx` | ✅ |
| **`PendingBody`** | **The one pending state. Four grew before it — three inline-styled bars in `QueryStates`, one silent 32px bar in `SurfaceState`, five unanimated bone rows in `ListTable`, and a long tail of hand-rolled bars and bare "Loading…" lines — and they disagreed about the shape, the height, and whether anything was ANNOUNCED at all. `label` is REQUIRED and not defaulted: a placeholder carries no text, so the spoken line is the only thing a screen reader has, and it is the caller who knows what is being waited for ("Loading the review queue…" beats "Loading…" for somebody who cannot see which part of the page went grey). `lines` is a HEIGHT RESERVATION, not decoration — under-reserving is why a card jumps when its body arrives, and the jump costs a reader their place, which is worse than the wait. `visible` shows the label as well as speaking it, for a wait long enough that a mute grey block reads as broken. `QueryGate` / `QueryStates` / `SurfaceState` all render it, and forward `pendingLabel` / `pendingLines` (`loadingLabel` / `loadingLines` on `SurfaceState`) so a screen names its own wait without spelling a second pending state. Two fitness functions hold the line: the announcement has exactly one home, and so does the pulse** | `atoms.tsx` | ✅ |
| **`useToast` / `ToastRegion`** | **The transient confirmation — "that worked" — fixed to the foot of the viewport. It withdraws itself after 3.5s, cancelling any earlier one so two quick saves each get a full life; `{ sticky: true }` keeps it for a message carrying a verb, because a reader reaching for Undo must not lose it mid-reach. `{ mark: false }` drops the green completion dot for anything that is not a completion — the three hand-copied toasts this replaces put a tick beside their failures, which says the opposite of what the sentence says. The `<output>` is a live region already, so nothing beside it declares `role="status"`** | `toast.tsx` | — |
| `Panel` / `PanelBody` / `PanelRow` / `PanelPlate` | The record page's titled-card shape, which `Card` does not offer: a fixed-height header (a title alone, or a title with a badge or a button, all the same height), full-bleed rows under it, and an optional footer band for a figure that belongs to the whole panel. `sub` is one line of description under the title, in the same band — the header height is a FLOOR, so a description is the one slot that raises it while everything sharing the title's line keeps the one height. Reach for it instead of falling back to `Card` for the sake of a sentence: that trade is how a panel-shaped card ends up drawn as a second surface. `PanelBody` is its own component rather than a prop on `Panel`, because the header's rhythm, the body's padding and a row that wants to touch the panel's own edges are three different things living in one box: a caller needing both padded text and full-bleed rows nests `PanelBody` and `PanelRow` as siblings instead of fighting one slot that tries to be both. A `PanelRow` is INERT unless you pass `interactive`, which claims the WHOLE row is one press target — a button or a link filling it, so pointing anywhere in the row aims at the same thing, which is what earns the hover fill. A row that merely CONTAINS a control is not that: the control draws its own hover, and a fill behind it claims a hit area the row does not have. The hairline between two consecutive `PanelBody`s is the panel's own seam rule — a screen that wants ruled reading blocks stacks bodies and draws no border of its own. `tone="accent"` is the ONE lead variant: an accent border and a tinted header for the single card on a page that asks for a MOVE rather than reporting state — two of them on one page is no lead at all. `actions` is a band under the body for verbs that CHANGE the panel (the footer reports; this acts). `PanelPlate` is the recessed plate inset from the panel's edges that separates what IS from what to DO: context on the plate, moves full-bleed on the panel's own ground, and a reader can tell the halves apart before reading a word of either. It is a `Card` with rows, not a rival surface — when `Card` grows a row and a footer band, this folds into it | `panel.tsx` | ✅ |
| `StatCard` / `AttainmentRing` | One reading with the basis it was drawn from; the server's attainment band as an arc. `value` is a **string**, and `numeric` is what gives a figure the mono face — money, a count, a duration, where digits of one width make a column of readings line up. Deliberately a flag and not a `ReactNode` value: widened, the face would be the caller's to spell, and a screen that spells type is the second author of a scale this tier owns | `atoms.tsx` | ✅ |
| **`StatStrip`** | **A record's readings as ONE plate of ruled slots rather than N free-standing cards — cards are read one at a time, a strip is read across as a single comparison. Takes `StatCard`s as children and owns only the plate: slot count (from the children actually drawn, so a conditional slot leaves no empty cell), the rules between slots, the fold when the row stops being legible, and the one type scale every slot shares. A slot sized to its own content stops the row reading as one comparison** | `statstrip.tsx` | ✅ |
| `Meter` / `Sparkline` / `Chip` | A proportion as a bar (pass `value` and `max`, never a percentage), a short series as a bare polyline, and one attribute of a record as an icon pill — a `Chip` is a fact, a `Badge` is a status. `Meter`'s `tone` colours the FILL and `restTone` says the TRACK is itself a value the caller names beside the bar (overdue against open) rather than the empty space the fill has not reached; `dense` is its second size — the bar as a LABEL'S own bar rather than a block of its own, thinner and with no vertical interval, which is what a row-per-dimension reading wants. Take the size from the prop: reaching into `.meterbar` from a screen sheet for a height is how two callers became two authors of one geometry | `readings.tsx` | ✅ |
| `FileChip` | One stored file as the small card a reader clicks to get it: kind glyph, filename, same-origin download. Distinct from `Chip`, which is a fact about a record and links off our origin — this IS the file, so its name is the filename the saved copy will carry, and two files on one row are tellable apart before the click rather than after it. PDF is the one kind with its own glyph (it is what agreement paper arrives as); everything else takes the neutral mark rather than a guessed one | `filechip.tsx` | ✅ |
| `DataTable` | A simple column/row table with optional row navigation, drawn inside a `TableScroll`. `label` is REQUIRED: it is the name the scroll region announces once the table is wider than its box, so it is the same phrase the heading above the table already says (`t("aiusage.spendLabel")`, `t("offer.lines")`) | `atoms.tsx` | ✅ |
| **`TableScroll`** | **The ONE box a table too wide for its column scrolls sideways INSIDE, so the page never scrolls with it — 720px settings columns and eight-column delivery logs meet here. Reach for it for any hand-drawn `<table>` that `DataTable` cannot express; `DataTable` already renders into one. It takes a tab stop and announces itself as a named `region` only while it is ACTUALLY holding something past its right edge, because a tab stop in front of every table in the product is a cost every keyboard reader pays for the few that overflow. `label` is required for that name — an unnamed region tells a reader nothing about which of the page's tables they just landed in. Four screens had each written this wrapper by hand and none of them was reachable** | `atoms.tsx` | ✅ |
| `EvidenceMark` | The ONE §4 provenance affordance: a dotted underline on a value a person did not type, opening to where it came from | `evidencemark.tsx` | ✅ |
| `AutonomyDot` | The 🟢/🟡 autonomy semantics as a token component — never an emoji glyph | `trust.tsx` | ✅ |
| `EvidenceChip` / `ConfidenceMeter` / `ProvenanceTag` | Where a value came from, how sure we are, who captured it. Inside `EvidenceMark` and on the staging surfaces — never stacked under a field. `EvidenceChip`'s `evidence.lines` adds WHERE in the source the snippet sits, as 1-based line numbers: consecutive numbers close into a range (`lines 12–14`) because that is one place in the transcript, gaps stay listed apart, and a source with no lines to point at renders exactly as before | `trust.tsx` | ✅ |
| `ApprovalGate` / `StagingCard` / `StagedProposal` | Staged-not-real state and the Accept / Edit / Dismiss triad | `trust.tsx` | ✅ |
| `FieldDiff` | The inline old→new value diff; a null side reads as a marker, never a blank | `trust.tsx` | ✅ |
| `PassportChip` | An agent passport id, mono so it reads as an identifier | `trust.tsx` | ✅ |
| `RoleBadge` / `FieldGuard` | A principal's role, and a withheld value that reads as withheld rather than absent | `rbac.tsx` | — |
| `ExplainNumber` | A converted aggregate opening into its contributing rows (FX lineage) | `explain.tsx` | ✅ |
| `MarginceCoreScene` | The product's one piece of AI identity: a glass ball of liquid with four dots suspended in it, in the closed eight-state vocabulary of the agent's work lifecycle (dormant · ingesting · reasoning · drafting · applied · flagged · disconnected · error). State is MOTION first — one formation each — and colour second. `aria-hidden`, and it carries no click. Callers pass `state` and never restyle; a different material is the `--coreC1/2/3` triple, never a second component. `margince-core-motion.ts` (pure placements) / `margince-core-engine.ts` (the loop) / `margince-core-feed.tsx` are its internals, not a caller's API | `margince-core.tsx` | ✅ |
| `MarginceWorkbench` | The in-app agent workbench: steps, runtime chip, the Core in context | `margince-workbench.tsx` | — |
| `PipelineBoard` / `DealCard` | The pipeline surface and its cards | `composed.tsx` | ✅ (`RecordView → BoardInSurface`) |
| `RecordView` | The record page shell: identity, readings, timeline | `composed.tsx` | ✅ |
| `GroupedTimelineList` / `TimelineRow` | The activity timeline, grouped | `composed.tsx` | via `RecordView` |
| `MorningBriefItem` | One brief line. **Zero production callers and no story**: `RecordView` does not render it, and `home.tsx` draws its own `BriefItemCard` instead. Its only exercise is `composed.test.tsx`. Either the home brief adopts it or it goes — decide before building on it | `composed.tsx` | — |
| `ProviderMark` | A federated provider's own sign-in mark — the ONE file allowed literal colours, because another company's colours are not ours to tokenise | `provider-mark.tsx` | — |
| **`SurfaceState`** | **The nine-state honesty vocabulary — `ready \| empty \| withheld \| unavailable \| loading \| unsupported \| failed \| stale \| partial` — as ONE component, with `sectionState()` to classify a composite read's section and `omitted()` to ask whether a grant withheld it. `empty` is the only state allowed to say "there is none", because it is the only one that knows; drawing any of the other eight as empty states a fact the page does not have. Two ordering decisions are load-bearing: `stale` puts its caveat ABOVE the rows (a caveat under a figure arrives after the reader has taken it as current) and `partial` puts its count BELOW them. A `failed` with no `onRetry` is `unavailable` with extra words. The nine sentences are keyed `state.*`; what there is none OF stays the caller's word, in `emptyLabel`, and what is being WAITED for is `loadingLabel` — the loading arm used to be a mute 32px bar, and three screens had bolted their own visually-hidden line beside it rather than being able to hand it one** | `surfacestate.tsx` | ✅ |
| **`Eyebrow`** | **The one spelling of uppercase micro-type: 11px, `--fw-semibold`, `--tracking-eyebrow`, `--textMeta`. `as` picks `h2` / `h3` / `h4` / `span` / `dt`, because the same look is a real heading over a section and a plain label beside a value, and nothing about the type says which. The declarations live in `base.css` as `.t-eyebrow` so a selector-only site (`.firmo dt`) can reach them too** | `eyebrow.tsx` | ✅ |
| `Breadcrumb` | The trail that says where the reader is and how they got here: a `nav` landmark the caller names, an `<ol>`, one `<li>` per stop. It refuses the three things a hand-rolled trail gets wrong. The LAST stop is the current page and is never a link, even when the caller passes it an `href` — a link to the page you are already on is a control that does nothing. Separators are `aria-hidden` spans INSIDE the item they follow, never list items of their own (a three-stop trail would then announce as five things) and never CSS `content` (some screen readers speak it and some do not, so the same page reads differently to two people). And only the last stop gives way under pressure: ancestors are short labels the product chose, and shortening them would hide the way back, while the last is user data of unbounded length — `useTruncationTooltip` carries the rest of it on hover AND on focus. Takes no `className` or `style`: a trail restyled per screen is how one product comes to draw two different crumbs | `breadcrumb.tsx` | ✅ |
| **`CardBoundary`** | **A render boundary around ONE card. The app-level boundary is the floor, not the story: by the time it catches, the whole shell — navigation rail included — has unmounted, so one card's throw costs the reader every way out. This one keeps its place, says the card failed, and retries with the query cache reset beside its own state. It never shows the thrown error's text: a render throw names our internals and a reader cannot act on it** | `cardboundary.tsx` | ✅ |
| **`PipelineLadder`** | **One message's path through the ingress pipeline, top to bottom, with what each step did and why. It holds NO list of stages — it walks whatever the server sends, so a step added later appears with no frontend release. An unrecognised stage renders from the server's own `label` / `reason_text` rather than vanishing or printing a raw key: a closed set is right for a five-value enum and wrong for a vocabulary designed to grow, where an omission is indistinguishable from "nothing happened". The nine statuses keep four distinctions apart that a reader would otherwise conflate — `not_applicable` (did not apply) vs `unknown` (swept, we cannot tell), and `withheld` (rendered UNCONDITIONALLY to a non-owner, so its presence is not a row-existence oracle) vs an omitted rung** | `pipelineladder.tsx` | ✅ |
| `ListTable` | A record list as a table: columns, rows, the query dials above them, and the footer under them. Controlled — the screen owns the sort/filter/search state the server answers to. `selection` adds row checkboxes (inside the frozen identity cell) and a bulk bar above the grid; the screen owns the verbs and their per-row writes. A column declares its KIND and the table sizes it: `fixed` is the identity column, `numeric` right-aligns an amount, and `verbs` says the column holds buttons rather than a value — sized by the buttons, in pixels, since a translated label is not a fraction of a page. Every other column takes a share of what is left and stops at its own minimum, at which point the BODY scrolls sideways (keyboard-reachable and named, via `TableScroll`'s hook) rather than the columns being crushed: a settings column is 654px wide and a five-column table is not | `listtable.tsx` | ✅ |
| `ListSurface` | The chrome `ListTable` renders into, usable on its own for a list that is not a table: saved-view tabs, the count line, the search field, filter chips, the archived toggle, and the footer | `listsurface.tsx` | via `ListTable`, and directly in `RecordView → BoardInSurface` |
| `Menu` | The popover panel `ListSurface` hangs its sort and filter sets in — a `fieldset` with a heading, because a set of controls that belong together IS one, and `align` picks which edge it opens from. Reached through the surface's own dials; there is no second spelling of a dropdown panel | `listsurface.tsx` | via `ListTable` |
| `CountLine` | The one sentence under a list saying what is on screen out of what exists, and what it is sorted by. It is where "23 of 1,204" is spelled, so a screen never invents its own phrasing for the same fact — and `more` is what keeps an unknown total from being reported as a known one | `listsurface.tsx` | via `ListTable` |
| `InlineChoice` / `InlineText` | Hover-to-edit ON a record page: a chooser that commits the instant something is picked, and a text field that commits on Enter or blur. Both show the plain VALUE with no hover affordance to a reader who may not edit it, keep the typed answer when a save is refused, and treat re-picking the stored value as no edit at all. `FieldRow` is what puts them in the grid | `inlinechoice.tsx` | via `FieldGrid` |
| `Logomark` | The product's own "M". One mark, every fill on `currentColor`, so the shell chip and the onboarding speaker cannot drift into two | `logomark.tsx` | — |
| `useTruncationTooltip` | The whole of a string its row had to truncate, on hover or focus — and nothing at all when the string already fits. Reach for it wherever user data of unbounded length is drawn on one line: a record name, a trail segment, a rail row's reading | `tooltip.tsx` | ✅ |
| `usePrefersReducedMotion` / `useTypeStream` / `useDocumentIntro` | Motion, with one rule: reduced motion jumps to the END state, never to nothing | `motion.ts` | — |
| **`.arrive` / `.arrive-stack`** | **The arrival: content fades over `--dur-enter` and rises over `--dur-move`, staggered `--stagger-enter` apart, four deep and then together. A container whose CHILDREN arrive says `.arrive-stack` and its children need no class — a screen should not have to remember to label every card. `.wrap`, the shell's page gutter, is named in the rule itself, so every screen gets it without a call site. Two things to know before using it. A container that is a stack does NOT itself arrive (two nested fades make a dimmer fade, not a longer one), so mark every link of the chain where a screen keeps its blocks a level deeper. And it plays when an element is INSERTED, which is why a tab strip whose panel is a fresh element gets its transition for free while the strip itself stays still — that is the whole tab-panel animation, with no wrapper and no state** | `enter.css` | via every screen |
| **`--focus-ring` / `--focus-glow` / `--focus-ring-forced`** | **The focus ring, in the three shapes this product has and no more. An OUTLINE for a control sitting on a surface — outside the box, so it never changes the control's size. A GLOW for a field, because a field already has a boundary and an outline around a bordered box reads as two boxes. And a transparent outline PAIRED with the glow for a control that is neither, which replaces the platform's off-palette ring and holds the outline property at a real width so forced-colors mode still has something to paint. Ten spellings preceded them, two of which drew nothing at all. The offset stays per-component: a control clipped by its own container draws the ring inside itself or loses half of it. A fitness function refuses an eleventh spelling** | `tokens.css` | — |
| **`useUnsavedGuard` / `UnsavedGuard`** | **Unsaved edits, and what happens when the reader leaves without saving them — where the answer used to be nothing. `useUnsavedGuard(dirty)` is called by whatever holds a draft, from wherever it is; `UnsavedGuard` wraps the surface and holds ITS OWN content until the question is answered, so the draft is still on screen while the reader decides. Two mechanisms because there are two ways to leave: a reload is the browser's to ask about through `beforeunload` (registered only while dirty — a listener that is merely present costs the page its back/forward cache), and a move within the app gets this product's `ConfirmModal`. Escape and the backdrop KEEP the edit: a dialog whose dismissal destroys work punishes the reader for not reading it. Scoped to the surface rather than wired into `hashchange` on purpose — a router-level guard also intercepts Back and Forward, and getting that wrong breaks navigation for everybody, which is worse than the loss it prevents** | `app/unsaved.tsx` | — |
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

**`SurfaceState` is the primitive for the third row**, and for the six other
things a surface can be that are not content. Reach for it before hand-rolling
a message line: it already knows that withheld, unavailable and unsupported are
three different sentences, and that only `empty` may claim there is none.

Three more things carry this properly and are worth copying. `Switch`'s
`reason` prop renders the explanation, points the control at it with
`aria-describedby`, **and refuses the flip by itself** — the caller does not
owe it a `disabled` beside it and cannot cancel it with one, because a sentence
saying a setting is not yours to change, over a switch that changes it, is a
denial the reader disproves with one click. `Button`'s `reason` does the same for an ACTION — it
disables the button, renders the sentence beside it and wires
`aria-describedby` — because a `title` on a disabled button is announced by no
screen reader and a disabled button cannot be focused, so a reason living only
in `title` reaches exactly nobody who needed it; `Button`'s `reasonId` points
several refused controls at ONE sentence already on the page, for a surface
where one fact refuses all of them. And `FieldGuard` covers a withheld VALUE
rather than a withheld surface. What is left hand-rolls
`<EmptyState><p className="t-small">{t(…)}</p></EmptyState>` as the card body,
which is the shape to match where `SurfaceState` does not fit.

`Switch` versus `Checkbox` follows from the same honesty: a `Checkbox` states an
intent that something later submits, a `Switch` **is** the action. A control that
writes when you flip it and announces itself as a checkbox has told the reader
the wrong thing about what their next click does.

That pairing is also the answer for a **stateful** control a permission denies —
one that is the only place a reader can see the setting's current value. Absent
would hide a granted read; withholding the surface would hide the fact. A
`Switch` carrying `reason` shows the state, refuses the change, and says
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

The sidebar has seven roots, and a story's `title` is the only thing that puts
it under one — the gate keys on `importPath`, never on the title, so a title is
free to describe the surface rather than the file:

| Root | What is under it |
|---|---|
| `Design System/` | One node per primitive in this directory. |
| `Records/` | The screens a rep works in, and the cards on them (`Company 360/`, `Company rail/`). |
| `Settings/` | `You/` and `Organization/`, mirroring `SETTINGS_TABS` in `screens/settings.tsx` one for one, with the card as the leaf. |
| `Patterns/` | Screen-tier building blocks that are not a page: the query gate, the create/edit/merge/share actions, the composer. |
| `Onboarding/`, `Signed out/`, `Shell/` | The first run, the pages reachable without a session, and the application frame. |
| `MCP Apps/` | The governed tool surfaces and their document forms. |

Leaves are Sentence case throughout. The trap the old flat `Screens/` root left
behind is worth naming once: `Company connections` was the company rail's
relationship graph, not the settings Connections tab, and the tab it looked like
had no story at all. It is `Records/Company rail/Relationship graph` now.

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
| `design-system/conformance.test.ts` | Hard-coded user-facing copy, the same colour and font rules, one stylesheet per class namespace; a reduced-motion rule a later plain rule of equal specificity beats; an infinite animation with no reduced-motion answer; a second home for the pending announcement or the placeholder pulse; a focus ring that spells its own width and colour |
| `design-system/tokens.test.ts` | A token whose value drifted from the design canon |
| `e2e/` (axe) | WCAG 2.2 AA on every core screen, plus the 390px no-horizontal-scroll sweep |
