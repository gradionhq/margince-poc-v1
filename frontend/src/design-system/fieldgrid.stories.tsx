// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { LocaleProvider } from "../i18n";
import { FieldGrid, FieldRow } from "./fieldgrid";
import { InlineChoice, InlineText } from "./inlinechoice";

// FieldGrid is the grid around a value, not the value itself: a read-only row
// takes a plain node, an editable row wraps InlineText or InlineChoice.
const meta: Meta = {
  title: "Design System/FieldGrid",
  parameters: { layout: "padded" },
  decorators: [
    (Story) => (
      <LocaleProvider initial="en">
        <div style={{ maxWidth: 360 }}>
          <Story />
        </div>
      </LocaleProvider>
    ),
  ],
};
export default meta;

type Story = StoryObj;

export const ReadOnlyAndEditable: Story = {
  render: () => (
    <FieldGrid>
      <FieldRow label="Segment">Diagnostics</FieldRow>
      <FieldRow label="Region">Benelux</FieldRow>
      {/* InlineText draws no visible label of its own (its `label` prop is
          screen-reader- and aria-only), so FieldRow's label is the only one
          on screen here. */}
      <FieldRow label="Description">
        <InlineText
          label="Description"
          value="A short note on the account."
          placeholder="Add a description"
          canEdit
          onSave={async () => {}}
        />
      </FieldRow>
      {/* InlineChoice draws its OWN "label: value" inline, closed or open —
          `hideLabel` suppresses that visible half so FieldRow's own label is
          the only one on screen, and the value still sits in the grid's
          shared value column rather than escaping it. */}
      <FieldRow label="Stage">
        <InlineChoice
          label="Stage"
          hideLabel
          value="qualified"
          options={[
            { value: "qualified", label: "Qualified" },
            { value: "negotiating", label: "Negotiating" },
            { value: "closed", label: "Closed" },
          ]}
          canEdit
          render={(value) => value}
          onSave={async () => {}}
        />
      </FieldRow>
    </FieldGrid>
  ),
};

// The rail's own shape: every row a plain node, none of them InlineText or
// InlineChoice. A caller with nothing to write, only to show, is the FIRST
// production caller (companyrail.tsx's details grid), not an edge case of a
// mostly-editable one.
export const ReadOnly: Story = {
  render: () => (
    <FieldGrid>
      <FieldRow label="Legal name">Brandt Automotive GmbH</FieldRow>
      <FieldRow label="Account lifecycle">Customer</FieldRow>
      <FieldRow label="Owner">Mira Voss</FieldRow>
      <FieldRow label="Domain">brandt.example</FieldRow>
    </FieldGrid>
  ),
};

// The label column shrinks to its content, the value column takes what is
// left, so a long value wraps inside its own column rather than pushing the
// grid, and the panel around it, wider than the space it has.
export const LongValueWraps: Story = {
  render: () => (
    <FieldGrid>
      <FieldRow label="Address">
        Building 4, Industriestraße 118, 51063 Köln, Germany
      </FieldRow>
      <FieldRow label="Industry">Automotive</FieldRow>
    </FieldGrid>
  ),
};
