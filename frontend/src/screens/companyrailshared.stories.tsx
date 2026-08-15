// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { SectionSummary } from "./companyrailshared";

// SectionSummary is the collapsible-section heading every rail card but
// Health draws off: a name, plus a count that is ABSENT (not zero) while the
// section has not yet answered — the withheld/loading story below is the
// only place that distinction has anything to render.

const meta: Meta = {
  title: "Records/Company rail/Section summary",
  parameters: { layout: "padded" },
};
export default meta;

type Story = StoryObj;

export const Answered: Story = {
  render: () => <SectionSummary title="Tags" count={3} />,
};

export const Withheld: Story = {
  render: () => <SectionSummary title="Tags" />,
};
