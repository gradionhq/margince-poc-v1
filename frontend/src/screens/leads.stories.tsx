// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { LeadScreen, LeadsScreen } from "./leads";
import { installFetchStub, jsonResponse, StoryProviders } from "./story-utils";

// LeadsScreen (list, accent-tinted "segregated" surface) and LeadScreen (its
// own 360 — never person.html, per the §3.5 segregation gap) both read
// through the api client on mount; LeadScreen's lifecycle panel also reads
// GET /me (the session-principal probe every role-aware surface shares).
const meta: Meta = {
  title: "Screens/Leads",
  parameters: { layout: "padded" },
};
export default meta;

type Story = StoryObj;

const lead = {
  id: "l-1",
  workspace_id: "w-1",
  full_name: "Jonas Petersen",
  email: "jonas@nordwind.example",
  company_name: "Nordwind Logistik",
  status: "working" as const,
  score: 72,
  source: "manual",
  captured_by: "human:u1",
  version: 1,
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
};

export const LeadsList: Story = {
  render: () => {
    installFetchStub({
      "GET /leads": () =>
        jsonResponse({
          data: [lead],
          page: { next_cursor: null, has_more: false },
        }),
    });
    return (
      <StoryProviders>
        <LeadsScreen />
      </StoryProviders>
    );
  },
};

export const LeadOverview: Story = {
  render: () => {
    installFetchStub({
      "GET /leads/l-1": () => jsonResponse(lead),
      "GET /me": () =>
        jsonResponse({
          user: { id: "u-9", display_name: "Me" },
          roles: ["rep"],
          teams: [],
        }),
    });
    return (
      <StoryProviders>
        <LeadScreen id="l-1" />
      </StoryProviders>
    );
  },
};
