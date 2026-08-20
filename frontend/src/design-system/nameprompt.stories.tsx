// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { userEvent, within } from "storybook/test";
import { LocaleProvider } from "../i18n";
import { NamePrompt } from "./nameprompt";

// The one dialog for a write whose only input is a name. Chrome comes from
// ConfirmModal, so what these stories document is the part NamePrompt owns: the
// empty-name refusal, the in-flight reading, and the failure that leaves the box
// filled so the reader can correct it.
//
// Locale only — no query client and no fetch, because the component takes its
// pending and problem states as props rather than owning a mutation. That is
// what lets a caller's own write drive it.
const meta: Meta<typeof NamePrompt> = {
  title: "Patterns/Name prompt",
  component: NamePrompt,
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

const shared = {
  trigger: "Save as list",
  title: "Save this filter as a dynamic list",
  label: "List name",
  confirmLabel: "Create list",
  onSave: () => undefined,
};

type Story = StoryObj<typeof NamePrompt>;

export const Closed: Story = {
  // The trigger alone, which is all a reader sees until they ask.
  args: shared,
};

export const EmptyNameRefused: Story = {
  // Opened with nothing typed: Confirm is refused rather than pending, and the
  // two are drawn differently on purpose — one says "not yet", the other says
  // "already going".
  args: shared,
  play: async ({ canvasElement }) => {
    const page = within(canvasElement.ownerDocument.body);
    await userEvent.click(
      await page.findByRole("button", { name: "Save as list" }),
    );
  },
};

export const Saving: Story = {
  // A write in flight. Confirm draws busy and keeps focus; Cancel is genuinely
  // unavailable, since it is not the control that started anything.
  args: { ...shared, pending: true },
  play: async ({ canvasElement }) => {
    const page = within(canvasElement.ownerDocument.body);
    await userEvent.click(
      await page.findByRole("button", { name: "Save as list" }),
    );
    await userEvent.type(await page.findByLabelText("List name"), "Anns");
  },
};

export const Refused: Story = {
  // The server's reason, with the name still in the box. A dialog that closed on
  // failure would lose what was typed and leave the reader unsure whether the
  // write landed.
  args: { ...shared, problem: "A list called Anns already exists." },
  play: async ({ canvasElement }) => {
    const page = within(canvasElement.ownerDocument.body);
    await userEvent.click(
      await page.findByRole("button", { name: "Save as list" }),
    );
    await userEvent.type(await page.findByLabelText("List name"), "Anns");
  },
};
