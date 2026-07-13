// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { userEvent, within } from "storybook/test";
import { ArchiveAction } from "./archive";
import { StoryProviders } from "./story-utils";

// ArchiveAction owns its own confirm-modal open state (there is no
// startOpen prop, unlike CreateAction) — a play() interaction drives the
// trigger click so the capture gate screenshots the confirm dialog itself,
// not just the closed danger button. fe-uat.mjs waits 250ms after render for
// any play() interaction to settle before it screenshots. useArchiveRecord
// is a react-query mutation, so this needs the shared QueryClient provider
// (not just LocaleProvider) even though no fetch ever actually fires here.
const meta: Meta<typeof ArchiveAction> = {
  title: "Screens/Archive",
  component: ArchiveAction,
  parameters: { layout: "padded" },
  decorators: [
    (Story) => (
      <StoryProviders>
        <Story />
      </StoryProviders>
    ),
  ],
};
export default meta;

type Story = StoryObj<typeof ArchiveAction>;

export const ConfirmOpen: Story = {
  args: {
    label: "Archive",
    confirmText:
      "Are you sure? This archives the record — there is no undo control.",
    archive: () => Promise.resolve({ id: "p-1" }),
    invalidate: "people",
    recordKey: "person",
    onArchived: () => undefined,
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(canvas.getByTestId("archive-record"));
  },
};
