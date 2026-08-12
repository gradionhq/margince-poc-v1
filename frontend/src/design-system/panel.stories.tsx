// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { Badge, Button } from "./atoms";
import { Panel, PanelBody, PanelRow } from "./panel";

// The titled-card shape: a fixed-height header, an optional footer band, and
// two ways to fill the middle — padded prose in PanelBody, or full-bleed rows
// that touch the panel's own edges in PanelRow.
const meta: Meta<typeof Panel> = {
  title: "Design System/Panel",
  component: Panel,
  parameters: { layout: "padded" },
  decorators: [
    (Story) => (
      <div style={{ maxWidth: 420 }}>
        <Story />
      </div>
    ),
  ],
};
export default meta;

type Story = StoryObj<typeof Panel>;

export const WithBody: Story = {
  args: {
    title: "Overview",
    children: (
      <PanelBody>
        <p>A short paragraph of read-only prose, padded to the panel edge.</p>
      </PanelBody>
    ),
  },
};

export const WithTitleAction: Story = {
  args: {
    title: "Open items",
    titleAction: <Badge tone="accent">3</Badge>,
    children: (
      <>
        <PanelRow>First row</PanelRow>
        <PanelRow>Second row</PanelRow>
        <PanelRow>Third row</PanelRow>
      </>
    ),
  },
};

export const WithFooter: Story = {
  args: {
    title: "Recent activity",
    children: (
      <>
        <PanelRow>Alpha note logged</PanelRow>
        <PanelRow>Beta call completed</PanelRow>
      </>
    ),
    footer: (
      <>
        <span>Two of six shown</span>
        <Button small>See all</Button>
      </>
    ),
  },
};

export const Untitled: Story = {
  args: {
    children: (
      <PanelBody>
        <p>A panel with no header at all — the body alone.</p>
      </PanelBody>
    ),
  },
};
