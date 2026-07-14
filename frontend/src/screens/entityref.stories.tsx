// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { EntityRef } from "./entityref";
import { installFetchStub, jsonResponse, StoryProviders } from "./story-utils";

// EntityRef resolves a record id to its display name and backlinks to the
// 360. The resolved case renders a link; an id the lookup can't resolve
// (fallback route → empty page, no name) stays a plain mono id. Both states
// are captured so the gate proves the fallback never renders blank.
const meta: Meta<typeof EntityRef> = {
  title: "Screens/EntityRef",
  component: EntityRef,
  parameters: { layout: "padded" },
  decorators: [
    (Story) => {
      installFetchStub({
        "GET /organizations/o-1": () =>
          jsonResponse({ id: "o-1", display_name: "Brandt Automotive GmbH" }),
      });
      return (
        <StoryProviders>
          <Story />
        </StoryProviders>
      );
    },
  ],
};
export default meta;

type Story = StoryObj<typeof EntityRef>;

export const ResolvedBacklink: Story = {
  args: { kind: "organization", id: "o-1" },
};

export const UnresolvedFallsBackToId: Story = {
  args: { kind: "organization", id: "o-unknown" },
};
