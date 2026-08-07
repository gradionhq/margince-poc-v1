// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { Button, SegmentedControl } from "./atoms";
import {
  type BoardColumn,
  type BoardDeal,
  PipelineBoard,
  RecordView,
  type TimelineEntry,
} from "./composed";
import { ListSurface } from "./listsurface";
import { Select } from "./select";

// RecordView's timeline gained an optional per-row `actions` slot (the Reply /
// Relink cluster the 360 screens mount). These stories exercise both shapes:
// rows without an affordance render exactly as before, and rows that carry an
// action node get the right-aligned slot — so a render regression in either
// path is caught here rather than only in the screen that composes it.

const emailEntry: TimelineEntry = {
  id: "a1",
  kind: "email",
  title: "Re: Q3 renewal terms",
  atIso: "2026-07-01T09:12:00Z",
  provenance: { kind: "human", self: true },
};
const meetingEntry: TimelineEntry = {
  id: "a2",
  kind: "meeting",
  title: "Discovery call",
  atIso: "2026-06-24T14:00:00Z",
  provenance: { kind: "agent", agent: "capture" },
};
const noteEntry: TimelineEntry = {
  id: "a3",
  kind: "note",
  title: "Left a voicemail",
  atIso: "2026-06-20T16:30:00Z",
  provenance: { kind: "human", self: true },
};
const baseTimeline: TimelineEntry[] = [emailEntry, meetingEntry, noteEntry];

const meta: Meta<typeof RecordView> = {
  title: "Design System/RecordView",
  component: RecordView,
};
export default meta;

type Story = StoryObj<typeof RecordView>;

// The unchanged shape: no row carries an action, so every entry renders as it
// did before the slot existed.
export const Default: Story = {
  args: {
    name: "Acme GmbH",
    subtitle: "Enterprise · Munich",
    zone: "Europe/Berlin",
    timeline: baseTimeline,
  },
};

// The new slot: the email row carries a Reply action, the meeting row a Relink
// action, and the note row none — the right-aligned cluster only appears where
// an affordance is supplied.
export const WithRowActions: Story = {
  args: {
    name: "Acme GmbH",
    subtitle: "Enterprise · Munich",
    zone: "Europe/Berlin",
    timeline: [
      {
        ...emailEntry,
        actions: (
          <Button small onClick={() => {}}>
            Reply
          </Button>
        ),
      },
      {
        ...meetingEntry,
        actions: (
          <Button small onClick={() => {}}>
            Relink
          </Button>
        ),
      },
      noteEntry,
    ],
  },
};

// PipelineBoard inside ListSurface (design-system/listsurface.tsx) — the same
// shell the record tables render into, so the board's header, count and tools
// row read exactly as a table's would. Four open stages plus one won stage,
// and Proposal carries no deals — the honest empty-column case a stage sees
// between a lead qualifying and the next one reaching it.
function boardDeal(
  id: string,
  name: string,
  valueMinor: number,
  ageDays: number,
  extra?: Partial<BoardDeal>,
): BoardDeal {
  return {
    id,
    name,
    org: "Acme GmbH",
    valueMinor,
    currency: "EUR",
    ageMs: ageDays * 24 * 60 * 60 * 1000,
    ...extra,
  };
}

const boardColumns: BoardColumn[] = [
  {
    stage: "discovery",
    label: "Discovery",
    probabilityPct: 10,
    rawMinor: 450_00,
    weightedMinor: 45_00,
    currency: "EUR",
    deals: [
      boardDeal("d1", "Contoso renewal", 120_00, 3),
      boardDeal("d2", "Fabrikam expansion", 330_00, 9, { stalled: true }),
    ],
  },
  {
    stage: "qualified",
    label: "Qualified",
    probabilityPct: 30,
    rawMinor: 280_00,
    weightedMinor: 84_00,
    currency: "EUR",
    deals: [boardDeal("d3", "Globex onboarding", 280_00, 14)],
  },
  {
    stage: "proposal",
    label: "Proposal",
    probabilityPct: 60,
    rawMinor: 0,
    weightedMinor: 0,
    currency: "EUR",
    deals: [],
  },
  {
    stage: "negotiation",
    label: "Negotiation",
    probabilityPct: 80,
    rawMinor: 540_00,
    weightedMinor: 432_00,
    currency: "EUR",
    deals: [
      boardDeal("d4", "Initech upgrade", 540_00, 21, {
        singleThreaded: true,
      }),
    ],
  },
  {
    stage: "won",
    label: "Closed Won",
    probabilityPct: 100,
    rawMinor: 610_00,
    weightedMinor: 610_00,
    currency: "EUR",
    deals: [boardDeal("d5", "Umbrella Corp", 610_00, 2)],
  },
];

export const BoardInSurface: StoryObj = {
  render: () => (
    <ListSurface
      count="5 deals"
      search={{ value: "", onChange: () => undefined }}
      action={<Button small>New deal</Button>}
      // The board brings its own controls rather than the table's: which
      // pipeline is shown, and whether it is read as stages or as rows.
      tools={
        <>
          <SegmentedControl
            options={["board", "table"] as const}
            value="board"
            onChange={() => undefined}
            labels={{ board: "Board", table: "Table" }}
          />
          <Select
            className="input"
            aria-label="Pipeline"
            value="sales"
            onChange={() => undefined}
            options={[
              { value: "sales", label: "Sales" },
              { value: "partner", label: "Partner" },
            ]}
          />
        </>
      }
      // The stage and company filters read as the same chip as any other,
      // even though a pipeline named their options rather than a catalog.
      chips={[
        {
          key: "stage_id",
          label: "Stage",
          allLabel: "All stages",
          options: [
            { value: "discovery", label: "Discovery" },
            { value: "qualified", label: "Qualified" },
          ],
        },
        {
          key: "organization_id",
          label: "Company",
          allLabel: "All companies",
          options: [{ value: "acme", label: "Acme GmbH" }],
        },
      ]}
    >
      <PipelineBoard columns={boardColumns} />
    </ListSurface>
  ),
};
