// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { PipelinesCard } from "./settings";
import { installFetchStub, jsonResponse, StoryProviders } from "./story-utils";

// PipelinesCard (D-8) reads GET /me (roles → canConfigureAutomations gate) and
// GET /pipelines (the ["pipelines","all"] list). Both stubbed here so the card
// renders off fixtures — never a live call. Admin sees the write affordances;
// a rep sees the same list read-only (server stays the RBAC authority).
const meta: Meta = {
  title: "Screens/Settings",
  parameters: { layout: "padded" },
};
export default meta;

type Story = StoryObj;

const pipelines = {
  data: [
    {
      id: "pl",
      workspace_id: "w",
      name: "Sales",
      is_default: true,
      position: 0,
      stages: [
        {
          id: "s1",
          workspace_id: "w",
          pipeline_id: "pl",
          name: "Qualify",
          position: 1,
          semantic: "open",
          win_probability: 20,
        },
        {
          id: "s2",
          workspace_id: "w",
          pipeline_id: "pl",
          name: "Proposal",
          position: 2,
          semantic: "open",
          win_probability: 50,
        },
        {
          id: "s3",
          workspace_id: "w",
          pipeline_id: "pl",
          name: "Won",
          position: 3,
          semantic: "won",
          win_probability: 100,
        },
      ],
    },
  ],
  page: { next_cursor: null, has_more: false },
};

function me(roles: string[]) {
  return {
    user: { id: "u-1", display_name: "Me" },
    roles,
    teams: [],
  };
}

export const Admin: Story = {
  render: () => {
    installFetchStub({
      "GET /me": () => jsonResponse(me(["admin"])),
      "GET /pipelines": () => jsonResponse(pipelines),
    });
    return (
      <StoryProviders>
        <PipelinesCard />
      </StoryProviders>
    );
  },
};

export const ReadOnly: Story = {
  render: () => {
    installFetchStub({
      "GET /me": () => jsonResponse(me(["rep"])),
      "GET /pipelines": () => jsonResponse(pipelines),
    });
    return (
      <StoryProviders>
        <PipelinesCard />
      </StoryProviders>
    );
  },
};
