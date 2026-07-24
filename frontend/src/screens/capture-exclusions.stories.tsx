// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import type { components } from "../api/schema";
import { CaptureExclusionsCard } from "./capture-exclusions";
import { installFetchStub, jsonResponse, StoryProviders } from "./story-utils";

// CaptureExclusionsCard stories for the fe-uat render gate: a populated rule
// list across all three kinds, and the empty state — off the same
// GET /capture/exclusions shape the unit tests already exercise.

type CaptureExclusionRule = components["schemas"]["CaptureExclusionRule"];

const rules: CaptureExclusionRule[] = [
  {
    id: "018f3a1b-0000-7000-8000-0000000000e1",
    kind: "sender_domain",
    value: "family.example",
    created_at: "2026-07-01T00:00:00Z",
  },
  {
    id: "018f3a1b-0000-7000-8000-0000000000e2",
    kind: "recipient_domain",
    value: "vendor-portal.example",
    created_at: "2026-07-05T00:00:00Z",
  },
  {
    id: "018f3a1b-0000-7000-8000-0000000000e3",
    kind: "label",
    value: "Personal",
    created_at: "2026-07-10T00:00:00Z",
  },
];

function cardStory(data: CaptureExclusionRule[]) {
  return () => {
    installFetchStub({
      "GET /capture/exclusions": () => jsonResponse({ data }),
    });
    return (
      <StoryProviders>
        <CaptureExclusionsCard />
      </StoryProviders>
    );
  };
}

const meta: Meta<typeof CaptureExclusionsCard> = {
  title: "screens/capture-exclusions",
  component: CaptureExclusionsCard,
};
export default meta;
type Story = StoryObj<typeof CaptureExclusionsCard>;

export const Populated: Story = {
  render: cardStory(rules),
};

export const Empty: Story = {
  render: cardStory([]),
};
