// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { ASK_QUERY_KEY } from "../app/palette";
import { AskAiScreen } from "./ai";
import { StoryProviders } from "./story-utils";

// AskAiScreen (B-EP09.12c, 03b): the BYO-agent surface. It states the two-tier
// contract and connects to nothing — there is no chat backend behind it, and the
// screen never pretends otherwise, so both states below are the whole surface.
//
// The screen prints no title of its own: the app shell mints the one h1 for this
// route ("Ask Margince") and now carries the subtitle under it too. A story
// renders the screen without that shell, so the heading is absent here on
// purpose rather than missing.
const meta: Meta<typeof AskAiScreen> = {
  title: "Intelligence/Ask Margince",
  component: AskAiScreen,
};
export default meta;
type Story = StoryObj<typeof AskAiScreen>;

// Opened from the rail: nothing was typed on the way in, so the surface is the
// tier contract alone.
export const Cold: Story = {
  render: () => {
    sessionStorage.removeItem(ASK_QUERY_KEY);
    return (
      <StoryProviders>
        <AskAiScreen />
      </StoryProviders>
    );
  },
};

// Opened from the command palette, which hands the typed question over in
// session storage. The screen reads it ONCE and clears it, so the question shows
// on the visit it was asked on and not on the next one — a story that rendered
// twice would show it once, which is the behaviour, not a flake.
export const FromThePalette: Story = {
  render: () => {
    sessionStorage.setItem(
      ASK_QUERY_KEY,
      "which accounts went quiet since the trade fair?",
    );
    return (
      <StoryProviders>
        <AskAiScreen />
      </StoryProviders>
    );
  },
};
