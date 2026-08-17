// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { userEvent, within } from "storybook/test";
import { PersonGraphPanel } from "./persongraph";
import { installFetchStub, jsonResponse, StoryProviders } from "./story-utils";

/**
 * Who can open a door to this person, and through whom.
 *
 * The panel had no story, which is how its node list shipped spelling
 * `className="btn-ghost btn-small"` — a variant with no base `btn` beside it,
 * and a size class that exists in no stylesheet in this tree (the real one is
 * `.btn-sm`). Under Tailwind's preflight that is a naked native button: no
 * padding, no boundary, no hover, no focus ring. Its own docblock says the
 * nodes "are real buttons and selecting one drives a live detail region", and
 * they did work — they simply did not look like anything.
 */
const meta: Meta<typeof PersonGraphPanel> = {
  title: "Records/Person record/Relationship graph",
  component: PersonGraphPanel,
  parameters: { layout: "padded" },
};
export default meta;
type Story = StoryObj<typeof PersonGraphPanel>;

const graph = {
  person_id: "p-1",
  nodes: [
    {
      id: "person:p-1",
      type: "contact",
      group: "anchor",
      label: "Dana Buyer",
      sublabel: "Head of Fleet",
    },
    {
      id: "user:u-1",
      type: "colleague",
      group: "direct",
      label: "Lars Brandt",
      sublabel: "six exchanges in 90 days",
    },
    {
      id: "user:u-2",
      type: "colleague",
      group: "direct",
      label: "Mara Vogel",
      sublabel: "one exchange in 90 days",
    },
    {
      id: "org:o-1",
      type: "organization",
      group: "second_degree",
      label: "Brandt Logistik",
    },
  ],
  edges: [
    {
      from: "user:u-1",
      to: "person:p-1",
      strength_bucket: "strong",
      interactions_90d: 6,
      inbound_90d: 3,
      outbound_90d: 3,
    },
    {
      from: "user:u-2",
      to: "person:p-1",
      strength_bucket: "weak",
      interactions_90d: 1,
      inbound_90d: 0,
      outbound_90d: 1,
    },
  ],
  groups_omitted: [],
  route: {
    via_user_id: "u-1",
    via_display_name: "Lars Brandt",
    why: "6 two-way exchanges in 90 days · last contact yesterday",
  },
};

function stub(body: unknown) {
  installFetchStub({ "GET /people/p-1/graph": () => jsonResponse(body) });
}

/** The answer leads; the node list is the working underneath it. */
export const Routed: Story = {
  render: () => {
    stub(graph);
    return (
      <StoryProviders>
        <PersonGraphPanel personId="p-1" />
      </StoryProviders>
    );
  },
};

/**
 * A node selected. The pressed state is what says which door the detail region
 * below is describing, and it is carried by the button's own chrome — so a node
 * that draws no chrome cannot show it at all.
 */
export const NodeSelected: Story = {
  render: () => {
    stub(graph);
    return (
      <StoryProviders>
        <PersonGraphPanel personId="p-1" />
      </StoryProviders>
    );
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(await canvas.findByRole("button", { name: /Lars/ }));
  },
};

/**
 * Nobody knows them yet. The panel says so rather than drawing an empty list,
 * because an empty list and a list that failed to render are the same shape.
 */
export const NoRoute: Story = {
  render: () => {
    stub({
      person_id: "p-1",
      nodes: [
        {
          id: "person:p-1",
          type: "contact",
          group: "anchor",
          label: "Dana Buyer",
        },
      ],
      edges: [],
      groups_omitted: [],
      route: null,
    });
    return (
      <StoryProviders>
        <PersonGraphPanel personId="p-1" />
      </StoryProviders>
    );
  },
};

/**
 * Dark, where the node buttons sit on the card's own ground and their boundary
 * is the only thing separating them from it.
 */
export const RoutedDark: Story = {
  name: "Routed — dark",
  globals: { theme: "dark" },
  render: () => {
    stub(graph);
    return (
      <StoryProviders>
        <PersonGraphPanel personId="p-1" />
      </StoryProviders>
    );
  },
};
