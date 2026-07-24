// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { HomeScreen } from "./home";
import { installFetchStub, jsonResponse, StoryProviders } from "./story-utils";

// Home / Morning Brief for the fe-uat render gate. The digest's
// connectors[] health line (Task 11) is the focus here: it renders only
// when a source is unhealthy (sharing Settings' own connectors.* vocabulary,
// Task 5) and stays silent — a permanent green row is noise — otherwise.

const digestBase = {
  date: "2026-07-16",
  generated_at: "2026-07-17T03:00:00Z",
  capture: {
    messages_synced: 42,
    activities_created: 42,
    people_created: 5,
    organizations_created: 2,
  },
  review: {
    dedupe_open: 3,
    approvals_pending: 1,
    classify: { commitments: 4, meetings: 2, noise: 30 },
  },
};

function homeStory(digest: unknown) {
  return () => {
    installFetchStub({
      "GET /brief": () => jsonResponse({ title: "Not Found" }, 404),
      "GET /digest": () => jsonResponse(digest),
    });
    return (
      <StoryProviders>
        <HomeScreen />
      </StoryProviders>
    );
  };
}

const meta: Meta<typeof HomeScreen> = {
  title: "screens/home",
  component: HomeScreen,
};
export default meta;
type Story = StoryObj<typeof HomeScreen>;

export const NeedsAttention: Story = {
  render: homeStory({
    ...digestBase,
    connectors: [
      {
        provider: "gmail",
        status: "reauth_required",
        last_sync_error_class: "auth",
      },
    ],
  }),
};

export const AllHealthy: Story = {
  render: homeStory({
    ...digestBase,
    connectors: [
      { provider: "gmail", status: "connected" },
      { provider: "gcal", status: "connected" },
    ],
  }),
};

export const NoDigestYet: Story = {
  render: () => {
    installFetchStub({
      "GET /brief": () => jsonResponse({ title: "Not Found" }, 404),
      "GET /digest": () => jsonResponse({ title: "Not Found" }, 404),
    });
    return (
      <StoryProviders>
        <HomeScreen />
      </StoryProviders>
    );
  },
};
