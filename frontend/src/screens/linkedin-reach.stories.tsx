// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { meFixture } from "../app/mefixture";
import { LinkedInReachCard } from "./linkedin-reach";
import { installFetchStub, jsonResponse, StoryProviders } from "./story-utils";

// LinkedInReachCard stories for the fe-uat render gate. The three cases that
// read differently are the three the card exists to keep apart: accounts the
// network reaches, a fresh workspace where nothing resolved yet (which still
// has to report the unresolved count), and a read that failed — which is NOT
// an empty network.

function reachStory(body: unknown, status = 200) {
  return () => {
    installFetchStub({
      "GET /me": () => jsonResponse(meFixture()),
      "GET /me/linkedin-reach": () => jsonResponse(body, status),
    });
    return (
      <StoryProviders>
        <LinkedInReachCard />
      </StoryProviders>
    );
  };
}

const meta: Meta<typeof LinkedInReachCard> = {
  title: "Settings/You/Connections/LinkedIn reach",
  component: LinkedInReachCard,
};
export default meta;
type Story = StoryObj<typeof LinkedInReachCard>;

export const Reaches: Story = {
  render: reachStory({
    accounts: [
      {
        organization_id: "018f3a1b-0000-7000-8000-0000000000a1",
        display_name: "Nordwind Logistik GmbH",
        connections: 14,
        contacts_on_file: 3,
      },
      {
        organization_id: "018f3a1b-0000-7000-8000-0000000000a2",
        display_name: "Havelmann & Söhne",
        connections: 6,
        contacts_on_file: 6,
      },
    ],
    accounts_total: 9,
    unresolved_connections: 1420,
  }),
};

// Nothing resolved yet, and the unresolved count matters MOST here: five
// thousand imported connections that matched no account is not "none yet".
export const NothingResolvedYet: Story = {
  render: reachStory({
    accounts: [],
    accounts_total: 0,
    unresolved_connections: 5210,
  }),
};

export const ReadFailed: Story = {
  render: reachStory(
    { title: "Internal Server Error", detail: "the reach index is rebuilding" },
    500,
  ),
};
