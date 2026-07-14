// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { userEvent, within } from "storybook/test";
import { MergeAction } from "./merge";
import { StoryProviders } from "./story-utils";

// MergeAction owns its own open/search/target state — a play() interaction
// opens the dialog, types a search term (past the 250ms debounce), and
// picks the returned candidate, so the capture gate screenshots the
// "target picked, confirm line showing" state the brief asks for. The
// mutation is a react-query useMutation, so this needs the shared
// QueryClient provider even though no fetch ever actually fires here.
const meta: Meta<typeof MergeAction> = {
  title: "Screens/Merge",
  component: MergeAction,
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

type Story = StoryObj<typeof MergeAction>;

export const TargetPicked: Story = {
  args: {
    label: "Merge into…",
    sourceId: "p-1",
    sourceName: "Anna Weber",
    searchTargets: () => Promise.resolve([{ id: "p-2", name: "Otto Fischer" }]),
    merge: (targetId: string) => Promise.resolve({ id: targetId }),
    invalidate: "people",
    recordKey: "person",
    survivorRoute: (targetId: string) => ({ screen: "contacts", id: targetId }),
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(canvas.getByTestId("merge-record"));
    await userEvent.type(canvas.getByPlaceholderText("Search…"), "otto");
    // Past MergeAction's 250ms search debounce so the candidate list settles.
    await new Promise((resolve) => setTimeout(resolve, 400));
    await userEvent.click(await canvas.findByText("Otto Fischer"));
  },
};
