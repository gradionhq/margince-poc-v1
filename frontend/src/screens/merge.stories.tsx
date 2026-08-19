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

const SOURCE = {
  label: "Merge into…",
  sourceId: "p-1",
  sourceName: "Anna Weber",
  searchTargets: () => Promise.resolve([{ id: "p-2", name: "Otto Fischer" }]),
  merge: (targetId: string) => Promise.resolve({ id: targetId }),
  invalidate: "people",
  recordKey: "person",
  survivorRoute: (targetId: string) => ({
    screen: "contacts" as const,
    id: targetId,
  }),
};

export const TargetPicked: Story = {
  // The merge dialog mounts record chrome that reads the session, so the probe
  // has to be routed — an unrouted one fails every grant closed and renders a
  // branch this story is not named for.
  beforeEach: () => {
    installFetchStub({ "GET /me": meRoute({ person: ["read", "update"] }) });
  },
  args: SOURCE,
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

// An archived source cannot be folded into anything — a refusal by the
// record's STATE, so the verb stays and says why (STATE-4a). The sentence
// lives on the page that owns the record, and the control points at it, which
// is the half a `title` on a disabled button cannot do.
export const RefusedByArchive: Story = {
  beforeEach: () => {
    installFetchStub({ "GET /me": meRoute({ person: ["read", "update"] }) });
  },
  args: { ...SOURCE, disabledReasonId: "merge-refusal" },
  render: (args) => (
    <>
      <MergeAction {...args} />
      <p id="merge-refusal" className="t-caption">
        This contact is archived. Restore them to change anything here.
      </p>
    </>
  ),
};
