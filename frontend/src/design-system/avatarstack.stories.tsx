// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { AvatarStack } from "./avatarstack";

// A committee of people as overlapping monograms, folding into a "+N" once
// the group runs past `max`.
const meta: Meta<typeof AvatarStack> = {
  title: "Design System/AvatarStack",
  component: AvatarStack,
  parameters: { layout: "padded" },
};
export default meta;

type Story = StoryObj<typeof AvatarStack>;

export const FewPeople: Story = {
  args: {
    people: [{ name: "Alex Rivera" }, { name: "Sam Okafor" }],
  },
};

export const OverTheMax: Story = {
  args: {
    people: [
      { name: "Alex Rivera" },
      { name: "Sam Okafor" },
      { name: "Priya Nair" },
      { name: "Jordan Blake" },
      { name: "Casey Lund" },
      { name: "Mira Vance" },
      { name: "Theo Marsh" },
    ],
    max: 5,
  },
};
