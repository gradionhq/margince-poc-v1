// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { userEvent, within } from "storybook/test";
import { LeadBulkBar } from "./leadbulk";
import { installFetchStub, jsonResponse, StoryProviders } from "./story-utils";

// The bulk bar over selected leads: the count, the owner picker, the reason
// picker, the two verbs. It renders inside ListTable's bulk-bar slot on the
// leads list; here it stands alone over two selected rows so its states are
// visible.
//
// Disqualify is REFUSED until a reason is chosen, so the bar's resting state
// carries a sentence under that verb — `ReasonPicked` is the same bar with the
// reason answered and the verb live, which is the state a screenshot of the
// happy path has to show.
const meta: Meta = {
  title: "Records/Leads/Bulk bar",
  parameters: { layout: "padded" },
};
export default meta;

type Story = StoryObj;

const leads = [
  {
    id: "l-1",
    full_name: "Jonas Petersen",
    email: "jonas@nordwind.example",
    status: "contacted" as const,
    score: 72,
    source: "manual",
    captured_by: "human:u1",
    version: 3,
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
  },
  {
    id: "l-2",
    full_name: "Otto Fischer",
    email: "otto@fischer.example",
    status: "new" as const,
    score: 40,
    source: "webform",
    captured_by: "human:u1",
    version: 7,
    created_at: "2026-01-02T00:00:00Z",
    updated_at: "2026-01-02T00:00:00Z",
  },
];

// The administered close reasons, as a fresh installation ships them. The
// retired row is served too: an inactive reason may not be applied to a new
// close, so the bar must not offer it.
const reasons = [
  { id: "r-1", label: "Not a fit", active: true },
  { id: "r-2", label: "No budget", active: true },
  { id: "r-3", label: "Duplicate", active: true },
  { id: "r-4", label: "Wrong region (retired)", active: false },
].map((reason, i) => ({
  ...reason,
  sort_order: (i + 1) * 10,
  system: true,
  lead_count: 0,
  version: 1,
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
}));

function stubBulkRoutes(): void {
  installFetchStub({
    "GET /users": () =>
      jsonResponse({
        data: [
          { id: "u-1", email: "lena@x.test", display_name: "Lena Fischer" },
          { id: "u-2", email: "mia@x.test", display_name: "Mia Berg" },
        ],
        page: { next_cursor: null, has_more: false },
      }),
    "GET /lead-disqualify-reasons": () => jsonResponse({ data: reasons }),
  });
}

function Bar() {
  return (
    <StoryProviders>
      <div className="lt-bulkbar">
        <LeadBulkBar leads={leads} onDone={() => undefined} />
      </div>
    </StoryProviders>
  );
}

export const TwoSelected: Story = {
  render: () => {
    stubBulkRoutes();
    return <Bar />;
  },
};

export const ReasonPicked: Story = {
  render: () => {
    stubBulkRoutes();
    return <Bar />;
  },
  // The listbox is portalled to document.body, so the option is not inside
  // the story canvas — reaching for it through `canvasElement` finds nothing.
  play: async ({ canvasElement }) => {
    await userEvent.click(
      await within(canvasElement).findByLabelText("Reason"),
    );
    await userEvent.click(
      await within(document.body).findByRole("option", { name: "No budget" }),
    );
  },
};

// Nothing administered to answer with: the picker is empty and the verb stays
// refused. Better than a batch closed with a null reason, and the state an
// operator sees if they retire every reason in Settings › Data model.
export const NoReasonsAdministered: Story = {
  render: () => {
    installFetchStub({
      "GET /users": () =>
        jsonResponse({
          data: [],
          page: { next_cursor: null, has_more: false },
        }),
      "GET /lead-disqualify-reasons": () => jsonResponse({ data: [] }),
    });
    return <Bar />;
  },
};
