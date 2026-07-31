// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { type PayoffCounts, PayoffMessage } from "./onboarding-payoff";
import { StoryProviders } from "./story-utils";

// Three states worth looking at side by side: the flow run all the way through,
// the flow where voice training was declined, and the flow picked back up days
// later. The second is not a degraded version of the first — skipping voice is
// an offered choice, so that cell states the choice rather than reporting a
// zero. The third changes the lead: setup is resumable, and "minutes ago" is a
// claim only a fresh run has earned.

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

// The story's own clock: the elapsed time is what decides the lead, so each
// story states the age of the setup it is showing rather than inheriting one.
const NOW_MS = Date.parse("2026-07-31T12:00:00Z");
const MINUTE_MS = 60_000;

function startedAgo(ms: number): string {
  return new Date(NOW_MS - ms).toISOString();
}

function frame(counts: PayoffCounts, startedAt: string) {
  return (
    <StoryProviders>
      {/* The width the payoff actually gets inside the conversation column,
          so the grid wraps here the way it wraps in the flow. */}
      <div style={{ maxWidth: "620px", padding: "var(--space-6)" }}>
        <PayoffMessage
          counts={counts}
          locale="en"
          startedAt={startedAt}
          nowMs={NOW_MS}
          onContinue={noAction}
        />
      </div>
    </StoryProviders>
  );
}

const justFinished = startedAgo(4 * MINUTE_MS);

export const FullCounts: Story = {
  render: () => frame(complete, justFinished),
};

export const VoiceSkipped: Story = {
  render: () => frame({ ...complete, voiceWords: null }, justFinished),
};

// Started one day, finished another — the counts are the same, the lead cannot
// be.
export const ResumedAcrossSessions: Story = {
  render: () => frame(complete, startedAgo(2 * 24 * 60 * MINUTE_MS)),
};
