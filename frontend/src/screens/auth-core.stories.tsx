// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { MarginceCore } from "./auth-core";
import "./auth.css";
import { StoryProviders } from "./story-utils";

const meta = {
  title: "Screens/Auth/Core Presence",
  component: MarginceCore,
  parameters: { layout: "fullscreen" },
  decorators: [
    (Story) => (
      <StoryProviders>
        <div className="auth-page">
          <Story />
        </div>
      </StoryProviders>
    ),
  ],
} satisfies Meta<typeof MarginceCore>;
export default meta;

type Story = StoryObj<typeof meta>;

export const Configured: Story = {
  args: {
    phase: "idle",
    profile: {
      name: "Margince",
      kind: "ai",
      state: "configured",
      inference_mode: "hybrid",
      providers: ["anthropic", "ollama"],
      configured_models: [
        { tier: "local_small", provider: "ollama", model: "gemma3" },
        { tier: "premium", provider: "anthropic", model: "claude-sonnet" },
      ],
    },
  },
};

export const Working: Story = {
  args: {
    ...Configured.args,
    phase: "signing-in",
  },
};

export const Unavailable: Story = {
  args: { phase: "unavailable" },
};
