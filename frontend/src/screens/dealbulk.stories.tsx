// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import type { components } from "../api/schema";
import { DealBulkBar } from "./dealbulk";
import { installFetchStub, jsonResponse, StoryProviders } from "./story-utils";

// The bar that appears under the deals table while rows are selected. It reads
// the user roster for the owner picker; everything else it needs is passed in,
// so these stories are the bar's own states rather than the table's.
const meta: Meta = {
  title: "Records/Deal bulk bar",
  parameters: { layout: "padded" },
};
export default meta;

type Story = StoryObj;
type Deal = components["schemas"]["Deal"];
type Stage = components["schemas"]["Stage"];

const stages: Stage[] = [
  {
    id: "s1",
    pipeline_id: "pl",
    name: "Qualify",
    position: 1,
    semantic: "open",
    win_probability: 20,
  },
  {
    id: "s2",
    pipeline_id: "pl",
    name: "Proposal",
    position: 2,
    semantic: "open",
    win_probability: 40,
  },
  // Terminal stages are deliberately absent from the picker: closing a deal
  // asks for a reason and freezes a rate, which is not a bulk gesture.
  {
    id: "s3",
    pipeline_id: "pl",
    name: "Won",
    position: 3,
    semantic: "won",
    win_probability: 100,
  },
];

function deal(id: string, name: string): Deal {
  return {
    id,
    name,
    amount_minor: 4_800_000,
    currency: "EUR",
    pipeline_id: "pl",
    stage_id: "s1",
    status: "open",
    source: "manual",
    captured_by: "human:u1",
    version: 4,
    created_at: "2026-06-01T00:00:00Z",
    updated_at: "2026-06-01T00:00:00Z",
  } as Deal;
}

const roster = {
  data: [
    {
      id: "u-1",
      email: "mila@acme.test",
      display_name: "Mila Brandt",
      timezone: "UTC",
      status: "active",
      is_agent: false,
    },
  ],
  page: { next_cursor: null, has_more: false },
};

export const Selected: Story = {
  render: () => {
    installFetchStub({ "GET /users": () => jsonResponse(roster) });
    return (
      <StoryProviders>
        <div className="lt-bulkbar">
          <DealBulkBar
            deals={[deal("d1", "Fleet retrofit"), deal("d2", "Depot rollout")]}
            stages={stages}
            onDone={() => {}}
          />
        </div>
      </StoryProviders>
    );
  },
};

// The German copy, whose length is the thing worth looking at here: three
// verbs and two pickers share one row.
export const German: Story = {
  render: () => {
    installFetchStub({ "GET /users": () => jsonResponse(roster) });
    return (
      <StoryProviders locale="de">
        <div className="lt-bulkbar">
          <DealBulkBar
            deals={[deal("d1", "Fleet retrofit"), deal("d2", "Depot rollout")]}
            stages={stages}
            onDone={() => {}}
          />
        </div>
      </StoryProviders>
    );
  },
};

// A roster whose walk never runs out of cursor, so it stops at its page budget
// and the bar owes the reader the caveat. The state to LOOK at is where that
// sentence lands: it is the last item in the row, so the row can still wrap
// between groups but never between the owner picker and the button that assigns
// it. German, because its string is the longest and moves the wrap point.
const truncatedRoster = {
  ...roster,
  page: { next_cursor: "next", has_more: true },
};

export const GermanPartialRoster: Story = {
  render: () => {
    installFetchStub({ "GET /users": () => jsonResponse(truncatedRoster) });
    return (
      <StoryProviders locale="de">
        <div className="lt-bulkbar">
          <DealBulkBar
            deals={[deal("d1", "Fleet retrofit"), deal("d2", "Depot rollout")]}
            stages={stages}
            onDone={() => {}}
          />
        </div>
      </StoryProviders>
    );
  },
};
