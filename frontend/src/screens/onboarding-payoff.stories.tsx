// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { type PayoffCounts, PayoffMessage } from "./onboarding-payoff";
import { StoryProviders } from "./story-utils";

// Two states worth looking at side by side: the flow run all the way through,
// and the flow where voice training was declined. The second is not a degraded
// version of the first — skipping voice is an offered choice, so that cell
// states the choice rather than reporting a zero.

const meta: Meta = {
  title: "Screens/Onboarding/Payoff",
  parameters: { layout: "centered" },
};
export default meta;

type Story = StoryObj;

const complete: PayoffCounts = {
  factsRead: 218,
  factsConfirmed: 46,
  peopleFound: 9,
  profileFields: 14,
  pagesRead: 18,
  voiceWords: 30512,
};

const noAction = () => undefined;

function frame(counts: PayoffCounts) {
  return (
    <StoryProviders>
      <div style={{ maxWidth: 620, padding: 24 }}>
        <PayoffMessage counts={counts} locale="en" onContinue={noAction} />
      </div>
    </StoryProviders>
  );
}

export const FullCounts: Story = {
  render: () => frame(complete),
};

export const VoiceSkipped: Story = {
  render: () => frame({ ...complete, voiceWords: null }),
};
