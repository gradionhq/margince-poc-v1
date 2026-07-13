// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { RelationshipsTab } from "./relationships";
import { installFetchStub, jsonResponse, StoryProviders } from "./story-utils";

// RelationshipsTab reads GET /relationships?person_id=… (there is no
// GET /relationships/{id} in the contract — every row is hydrated straight
// off the list read). The fixture mirrors people.test.tsx's employmentRel.
const meta: Meta = {
  title: "Screens/Relationships",
  parameters: { layout: "padded" },
};
export default meta;

type Story = StoryObj;

const employmentRel = {
  id: "rel-1",
  workspace_id: "w-1",
  kind: "employment",
  person_id: "p-1",
  organization_id: "o-1",
  role: "cto",
  is_current_primary: true,
  started_at: "2024-01-01",
  ended_at: null,
  source: "manual",
  captured_by: "human:u1",
  version: 1,
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
};

const partnerOfRel = {
  ...employmentRel,
  id: "rel-2",
  kind: "partner_of",
  role: "referral partner",
  organization_id: "o-2",
};

export const WithRelationships: Story = {
  render: () => {
    installFetchStub({
      "GET /relationships": () =>
        jsonResponse({
          data: [employmentRel, partnerOfRel],
          page: { next_cursor: null, has_more: false },
        }),
    });
    return (
      <StoryProviders>
        <RelationshipsTab scope={{ person_id: "p-1" }} />
      </StoryProviders>
    );
  },
};

export const Empty: Story = {
  render: () => {
    installFetchStub({
      "GET /relationships": () =>
        jsonResponse({
          data: [],
          page: { next_cursor: null, has_more: false },
        }),
    });
    return (
      <StoryProviders>
        <RelationshipsTab scope={{ person_id: "p-1" }} />
      </StoryProviders>
    );
  },
};
