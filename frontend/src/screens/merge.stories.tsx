// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { screen, userEvent, within } from "storybook/test";
import { MergeAction } from "./merge";
import { installFetchStub, meRoute, StoryProviders } from "./story-utils";

// MergeAction owns its own open/search/target state — a play() interaction
// opens the dialog, types a search term (past the 250ms debounce), and
// picks the returned candidate, so the capture gate screenshots the
// "target picked, confirm line showing" state the brief asks for. The
// mutation is a react-query useMutation, so this needs the shared
// QueryClient provider even though no fetch ever actually fires here.
const meta: Meta<typeof MergeAction> = {
  title: "Patterns/Merge records",
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
  // The merge dialog mounts record chrome that reads the session, so the probe
  // has to be routed — an unrouted one fails every grant closed and renders a
  // branch this story is not named for.
  beforeEach: () => {
    installFetchStub({ "GET /me": meRoute({ person: ["read", "update"] }) });
  },
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
    // The trigger is in the canvas; everything after it is inside the merge
    // Modal, which portals to document.body — so the picker is reached through
    // `screen`, not through a canvas-scoped query that would find nothing.
    await userEvent.click(within(canvasElement).getByTestId("merge-record"));
    await userEvent.type(screen.getByPlaceholderText("Search…"), "otto");
    // Past MergeAction's 250ms search debounce so the candidate list settles.
    await new Promise((resolve) => setTimeout(resolve, 400));
    await userEvent.click(await screen.findByText("Otto Fischer"));
  },
};
