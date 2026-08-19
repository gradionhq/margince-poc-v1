// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { useState } from "react";
import { ChoiceList } from "./choicelist";

// One question, every answer readable at rest — the control a binary decision
// needs and a dropdown cannot be. The cases on this canvas are the three a
// caller actually meets: a live either/or, a question nobody has answered yet,
// and a group a reader may look at but not change.

const meta: Meta<typeof ChoiceList> = {
  title: "Design system/ChoiceList",
  component: ChoiceList,
  parameters: { layout: "padded" },
};
export default meta;

type Story = StoryObj<typeof ChoiceList>;

const REACH = [
  {
    value: "everyone_except" as const,
    label: "Everyone I talk to — except the people I leave out",
    description:
      "Every conversation goes into the CRM until you name somebody to leave out.",
  },
  {
    value: "only_chosen" as const,
    label: "Only the people I choose",
    description: "Nothing goes in until you name somebody.",
  },
];

// Stateful, because a radio group that never moves is the one thing this
// control must not be caught doing: the story IS the interaction.
function Live({
  initial,
}: Readonly<{ initial: "" | "everyone_except" | "only_chosen" }>) {
  const [value, setValue] = useState<"" | "everyone_except" | "only_chosen">(
    initial,
  );
  return (
    <ChoiceList
      legend="Which conversations go into the CRM?"
      value={value}
      choices={REACH}
      onChange={setValue}
    />
  );
}

export const Answered: Story = {
  render: () => <Live initial="only_chosen" />,
};

// The unanswered group: no option carries the dot, so nothing claims to be the
// default. A question with a pre-selected answer is a question somebody can save
// without ever having read it.
export const Unanswered: Story = {
  render: () => <Live initial="" />,
};

// A reader who may not change it. The words stay legible — the point of the
// control is that both answers can be read, and that is as true for somebody
// who cannot pick one.
export const Disabled: Story = {
  args: {
    legend: "Which conversations go into the CRM?",
    value: "everyone_except",
    choices: REACH,
    disabled: true,
    onChange: () => {},
  },
};

// The legend off screen, for a card whose own heading already asks the question.
// It still names the group for a screen reader.
export const HiddenLegend: Story = {
  args: {
    legend: "Which conversations go into the CRM?",
    hideLegend: true,
    value: "only_chosen",
    choices: REACH,
    onChange: () => {},
  },
};

// A label with no description, which is the short form: the type carries the
// hierarchy, so an option that needs no explanation simply has none.
export const LabelsOnly: Story = {
  args: {
    legend: "Send the digest",
    value: "weekly",
    choices: [
      { value: "daily", label: "Every morning" },
      { value: "weekly", label: "Monday mornings only" },
    ],
    onChange: () => {},
  },
};
