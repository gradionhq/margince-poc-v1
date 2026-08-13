// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { useState } from "react";
import { LocaleProvider } from "../i18n";
import { Switch } from "./switch";

const meta: Meta<typeof Switch> = {
  title: "Design System/Switch",
  component: Switch,
  parameters: { layout: "padded" },
  decorators: [
    (Story) => (
      <LocaleProvider initial="en">
        <Story />
      </LocaleProvider>
    ),
  ],
};
export default meta;
type Story = StoryObj<typeof Switch>;

/** Live, so the knob's travel and the focus ring can be judged. */
function Live(props: Readonly<{ initial: boolean; disabled?: boolean }>) {
  const [on, setOn] = useState(props.initial);
  return (
    <Switch
      label="Auto-enrich captured companies"
      hint="Looks up a company the first time it is captured."
      checked={on}
      disabled={props.disabled}
      onChange={setOn}
    />
  );
}

export const Off: Story = { render: () => <Live initial={false} /> };
export const On: Story = { render: () => <Live initial /> };

/**
 * Unavailable with a reason. The words are what a reader needs; the dimming
 * alone would only tell them it is broken.
 */
export const WithReason: Story = {
  render: () => (
    <Switch
      label="Auto-enrich captured companies"
      hint="Looks up a company the first time it is captured."
      reason="Only an admin or ops can change this."
      checked
      disabled
      onChange={() => undefined}
    />
  ),
};

/**
 * Unavailable because a write is in flight — no reason, because the wait
 * explains itself by ending.
 */
export const Pending: Story = { render: () => <Live initial disabled /> };

/**
 * The label reaches assistive tech and nothing else, for a row that draws its
 * own heading with badges and prose the switch must not duplicate.
 */
export const LabelHidden: Story = {
  render: () => (
    <div
      style={{ display: "flex", alignItems: "center", gap: "var(--space-3)" }}
    >
      <span>Marketing email</span>
      <Switch
        label="Marketing email"
        labelHidden
        checked
        onChange={() => undefined}
      />
    </div>
  ),
};
