// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { AssistantPanel } from "./assistant";
import { installFetchStub, jsonResponse, StoryProviders } from "./story-utils";

// "Ask about this account" as it stands in the company overview's column: the
// same header band as the readings above and below it, with the disclosure
// badge IN that band rather than as a line of prose inside the body.
//
// The answer arrives only from a press, so the story routes the question and
// leaves the pressing to the reviewer — a story that rendered a sentence the
// component never produced would review a fixture rather than the panel.

const meta: Meta = {
  title: "Records/Company 360/Ask about this account",
  parameters: { layout: "padded" },
};
export default meta;

type Story = StoryObj;

const answer = {
  organization_id: "o-1",
  question: "whats_open",
  generated_at: "2026-06-01T09:00:00Z",
  generated_by: "model",
  sentences: [
    {
      text: "Two open deals, worth about €57,000 together, both waiting on us.",
      evidence: [{ entity_type: "deal", entity_id: "d-1" }],
    },
  ],
};

export const Default: Story = {
  render: () => {
    installFetchStub({
      "POST /organizations/o-1/ask": () => jsonResponse(answer),
    });
    return (
      <StoryProviders>
        <div style={{ maxWidth: 640 }}>
          <AssistantPanel orgId="o-1" enabled onOpenRecord={() => {}} />
        </div>
      </StoryProviders>
    );
  },
};
