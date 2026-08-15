// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { Badge, Button } from "./atoms";
import { Panel, PanelBody, PanelPlate, PanelRow } from "./panel";

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

// tone="accent": the ONE card on a page that asks for a move rather than
// reporting state. Put two of these on a page and you have no lead.
export const AccentTone: Story = {
  args: {
    tone: "accent",
    title: "Worth doing next",
    children: (
      <>
        <PanelRow>Write to Anna Brandt — nobody has since March.</PanelRow>
        <PanelRow>The renewal closes in eleven days and has no owner.</PanelRow>
      </>
    ),
    footer: <span>Two moves, both yours</span>,
  },
};

// actions: verbs that CHANGE the panel, in their own band under the body. The
// footer reports; this acts. A caller renders it only when the content is
// real — an "add" button under a section whose read failed offers a write
// nobody can say makes sense.
export const WithActions: Story = {
  args: {
    title: "Deals",
    children: (
      <>
        <PanelRow>Renewal — €48,000</PanelRow>
        <PanelRow>Expansion, EU — €12,500</PanelRow>
      </>
    ),
    actions: <Button small>Add a deal</Button>,
  },
};

// PanelPlate: the recessed plate that separates what IS from what to DO. The
// context sits on the plate, the moves run full-bleed on the panel's own
// ground, and a reader can tell the two apart before reading a word of either.
export const WithPlate: Story = {
  args: {
    tone: "accent",
    title: "Today",
    children: (
      <>
        <PanelPlate>
          <p>Their move — Anna replied on Tuesday and is waiting on pricing.</p>
        </PanelPlate>
        <PanelRow>Send the revised quote.</PanelRow>
        <PanelRow>Book the technical review.</PanelRow>
      </>
    ),
  },
};
