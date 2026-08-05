import type { Meta, StoryObj } from "@storybook/react-vite";
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
  SectionHeader,
  SegmentedControl,
  Select,
  Skeleton,
  StatCard,
  Textarea,
  TextInput,
} from "./atoms";

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

export const Buttons: Story = {
  render: () => (
    <div style={{ display: "flex", gap: "0.75rem", alignItems: "center" }}>
      <Button variant="primary">Save</Button>
      <Button variant="ghost">Cancel</Button>
      <Button variant="danger">Delete</Button>
      <Button variant="primary" small>
        Small
      </Button>
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
    </div>
  ),
};

export const Avatars: Story = {
  render: () => (
    <div style={{ display: "flex", gap: "0.75rem", alignItems: "center" }}>
      <Avatar name="Alice Müller" />
      <Avatar name="Bob Schmidt" />
      <Avatar name="Carol Wagner" />
    </div>
  ),
};

// The four field controls in one column, which is the point of the story: a
// text input, a dropdown and a textarea stacked the way a form stacks them is
// the only way to see that their type size, padding, height and focus ring
// actually agree. Reviewed one at a time they always look fine.
export const Fields: Story = {
  render: () => (
    <div className="form-stack" style={{ maxWidth: "22rem" }}>
      <Field label="Deal name">
        {(control) => <TextInput {...control} defaultValue="Globex renewal" />}
      </Field>
      <Field label="Stage" required>
        {(control) => (
          <Select {...control} defaultValue="proposal">
            <option value="qualify">Qualify</option>
            <option value="proposal">Proposal</option>
            <option value="won">Won</option>
          </Select>
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
  ),
};

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

// The two card surfaces plus the reading tile, together because the tile is
// what a card usually holds first and because the three tones only read as a
// system next to each other: the tile stays neutral and the VALUE takes the
// tone, which is invisible when a tinted tile is shown on its own.
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
      <Card>
        {/* As the card's first child, which is the pairing atoms.css styles. */}
        <SectionHeader title="Contacts" sub="Three people at this company" />
        <p className="t-small">Carol Wagner · Bob Schmidt · Alice Müller</p>
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
      <DataTable columns={DEAL_COLUMNS} rows={[]} rowKey={(deal) => deal.id} />
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
