// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { userEvent, within } from "storybook/test";
import { LeadManualSignals } from "./leadsignals";
import { installFetchStub, jsonResponse, StoryProviders } from "./story-utils";

// The human half of the lead score: three plain questions a rep can answer in
// one pass, and the provenance the wire still needs behind "More…".
//
// The shape before this asked for Factor / Value / Evidence quality /
// Confidence / Why as five equal fields, and opened by asking a rep to grade
// their own evidence — which is a form a rep abandons. The provenance did not
// leave the wire; it stopped being the first thing asked.
//
// Both halves are stories here on purpose. `Collapsed` is what a rep meets, and
// `ProvenanceOpen` is the state a reviewer has to be able to see, because what
// the disclosure hides is what the unopened form SENDS.
const meta: Meta = {
  title: "Records/Leads/Manual signals",
  parameters: { layout: "padded" },
};
export default meta;

type Story = StoryObj;

// One stored signal, so the list above the questions is not empty: a rep reads
// what is already on the score before adding to it, and the row carries its own
// reason and author because those are why the score reads as it does.
const stored = [
  {
    factor: "budget_hint",
    band: "some",
    points: 4,
    signal_kind: "fact",
    confidence: 0.9,
    reason: "CFO named a Q4 line item",
    set_by: "u-9",
    set_at: "2026-06-04T09:00:00Z",
  },
];

function stubSignalRoutes(): void {
  installFetchStub({
    // A literal path, not a template: the stub keys on the request's own
    // pathname, so `{id}` would route nothing and the fallback's empty page
    // would quietly stand in for the list.
    "GET /leads/l-1/manual-signals": () => jsonResponse({ data: stored }),
    "GET /users": () =>
      jsonResponse({
        data: [{ id: "u-9", email: "lena@x.test", display_name: "Lena F." }],
        page: { next_cursor: null, has_more: false },
      }),
  });
}

function Panel({ readOnlyReason }: Readonly<{ readOnlyReason?: string }>) {
  return (
    <StoryProviders>
      <LeadManualSignals id="l-1" readOnlyReason={readOnlyReason} />
    </StoryProviders>
  );
}

export const Collapsed: Story = {
  render: () => {
    stubSignalRoutes();
    return <Panel />;
  },
};

// What the rep never opens is what the unopened form sends: `assumption`, the
// weakest claim the contract's kind enum can make, and no confidence at all.
export const ProvenanceOpen: Story = {
  render: () => {
    stubSignalRoutes();
    return <Panel />;
  },
  play: async ({ canvasElement }) => {
    await userEvent.click(await within(canvasElement).findByText("More…"));
  },
};

// A terminal lead keeps its stored signals and loses the questions. The inputs a
// rep made are part of why the lead was worked, so hiding them on a closed lead
// would hide the reasoning rather than the controls.
export const Closed: Story = {
  render: () => {
    stubSignalRoutes();
    return <Panel readOnlyReason="This lead is closed and takes no changes." />;
  },
};

// Nothing on the score yet: the three questions are the whole surface, which is
// the state a fresh lead opens in.
export const NothingStoredYet: Story = {
  render: () => {
    installFetchStub({
      "GET /leads/l-1/manual-signals": () => jsonResponse({ data: [] }),
      "GET /users": () =>
        jsonResponse({
          data: [],
          page: { next_cursor: null, has_more: false },
        }),
    });
    return <Panel />;
  },
};
