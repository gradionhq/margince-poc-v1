// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { LocaleProvider } from "../i18n";
import { VoiceInsights, type VoiceInsightsData } from "./voice-insights";

// What a finished build actually says about how somebody writes. Prop-driven —
// the parsing of a raw build into this shape is `parseVoiceInsights`, and the
// card only ever renders what came out of it.
const FULL: VoiceInsightsData = {
  identity: "Plain, specific, and quick to the ask.",
  thinking: "Leads with the constraint, then the offer.",
  obsessions: ["delivery dates", "who signs"],
  moves: [
    {
      move: "Names the blocker before the ask",
      quote: "Depot's offline the 14th — can we sign before then?",
    },
    { move: "Closes on one question", quote: "Does Thursday work?" },
  ],
  avoid: ["exclamation marks", "hedging"],
  sampleDrafts: [
    {
      subject: "Retrofit timeline",
      body: "Lars — the depot window moved to the 14th. Can you confirm the quote before then?",
      score: 0.82,
    },
  ],
  nextBest: "Add three sent threads from the last quarter.",
  nextBestKey: "recent_sent",
  nextBestWords: 900,
  words: 4200,
  meanSentence: 14,
  sources: 6,
  modelName: "deepseek-chat",
};

// Every optional half absent: the earliest a profile can say anything at all,
// and the state that proves each block stands on its own.
const SPARSE: VoiceInsightsData = {
  identity: null,
  thinking: null,
  obsessions: [],
  moves: [],
  avoid: [],
  sampleDrafts: [],
  nextBest: null,
  nextBestKey: null,
  nextBestWords: null,
  words: 380,
  meanSentence: null,
  sources: 1,
  modelName: "unrecorded",
};

const meta: Meta<typeof VoiceInsights> = {
  title: "Settings/You/Writing voice/Insights",
  component: VoiceInsights,
  decorators: [
    (Story) => (
      <LocaleProvider initial="en">
        <Story />
      </LocaleProvider>
    ),
  ],
};
export default meta;
type Story = StoryObj<typeof VoiceInsights>;

export const Rich: Story = {
  render: () => <VoiceInsights data={FULL} profileVersion={4} />,
};

export const JustEnough: Story = {
  render: () => <VoiceInsights data={SPARSE} profileVersion={1} />,
};
