// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { IdentityRegion } from "./auth-core";
import "./auth.css";
import { StoryProviders } from "./story-utils";

/**
 * The identity region on its own (ADR-0076 Decision 2), so its three server-read
 * postures can be reviewed without a form beside them.
 *
 * The decorator gives it the surface's grid rather than the old `.auth-page`
 * centring box: the region is a grid child now, and reviewing it outside that
 * context would show a layout the product never renders.
 */
const meta = {
  title: "Screens/Auth/Identity region",
  component: IdentityRegion,
  parameters: { layout: "fullscreen" },
  decorators: [
    (Story) => (
      <StoryProviders>
        <div className="auth-surface">
          <Story />
        </div>
      </StoryProviders>
    ),
  ],
} satisfies Meta<typeof IdentityRegion>;
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
    },
  },
};

export const Working: Story = {
  args: {
    ...Configured.args,
    phase: "signing-in",
  },
};

/**
 * The AI is not configured on this installation, and the region says so rather
 * than hiding it. "The CRM still works" is the second half of the line.
 */
export const AiUnconfigured: Story = {
  args: {
    phase: "idle",
    profile: {
      name: "Margince",
      kind: "ai",
      state: "unconfigured",
      inference_mode: "none",
      providers: [],
    },
  },
};

/**
 * No profile: the probe has not answered, or it failed. The runtime line is
 * ABSENT rather than guessed — Decision 2c forbids a fact the frontend invented.
 */
export const RuntimePostureUnknown: Story = {
  args: { phase: "idle" },
};

export const Unavailable: Story = {
  args: { phase: "unavailable" },
};
