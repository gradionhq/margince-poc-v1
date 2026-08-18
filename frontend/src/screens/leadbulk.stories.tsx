// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { LeadBulkBar } from "./leadbulk";
import { installFetchStub, jsonResponse, StoryProviders } from "./story-utils";

// The bulk bar over selected leads: the count, the owner picker, the two
// verbs. It renders inside ListTable's bulk-bar slot on the leads list; here
// it stands alone over two selected rows so its states are visible.
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
    status: "working" as const,
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

export const TwoSelected: Story = {
  render: () => {
    installFetchStub({
      "GET /users": () =>
        jsonResponse({
          data: [
            { id: "u-1", email: "lena@x.test", display_name: "Lena Fischer" },
            { id: "u-2", email: "mia@x.test", display_name: "Mia Berg" },
          ],
          page: { next_cursor: null, has_more: false },
        }),
    });
    return (
      <StoryProviders>
        <div className="lt-bulkbar">
          <LeadBulkBar leads={leads} onDone={() => undefined} />
        </div>
      </StoryProviders>
    );
  },
};
