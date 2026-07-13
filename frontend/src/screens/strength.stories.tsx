// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { installFetchStub, jsonResponse, StoryProviders } from "./story-utils";
import { StrengthCard } from "./strength";

// StrengthCard fetches its own data (GET /people/{id}/strength or
// /organizations/{id}/strength) — the shared fetch stub (story-utils.tsx)
// mirrors the strength fixtures already exercised in people.test.tsx.
const meta: Meta = {
  title: "Screens/Strength",
  parameters: { layout: "padded" },
};
export default meta;

type Story = StoryObj;

const strongStrength = {
  score: 72,
  bucket: "strong",
  factors: { recency: 0.9, frequency: 0.6, reciprocity: 0.5, direction: 0.8 },
  last_interaction: "2026-07-01T09:00:00Z",
  inbound_90d: 5,
  outbound_90d: 7,
  contributing_activity_ids: ["a-1", "a-2", "a-3"],
};

const dormantStrength = {
  score: 0,
  bucket: "dormant",
  factors: { recency: 0, frequency: 0, reciprocity: 0, direction: 0 },
  last_interaction: null,
};

export const Strong: Story = {
  render: () => {
    installFetchStub({
      "GET /people/p-1/strength": () => jsonResponse(strongStrength),
    });
    return (
      <StoryProviders>
        <StrengthCard kind="person" id="p-1" />
      </StoryProviders>
    );
  },
};

// A record with no qualifying interactions: bucket:dormant, score:0,
// rendered plainly (0% bars, an honest "no interactions yet" caption) —
// never hidden or dressed up as an error.
export const Dormant: Story = {
  render: () => {
    installFetchStub({
      "GET /people/p-1/strength": () => jsonResponse(dormantStrength),
    });
    return (
      <StoryProviders>
        <StrengthCard kind="person" id="p-1" />
      </StoryProviders>
    );
  },
};
