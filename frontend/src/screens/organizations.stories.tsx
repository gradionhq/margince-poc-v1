// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { CompaniesScreen, CompanyScreen } from "./organizations";
import { installFetchStub, jsonResponse, StoryProviders } from "./story-utils";

// CompaniesScreen (list) and CompanyScreen (360 Overview) both read through
// the api client on mount — fixtures mirror organizations.test.tsx's `org`
// plus the dormant-strength default the Overview tab always fires.
const meta: Meta = {
  title: "Screens/Organizations",
  parameters: { layout: "padded" },
};
export default meta;

type Story = StoryObj;

const org = {
  id: "o-1",
  workspace_id: "w-1",
  display_name: "Brandt Automotive GmbH",
  industry: "Automotive",
  size_band: "201-500",
  domains: [{ domain: "brandt.example", is_primary: true }],
  captured_by: "human:u1",
  source: "manual",
  version: 1,
};

const dormantStrength = {
  score: 0,
  bucket: "dormant",
  factors: { recency: 0, frequency: 0, reciprocity: 0, direction: 0 },
  last_interaction: null,
};

export const CompaniesList: Story = {
  render: () => {
    installFetchStub({
      "GET /organizations": () =>
        jsonResponse({
          data: [org],
          page: { next_cursor: null, has_more: false },
        }),
    });
    return (
      <StoryProviders>
        <CompaniesScreen />
      </StoryProviders>
    );
  },
};

export const CompanyOverview: Story = {
  render: () => {
    installFetchStub({
      "GET /organizations/o-1": () => jsonResponse(org),
      "GET /organizations/o-1/strength": () => jsonResponse(dormantStrength),
      "GET /activities": () => jsonResponse({ data: [] }),
    });
    return (
      <StoryProviders>
        <CompanyScreen id="o-1" />
      </StoryProviders>
    );
  },
};
