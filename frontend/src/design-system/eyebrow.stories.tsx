// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { LocaleProvider } from "../i18n";
import { Eyebrow } from "./eyebrow";

// The micro-label above the thing it names, in the one spelling: 11px,
// semibold, uppercase, tracked open, meta colour.
const meta: Meta<typeof Eyebrow> = {
  title: "Design System/Eyebrow",
  component: Eyebrow,
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

type Story = StoryObj<typeof Eyebrow>;

export const AsLabel: Story = {
  args: { children: "Last contacted" },
};

// Over a block of content it is a real heading, and the level says where in
// the outline the block sits. Nothing about the type says which of the two
// jobs it is doing, which is why the element is the caller's call.
function HeadingDemo() {
  return (
    <section>
      <Eyebrow as="h3">Our side</Eyebrow>
      <p className="t-body">Two people at Margince know someone here.</p>
      <Eyebrow as="h3">Their side</Eyebrow>
      <p className="t-body">Four people at Brandt know someone here.</p>
    </section>
  );
}

export const AsHeading: Story = { render: () => <HeadingDemo /> };

// The same type beside a value, where a heading would invent structure a
// screen reader then has to read past.
function DefinitionDemo() {
  return (
    <dl style={{ display: "flex", gap: "var(--space-6)" }}>
      <div>
        <Eyebrow as="dt">Employees</Eyebrow>
        <dd className="t-body">1,400</dd>
      </div>
      <div>
        <Eyebrow as="dt">Founded</Eyebrow>
        <dd className="t-body">1998</dd>
      </div>
    </dl>
  );
}

export const InADefinitionList: Story = {
  render: () => <DefinitionDemo />,
};
