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
function Live(
  props: Readonly<{ initial: boolean; disabled?: boolean; pending?: boolean }>,
) {
  const [on, setOn] = useState(props.initial);
  return (
    <Switch
      label="Auto-enrich captured companies"
      hint="Looks up a company the first time it is captured."
      checked={on}
      disabled={props.disabled}
      pending={props.pending}
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
 * The same refusal, stated with `reason` and nothing else. It has to be
 * indistinguishable from `WithReason` above — dimmed track, dimmed label, the
 * sentence underneath — because the two differ only in what the caller
 * remembered to type, and a reader cannot see a prop. Judge them side by side
 * in both themes: the refused ON knob names its own ink rather than dimming
 * the accent, which over a dark ground is what keeps it from reading live.
 */
export const ReasonWithoutDisabled: Story = {
  render: () => (
    <Switch
      label="Auto-enrich captured companies"
      hint="Looks up a company the first time it is captured."
      reason="Only an admin or ops can change this."
      checked
      onChange={() => undefined}
    />
  ),
};

/**
 * The flip is being written. No reason, because the wait explains itself by
 * ending — and no dimming, because dimming is what this product uses to say
 * "not yours to change". This story used to render `disabled`, which is the
 * conflation itself: it showed a refusal and called it a wait, and there was
 * no `pending` prop for it to render instead.
 *
 * Beside `WithReason` in the sidebar on purpose. The two have to look
 * different, and one story cannot show that.
 */
export const Pending: Story = { render: () => <Live initial pending /> };

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
