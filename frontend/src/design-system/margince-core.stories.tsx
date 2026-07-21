// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { MarginceCoreScene } from "./margince-core";

const meta = {
  title: "Design System/Margince Core",
  component: MarginceCoreScene,
  parameters: { layout: "centered" },
} satisfies Meta<typeof MarginceCoreScene>;
export default meta;

type Story = StoryObj<typeof meta>;

export const Idle: Story = {
  args: { state: "idle" },
};

export const Working: Story = {
  args: { state: "working", progress: 0.58 },
};

export const Success: Story = {
  args: { state: "success" },
};

export const Attention: Story = {
  args: { state: "attention" },
};
