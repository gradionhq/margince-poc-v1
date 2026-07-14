// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { userEvent, within } from "storybook/test";
import { ShareScreen } from "./share";
import { installFetchStub, jsonResponse, StoryProviders } from "./story-utils";

// ShareScreen (AS-3/4/5) — record-level manual grants (A52/ADR-0039):
// empty roster (nothing to grant to yet), a populated who-has-access list,
// and the revoke confirm modal opened.

const meta: Meta = {
  title: "Screens/Share",
  parameters: { layout: "padded" },
};
export default meta;

type Story = StoryObj;

const usersPage = {
  data: [
    { id: "u-1", display_name: "Priya Nair", email: "priya@example.com" },
    { id: "u-2", display_name: "Mor Adler", email: "mor@example.com" },
  ],
  page: { next_cursor: null, has_more: false },
};

const teamsPage = {
  data: [{ id: "t-1", name: "Deal Desk", member_count: 4 }],
  page: { next_cursor: null, has_more: false },
};

const grant = {
  id: "g-1",
  record_type: "deal",
  record_id: "d-1",
  subject_type: "user" as const,
  subject_id: "u-2",
  access: "read" as const,
  granted_by: "u-1",
  reason: "compliance review",
  expires_at: null,
  created_at: "2026-06-22T14:08:00Z",
  version: 1,
};

export const EmptyRoster: Story = {
  render: () => {
    installFetchStub({
      "GET /users": () =>
        jsonResponse({ data: [], page: { next_cursor: null, has_more: false } }),
      "GET /teams": () =>
        jsonResponse({ data: [], page: { next_cursor: null, has_more: false } }),
      "GET /record-grants": () =>
        jsonResponse({ data: [], page: { next_cursor: null, has_more: false } }),
    });
    return (
      <StoryProviders>
        <ShareScreen recordType="deal" recordId="d-1" />
      </StoryProviders>
    );
  },
};

export const WithAccessList: Story = {
  render: () => {
    installFetchStub({
      "GET /users": () => jsonResponse(usersPage),
      "GET /teams": () => jsonResponse(teamsPage),
      "GET /record-grants": () =>
        jsonResponse({ data: [grant], page: { next_cursor: null, has_more: false } }),
    });
    return (
      <StoryProviders>
        <ShareScreen recordType="deal" recordId="d-1" />
      </StoryProviders>
    );
  },
};

export const RevokeConfirmOpen: Story = {
  render: () => {
    installFetchStub({
      "GET /users": () => jsonResponse(usersPage),
      "GET /teams": () => jsonResponse(teamsPage),
      "GET /record-grants": () =>
        jsonResponse({ data: [grant], page: { next_cursor: null, has_more: false } }),
    });
    return (
      <StoryProviders>
        <ShareScreen recordType="deal" recordId="d-1" />
      </StoryProviders>
    );
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const revokeButton = await canvas.findByTestId("revoke-grant");
    await userEvent.click(revokeButton);
  },
};
