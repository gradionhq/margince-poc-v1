// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { SearchScreen } from "./search";
import { installFetchStub, jsonResponse, StoryProviders } from "./story-utils";

const hits = () =>
  jsonResponse({
    data: [
      {
        type: "person",
        id: "p1",
        title: "Dana Buyer",
        snippet: "…Dana at Acme…",
        score: 0.91,
        trust_tier: "authoritative",
      },
      {
        type: "organization",
        id: "o1",
        title: "Acme GmbH",
        snippet: "…Acme…",
        score: 0.82,
        trust_tier: "authoritative",
      },
      {
        type: "deal",
        id: "d1",
        title: "Acme — Platform expansion",
        snippet: "…platform…",
        score: 0.74,
        trust_tier: "authoritative",
      },
    ],
    page: { next_cursor: null, has_more: false },
  });

const meta: Meta<typeof SearchScreen> = {
  title: "screens/search",
  component: SearchScreen,
};
export default meta;
type Story = StoryObj<typeof SearchScreen>;

export const Populated: Story = {
  render: () => {
    installFetchStub({ "GET /search": hits });
    return (
      <StoryProviders>
        <SearchScreen q="acme" />
      </StoryProviders>
    );
  },
};
export const Empty: Story = {
  render: () => {
    installFetchStub({
      "GET /search": () =>
        jsonResponse({
          data: [],
          page: { next_cursor: null, has_more: false },
        }),
    });
    return (
      <StoryProviders>
        <SearchScreen q="zzz" />
      </StoryProviders>
    );
  },
};
