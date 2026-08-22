import type { Meta, StoryObj } from "@storybook/react-vite";
import { Lock, Mail, Plus, RefreshCw, Trash2 } from "lucide-react";
import { type CSSProperties, useEffect, useId, useRef, useState } from "react";
import {
  AttainmentRing,
  Avatar,
  Badge,
  Button,
  Card,
  Checkbox,
  DataTable,
  Disclosure,
  EmptyState,
  Field,
  Kbd,
  Modal,
  OverflowMenu,
  Radio,
  SearchField,
  SectionHeader,
  SegmentedControl,
  Skeleton,
  StatCard,
  Textarea,
  TextInput,
} from "./atoms";
import { AvatarStack } from "./avatarstack";
import { usePasswordReveal } from "./passwordreveal";
import { ProviderMark } from "./provider-mark";
import { Select } from "./select";

// Stories are the render surface the change-scoped fe-uat capture gate drives
// (frontend/scripts/fe-uat.mjs): a change to atoms.tsx re-renders these in a
// headless browser and fails on an unclean render. One story file per
// component module — fe-uat maps atoms.tsx → atoms.stories.tsx.
const meta: Meta = {
  title: "Design System/Atoms",
  parameters: { layout: "padded" },
};
export default meta;

type Story = StoryObj;

// The two shapes the stories below arrange things in: a wrapping row for
// atoms that sit side by side, and a column for surfaces that stack.
const row: CSSProperties = {
  display: "flex",
  gap: "0.75rem",
  alignItems: "center",
  flexWrap: "wrap",
};
const stack: CSSProperties = {
  display: "flex",
  flexDirection: "column",
  gap: "1rem",
};

// Every axis of the button on one screen, because each of the four defects this
// story was rewritten to expose was invisible while the variants were reviewed
// one at a time: a ghost 2px taller than the primary beside it, an icon at
// lucide's 24px default next to a 13.5px label, a two-letter label shrunk to a
// pill nobody reads as a button, and no focus ring at all. The rows below are
// the comparisons that make each of those visible in one look — same-row height,
// same-row icon size, same-row width floor.
export const Buttons: Story = {
  render: () => (
    <div style={stack}>
      <div style={stack}>
        <span className="t-label">Variants, default size</span>
        <div style={row}>
          <Button variant="primary">Save</Button>
          <Button variant="ghost">Cancel</Button>
          <Button variant="danger">Delete</Button>
        </div>
      </div>
      {/* The text affordance, beside a real Button so the thing it must not
          out-shout is in the same picture. Its focus ring is a SOLID outline,
          not the low-alpha shadow the filled controls use: with no fill of its
          own there is nothing for that ring to read against, and on an
          elevated surface it disappears. Tab through this row to see it. */}
      <div style={stack}>
        <span className="t-label">The secondary text affordance</span>
        <div style={row}>
          <Button variant="primary">Save changes</Button>
          <a className="link-button" href="#link-button-story">
            Download the signed PDF
          </a>
          <button type="button" className="link-button">
            View existing
          </button>
          {/* With an icon: lucide hands over a 24px glyph, so without a size
              rule on the class this label wrapped underneath its own icon.
              Compare the glyph here with the one in the buttons above — one
              size, owned by the control rather than by the call site. */}
          <button type="button" className="link-button">
            <Plus aria-hidden />
            Add another
          </button>
        </div>
      </div>
      <div style={stack}>
        <span className="t-label">Variants, small</span>
        <div style={row}>
          <Button variant="primary" small>
            Save
          </Button>
          <Button variant="ghost" small>
            Cancel
          </Button>
          <Button variant="danger" small>
            Delete
          </Button>
        </div>
      </div>
      <div style={stack}>
        <span className="t-label">With an icon</span>
        <div style={row}>
          <Button variant="primary">
            <Plus aria-hidden />
            Add person
          </Button>
          <Button variant="ghost">
            <RefreshCw aria-hidden />
            Reconnect
          </Button>
          <Button variant="ghost" small>
            <RefreshCw aria-hidden />
            Reconnect
          </Button>
        </div>
      </div>
      <div style={stack}>
        <span className="t-label">
          Icon only — square, and named for a reader
        </span>
        <div style={row}>
          <Button variant="primary" iconOnly aria-label="Add person">
            <Plus aria-hidden />
          </Button>
          <Button variant="ghost" iconOnly aria-label="Reconnect">
            <RefreshCw aria-hidden />
          </Button>
          <Button variant="ghost" iconOnly small aria-label="Reconnect">
            <RefreshCw aria-hidden />
          </Button>
        </div>
      </div>
      <div style={stack}>
        <span className="t-label">Short labels keep the width floor</span>
        <div style={row}>
          <Button variant="ghost">No</Button>
          <Button variant="primary">Yes</Button>
          <Button variant="ghost">Add</Button>
          <Button variant="primary">Save</Button>
          <Button variant="ghost">Disconnect this inbox</Button>
        </div>
      </div>
      <div style={stack}>
        <span className="t-label">Beside a field, which is the same box</span>
        <div style={row}>
          <TextInput
            defaultValue="ops@example.com"
            style={{ inlineSize: "16rem" }}
          />
          <Button variant="primary">Invite</Button>
        </div>
      </div>
      <div style={stack}>
        <span className="t-label">
          Refused, disabled, and the icon affordance
        </span>
        <div style={row}>
          <Button variant="primary" reason="Connect an inbox first.">
            Send
          </Button>
          <Button variant="ghost" disabled>
            Cancel
          </Button>
          <button type="button" className="iconbtn" aria-label="Remove">
            <Trash2 aria-hidden />
          </button>
        </div>
      </div>
      {/* The row this story exists for. Working and refused are opposite
          facts, and the product drew both as `disabled` — dimmed, barred,
          focus dropped — so a reader could not tell a request in flight from
          one their seat is not allowed to make. Side by side is the only way
          to see that they now differ: full ink and a turning mark against a
          dimmed pill. The label does not change, which is the other half; a
          "Saving…" here would rename a control the reader is standing on. */}
      <div style={stack}>
        <span className="t-label">
          Working — which must not read as refused
        </span>
        <div style={row}>
          <Button variant="primary" pending>
            Save
          </Button>
          <Button variant="ghost" pending>
            Reconnect
          </Button>
          <Button variant="primary" small pending>
            Save
          </Button>
          <Button variant="ghost" iconOnly pending aria-label="Reconnect">
            <RefreshCw aria-hidden />
          </Button>
          <Button
            variant="primary"
            pending
            busyLabel="Signing you in — this can take a moment."
          >
            Sign in
          </Button>
          <Button variant="primary" disabled>
            Save
          </Button>
        </div>
      </div>
      {/* Refusal outranks busy in both its spellings, and this row is here to
          prove it visually: neither of these draws a mark. A control nobody
          may press cannot also be mid-press, and an earlier cut of this
          feature rendered a natively disabled button — focus already gone —
          with a spinner turning inside it. */}
      <div style={stack}>
        <span className="t-label">Refused wins over busy, both ways round</span>
        <div style={row}>
          <Button variant="primary" disabled pending>
            Save
          </Button>
          <Button variant="primary" pending reason="Connect an inbox first.">
            Send
          </Button>
        </div>
      </div>
      {/* The federated variant, in the column it is shaped for: full width is
          the whole point of it, and a wrapping row of them would hide that. All
          three states the sign-in surface draws, one under the other, because
          the pair of dims is only legible as a comparison — a live door, one
          dimmed while the password form beside it is writing and coming back,
          and one the installation advertises with nothing behind it yet.

          Each mark keeps its own company's colours. That is the one place in
          this product where a colour is not a token, and it is also why neither
          dim state grayscales anything: fading a control is ours to do,
          recolouring somebody else's logo is not. */}
      <div style={stack}>
        <span className="t-label">
          Federated sign-in — offered, in flight, unavailable
        </span>
        <div style={{ ...stack, gap: "0.5rem", maxInlineSize: "20rem" }}>
          <Button variant="federated">
            <ProviderMark providerKey="google" />
            Continue with Google
          </Button>
          <Button variant="federated" disabled>
            <ProviderMark providerKey="google" />
            Continue with Google
          </Button>
          <Button variant="federated" unavailable>
            <ProviderMark providerKey="microsoft" />
            Continue with Microsoft
          </Button>
        </div>
      </div>
    </div>
  ),
};

export const Badges: Story = {
  render: () => (
    <div style={{ display: "flex", gap: "0.75rem", alignItems: "center" }}>
      <Badge tone="success">Active</Badge>
      <Badge tone="warn">Pending</Badge>
      <Badge tone="danger">Overdue</Badge>
      <Badge tone="ai">AI</Badge>
      <Badge tone="accent">Rep</Badge>
      {/* The quiet spelling, for a column of statuses where filled pills read
          as decoration. Same tone vocabulary, no fill. */}
      <Badge quiet>Open</Badge>
      <Badge tone="danger" quiet>
        16 days overdue
      </Badge>
      <Badge tone="success" quiet>
        Paid 22 days late
      </Badge>
    </div>
  ),
};

// The chip is an IDENTIFIER, so the states that matter are the ones where two
// chips have to be told apart or recognised as the same record. The old story
// was three default-size untinted chips, which showed neither: it could not
// have caught that four sizes existed for a two-value prop, that the tint was
// opt-in so a company changed colour between its list row and its own page, or
// that a name with no space in it produced a single letter.
export const Avatars: Story = {
  render: () => (
    <div style={stack}>
      <div style={stack}>
        <span className="t-label">The four sizes</span>
        <div style={row}>
          <Avatar name="Alice Müller" size="xs" />
          <Avatar name="Alice Müller" size="sm" />
          <Avatar name="Alice Müller" size="md" />
          <Avatar name="Alice Müller" size="lg" />
        </div>
      </div>
      <div style={stack}>
        <span className="t-label">
          Six tones, picked from the record and never stored
        </span>
        <div style={row}>
          <Avatar name="Alice Müller" />
          <Avatar name="Bob Schmidt" />
          <Avatar name="Carol Wagner" />
          <Avatar name="Voltaq Systems" />
          <Avatar name="Northwind Handel" />
          <Avatar name="Dara O'Brien" />
        </div>
      </div>
      <div style={stack}>
        <span className="t-label">
          The names a monogram rule usually gets wrong
        </span>
        <div style={row}>
          <Avatar name="jane.doe@example.com" />
          <Avatar name="Müller" />
          <Avatar name="van der Berg" />
          <Avatar name="李" />
          <Avatar name="Ana-Sofía Ruiz" />
        </div>
      </div>
      <div style={stack}>
        <span className="t-label">
          Same record, same colour — keyed on an id, so a rename does not move
          it
        </span>
        <div style={row}>
          <Avatar identity="org_7f3" name="Voltaq Systems" />
          <Avatar identity="org_7f3" name="Voltaq Systems GmbH" size="md" />
        </div>
      </div>
      <div style={stack}>
        <span className="t-label">
          A logo sits ON the monogram; a broken one leaves it standing
        </span>
        <div style={row}>
          <Avatar
            name="Northwind Handel"
            size="md"
            src="data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 64 64'%3E%3Ccircle cx='32' cy='32' r='30' fill='%230b7a53'/%3E%3C/svg%3E"
          />
          {/* A data URI that is a well-formed URL and not a decodable image, so
              the failure this story is about happens in the DECODER. A path
              that 404s would exercise the same fallback, but it also puts a
              failed request in the console, and the capture gate reads a failed
              request as a story that did not render clean. */}
          <Avatar
            name="Northwind Handel"
            size="md"
            src="data:image/png;base64,AAAA"
          />
        </div>
      </div>
      <div style={stack}>
        <span className="t-label">
          Stacked — the ring costs the chip no size, so a folded face sits on
          the same line as a lone one
        </span>
        <div style={row}>
          <AvatarStack
            people={[
              { name: "Alice Müller" },
              { name: "Bob Schmidt" },
              { name: "Carol Wagner" },
              { name: "Dara O'Brien" },
              { name: "Eve Lindqvist" },
              { name: "Frank Osei" },
            ]}
          />
          <Avatar name="Alice Müller" />
        </div>
      </div>
    </div>
  ),
};

// The field controls in one column, which is the point of the story: a text
// input, a dropdown and a textarea stacked the way a form stacks them is the
// only way to see that their type size, padding, height and focus ring actually
// agree. Reviewed one at a time they always look fine.
//
// The dropdown is the Select from select.tsx, and it is here for exactly that
// comparison — its own states live in select.stories.tsx.
export const Fields: Story = {
  render: () => <FieldsRow />,
};

function FieldsRow() {
  const [stage, setStage] = useState("proposal");
  return (
    <div className="form-stack" style={{ maxWidth: "22rem" }}>
      <Field label="Deal name">
        {(control) => <TextInput {...control} defaultValue="Globex renewal" />}
      </Field>
      <Field label="Stage" required>
        {(control) => (
          <Select
            {...control}
            options={[
              { value: "qualify", label: "Qualify" },
              { value: "proposal", label: "Proposal" },
              { value: "won", label: "Won" },
            ]}
            value={stage}
            onChange={setStage}
          />
        )}
      </Field>
      <Field label="Note" hint="Only the deal's followers will see this.">
        {(control) => (
          <Textarea
            {...control}
            rows={3}
            defaultValue="Renewal terms agreed on the call."
          />
        )}
      </Field>
    </div>
  );
}

// The three slots a field grew for the sign-in screens, which had forked their
// own field component to get them. A leading glyph and a trailing control both
// sit INSIDE one outline — that is the whole point, and the reason a second
// component existed: `.input-icon` could carry a glyph on the left and nothing
// on the right, so a reveal button had no way into the ring.
//
// The refusal is the other half. `error` is its own slot rather than a message
// pushed through `hint`, because the two say different things and were being
// spelled identically: a refused password rendered in the same meta-grey as the
// rule it broke, and on the change-password card in the same grey as the
// success line four elements above it.
export const FieldStates: Story = {
  name: "Fields — affordances and refusal",
  render: () => <FieldStatesColumn />,
};

function FieldStatesColumn() {
  const reveal = usePasswordReveal({
    show: "Show password",
    hide: "Hide password",
  });
  const revealShort = usePasswordReveal({
    show: "Show password",
    hide: "Hide password",
  });
  return (
    <div className="form-stack" style={{ maxWidth: "22rem" }}>
      <Field label="Work address" icon={<Mail aria-hidden />}>
        {(control) => (
          <TextInput
            {...control}
            type="email"
            defaultValue="ops@example.com"
            autoComplete="username"
          />
        )}
      </Field>
      <Field
        label="Password"
        icon={<Lock aria-hidden />}
        labelEnd={
          <button type="button" className="link-button">
            Forgot?
          </button>
        }
        hint="At least 12 characters."
        trailing={reveal.trailing}
      >
        {(control) => (
          <TextInput
            {...control}
            type={reveal.type}
            defaultValue="correct horse battery"
            autoComplete="current-password"
          />
        )}
      </Field>
      <Field
        label="New password"
        required
        error="Too short. Use at least 12 characters."
        trailing={revealShort.trailing}
      >
        {(control) => (
          <TextInput
            {...control}
            type={revealShort.type}
            defaultValue="short"
            autoComplete="new-password"
          />
        )}
      </Field>
      <Field
        label="Confirm"
        required
        error="These two don't match."
        hint="Both fields have to say the same thing."
      >
        {(control) => (
          <TextInput
            {...control}
            type="password"
            defaultValue="something else"
            autoComplete="new-password"
          />
        )}
      </Field>
    </div>
  );
}

// A disabled control and a long label that wraps — the two states a field
// catalog usually omits and a real form always reaches.
export const Toggles: Story = {
  render: () => (
    <div className="form-stack" style={{ maxWidth: "22rem" }}>
      <Checkbox label="Replace the existing link" defaultChecked />
      <Checkbox label="Include archived records" />
      <Checkbox label="Notify the deal owner" disabled />
      <fieldset className="field-multiselect">
        <legend className="t-label">Target</legend>
        <Radio name="quota-side" label="Owner" defaultChecked />
        <Radio name="quota-side" label="Team" />
      </fieldset>
    </div>
  ),
};

// The card surfaces plus the reading tile, together because the tile is
// what a card usually holds first and because the three tones only read as a
// system next to each other: the tile stays neutral and the VALUE takes the
// tone, which is invisible when a tinted tile is shown on its own.
//
// The third card is the shape most screens want and the reason the header is
// props rather than a hand-placed child: one title, one description under it,
// and the section's actions beside them.
export const Cards: Story = {
  render: () => (
    <div style={stack}>
      <Card>
        <p className="t-small">
          The standing surface: a card carries a section of a record.
        </p>
      </Card>
      <Card inset>
        <p className="t-small">
          The inset variant sits inside another surface, so it recedes instead
          of stacking a second raised edge on the first.
        </p>
      </Card>
      <Card
        title="Passports"
        sub="Credentials you minted for an agent. Every call re-authenticates, so a revoked passport stops working mid-session."
        actions={<Button small>Mint</Button>}
      >
        <p className="t-small">
          The header comes from props: title over description across the full
          width, actions beside the pair.
        </p>
      </Card>
      <div
        style={{
          display: "grid",
          gridTemplateColumns: "repeat(auto-fit, minmax(13rem, 1fr))",
          gap: "0.75rem",
        }}
      >
        <StatCard
          label="Account"
          value="Customer"
          detail="Renewal · Reseller"
        />
        <StatCard
          label="Engagement"
          value="Cooling"
          tone="warn"
          detail="Last inbound 12 Jun · last outbound 3 Jul"
        />
        <StatCard
          label="Commercial"
          value="4 open deals"
          tone="danger"
          detail="2 stalled for over 30 days"
        />
        <StatCard label="Owner" value="Carol Wagner" />
        {/* `numeric`: a money reading draws in the mono face, so the digits
            share one width and a column of figures lines up. The flag lives
            here rather than a wrapped node in the value, which would make the
            screen the author of type this tier owns. */}
        <StatCard
          label="Won lifetime"
          value="€1,284,500.00"
          numeric
          detail="Across 14 closed deals"
        />
      </div>
    </div>
  ),
};

// Loading and empty in one story: they are the same moment of a screen's life
// seen twice, and the pair is where the honest failure shows up — a skeleton
// that outlives the request and an empty state that says nothing useful both
// read as "broken" to the person waiting.
export const Placeholders: Story = {
  render: () => (
    <div style={stack}>
      <Card>
        <div
          style={{ display: "flex", flexDirection: "column", gap: "0.6rem" }}
        >
          <Skeleton width="40%" height={18} />
          <Skeleton width="100%" />
          <Skeleton width="86%" />
          <Skeleton width={140} height={10} />
        </div>
      </Card>
      <EmptyState>No deals match these filters yet.</EmptyState>
      <EmptyState
        title="No projects yet"
        action={
          <Button small variant="primary">
            New project
          </Button>
        }
      >
        <p>
          A project is the body of work a deal is about. It starts during the
          deal, in the initiative phase, and outlives close-won: delivery is
          tracked here after the pipeline has let go.
        </p>
      </EmptyState>
    </div>
  ),
};

// The section-level structure: a header that names a block, and a disclosure
// that hides one until asked for. Both states of the disclosure are here
// because the chevron is its only state indicator — a summary that looks the
// same open and closed is the defect this catalog has to make visible.
export const Sections: Story = {
  render: () => (
    <div style={stack}>
      <SectionHeader title="Pipeline" />
      <SectionHeader title="Pipeline" sub="Six open deals · 1.2M weighted" />
      {/* The description is a line of its own, so length is a reading matter
          rather than a layout one — beside the title this sentence used to push
          the heading around and then run out of room. */}
      <SectionHeader
        title="Reporting currency"
        sub="Every aggregate on this installation converts to it at the day's rate, and the rate that was used stays on the figure."
        actions={<Button small>Change</Button>}
      />
      <Card>
        {/* As the card's first child, which is the pairing atoms.css styles.
            Equivalent to passing title/sub to Card — that is what it renders. */}
        <SectionHeader title="Contacts" sub="Three people at this company" />
        <p className="t-small">Carol Wagner · Bob Schmidt · Alice Müller</p>
      </Card>
      {/* level={3} is a section INSIDE a section — a group of fields under the
          page's own h2. The type steps down with the outline: an inner heading
          drawn at its parent's size tells the eye they are peers while the
          document says they are not, and the eye is the one a reader
          believes. */}
      <Card>
        <SectionHeader title="Delivery" sub="Where webhooks are sent" />
        <SectionHeader title="Endpoints" level={3} />
        <p className="t-small">Two subscriptions, both healthy.</p>
        <SectionHeader title="Dead-lettered" level={3} />
        <p className="t-small">Nothing waiting.</p>
      </Card>
      <Disclosure summary="Matching rules">
        <p className="t-small">
          Closed by default: the reader pays one line for a surface they rarely
          open.
        </p>
      </Disclosure>
      <Disclosure summary="Import log" open>
        <p className="t-small">
          Forced open for a state the reader must not miss — a run in progress,
          or a result that just arrived.
        </p>
      </Disclosure>
    </div>
  ),
};

const RANGES = ["month", "quarter", "year"] as const;
type Range = (typeof RANGES)[number];
const RANGE_LABELS: Record<Range, string> = {
  month: "Month",
  quarter: "Quarter",
  year: "Year",
};

const SIDES = ["owner", "team"] as const;
type Side = (typeof SIDES)[number];
const SIDE_LABELS: Record<Side, string> = { owner: "Owner", team: "Team" };

// SegmentedControl is fully controlled, so the catalog has to own the state or
// the buttons never move.
function ToolbarDemo() {
  const [range, setRange] = useState<Range>("quarter");
  const [side, setSide] = useState<Side>("owner");
  return (
    <div style={stack}>
      <div style={{ ...row, justifyContent: "space-between" }}>
        <SegmentedControl
          options={RANGES}
          value={range}
          onChange={setRange}
          labels={RANGE_LABELS}
          label="Reporting range"
        />
        <span className="t-caption">
          Press <Kbd>/</Kbd> to search, <Kbd>Ctrl</Kbd> <Kbd>K</Kbd> for the
          command bar, <Kbd>Esc</Kbd> to close.
        </span>
      </div>
      <SegmentedControl
        options={SIDES}
        value={side}
        onChange={setSide}
        labels={SIDE_LABELS}
        label="Quota target"
      />
    </div>
  );
}

// The toolbar pair: the segmented switch that scopes a screen and the key
// legend that sits beside it. Two options and three options are both here —
// a two-up control has no middle segment, which is where the divider rules
// break if they were written for three.
export const Toolbar: Story = {
  render: () => <ToolbarDemo />,
};

type DemoDeal = {
  id: string;
  name: string;
  stage: string;
  weighted: string;
};

const DEMO_DEALS: DemoDeal[] = [
  {
    id: "dl_1",
    name: "Globex renewal",
    stage: "Proposal",
    weighted: "48,000 EUR",
  },
  {
    id: "dl_2",
    name: "Initech platform",
    stage: "Qualify",
    weighted: "12,500 EUR",
  },
  {
    id: "dl_3",
    name: "Umbrella expansion",
    stage: "Negotiation",
    weighted: "156,000 EUR",
  },
];

const DEAL_COLUMNS = [
  { key: "name", header: "Deal", render: (deal: DemoDeal) => deal.name },
  {
    key: "stage",
    header: "Stage",
    render: (deal: DemoDeal) => <Badge tone="accent">{deal.stage}</Badge>,
  },
  {
    key: "weighted",
    header: "Weighted",
    render: (deal: DemoDeal) => <span className="t-mono">{deal.weighted}</span>,
  },
];

// onRowClick is what turns a row into a link, so the story has to supply one
// and show that it fired — a cursor change alone is not evidence.
function DealTableDemo() {
  const [opened, setOpened] = useState<DemoDeal | null>(null);
  return (
    <div style={stack}>
      <DataTable
        label={"Deals"}
        columns={DEAL_COLUMNS}
        rows={DEMO_DEALS}
        rowKey={(deal) => deal.id}
        onRowClick={setOpened}
      />
      <span className="t-caption">
        {opened
          ? `Row opened: ${opened.name}`
          : "Click a row — onRowClick is what makes it a link."}
      </span>
    </div>
  );
}

// Rows and no rows. The empty table is the state a screen actually reaches
// first, and it is header-only by design: DataTable never invents a message,
// so the screen pairs it with an EmptyState of its own.
export const Tables: Story = {
  render: () => (
    <div style={stack}>
      <DealTableDemo />
      <SectionHeader title="No rows" sub="The same table with rows={[]}" />
      <DataTable
        label={"Deals"}
        columns={DEAL_COLUMNS}
        rows={[]}
        rowKey={(deal) => deal.id}
      />
      <EmptyState>No deals in this pipeline yet.</EmptyState>
    </div>
  ),
};

// All three bands, and the over-100% case with them: the arc caps at a full
// circle while the figure keeps reading the real number. The band is the
// server's, never re-derived from pct — showing 113% next to 72% and 41% is
// what makes that separation visible.
export const Attainment: Story = {
  render: () => (
    <div style={row}>
      <AttainmentRing pct={113} band="met" caption="attained" />
      <AttainmentRing pct={72} band="accent" caption="attained" />
      <AttainmentRing pct={41} band="behind" caption="attained" />
    </div>
  ),
};

// Open on mount, because a dialog rendered closed screenshots as an empty
// canvas. The trigger stays so the reader can reopen it after dismissing.
function ModalDemo() {
  const [open, setOpen] = useState(true);
  const titleId = useId();
  return (
    <>
      <Button variant="primary" onClick={() => setOpen(true)}>
        Open the dialog
      </Button>
      <Modal open={open} onClose={() => setOpen(false)} labelledBy={titleId}>
        <h2
          id={titleId}
          className="t-h2"
          style={{ marginBottom: "var(--space-3)" }}
        >
          Merge these companies?
        </h2>
        <p className="t-small">
          Globex GmbH keeps its record; the duplicate's activities, deals and
          people move onto it. This cannot be undone.
        </p>
        <div className="actions">
          <Button onClick={() => setOpen(false)}>Cancel</Button>
          <Button variant="danger" onClick={() => setOpen(false)}>
            Merge
          </Button>
        </div>
      </Modal>
    </>
  );
}

export const Dialog: Story = {
  render: () => <ModalDemo />,
};

// OverflowMenu owns its open state and mounts its items only after the first
// open, so a story that merely renders it is a lone button in the canvas.
// Pressing the trigger on mount is the only way to catalog the panel without
// giving the component a prop it does not have.
function OverflowMenuDemo() {
  const wrap = useRef<HTMLDivElement>(null);
  const pressed = useRef(false);
  useEffect(() => {
    // The trigger TOGGLES, so this must happen exactly once — a mount effect
    // invoked twice would open the menu and close it again.
    if (pressed.current) {
      return;
    }
    pressed.current = true;
    wrap.current
      ?.querySelector<HTMLButtonElement>(".overflow-menu-trigger")
      ?.click();
  }, []);
  return (
    // The panel is absolutely positioned and right-aligned to its trigger, the
    // way a record header carries it — so the story puts the trigger at the
    // right edge (the panel opens inward, not off the page) and reserves the
    // height it drops into.
    <div
      ref={wrap}
      style={{
        display: "flex",
        justifyContent: "flex-end",
        alignItems: "flex-start",
        minHeight: "14rem",
      }}
    >
      <OverflowMenu label="More actions">
        <Button>Merge with…</Button>
        <Button>Export</Button>
        <Button variant="danger">Archive</Button>
      </OverflowMenu>
    </div>
  );
}

export const Overflow: Story = {
  render: () => <OverflowMenuDemo />,
};

// placement="right" is the drawer form of the SAME Modal — full height on the
// right edge, with the record behind it still legible. Catalogued because the
// centred dialog above is what everyone pictures when they read "Modal", and
// the two are one component with one prop between them.
function DrawerDemo() {
  const [open, setOpen] = useState(true);
  const titleId = useId();
  return (
    <>
      {/* Something behind the drawer, because "the record stays legible" is
          the whole claim the placement makes and an empty canvas cannot show
          it being kept. */}
      <SectionHeader title="Globex GmbH" sub="Enterprise · Munich" />
      <p className="t-body">
        Anna Brandt replied on Tuesday and is waiting on pricing. Nobody has
        written since.
      </p>
      <Button variant="primary" onClick={() => setOpen(true)}>
        Open the drawer
      </Button>
      <Modal
        open={open}
        onClose={() => setOpen(false)}
        labelledBy={titleId}
        placement="right"
      >
        <h2
          id={titleId}
          className="t-h2"
          style={{ marginBottom: "var(--space-3)" }}
        >
          Write to Anna Brandt
        </h2>
        <p className="t-small">
          The draft sits beside the record it is about, so a rep can read the
          history while writing rather than remembering it.
        </p>
        <div className="actions">
          <Button onClick={() => setOpen(false)}>Discard</Button>
          <Button variant="primary" onClick={() => setOpen(false)}>
            Send
          </Button>
        </div>
      </Modal>
    </>
  );
}

export const Drawer: Story = {
  render: () => <DrawerDemo />,
};

// SearchField had no node of its own: it appeared only inside RecordPicker's
// story, where it reads as part of that composite rather than as the one
// spelling of a search input. Both states are here because the affordance is
// the icon and the type="search" clear control, and a filled field is the only
// way to see the second.
export const Search: Story = {
  render: () => (
    <div style={{ display: "grid", gap: "var(--space-3)", maxWidth: "22rem" }}>
      <Field label="Find a company">
        {(control) => <SearchField {...control} placeholder="Search…" />}
      </Field>
      <Field label="Find a person">
        {(control) => <SearchField {...control} defaultValue="Anna Brandt" />}
      </Field>
    </div>
  ),
};
