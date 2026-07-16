// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { userEvent, within } from "storybook/test";
import { ConsentSection } from "./consent";
import {
  installFetchStub,
  jsonResponse,
  type RouteMap,
  StoryProviders,
} from "./story-utils";

// The Person 360's Art. 7 proof log + DOI redeem field (G-4/G-5). Three
// purposes cover the ternary state matrix in one render: transactional
// (granted, no DOI), events (unknown, no DOI), marketing_email (unknown,
// requiring double opt-in) — the same PURPOSES/CONSENT shapes
// consent.test.tsx exercises, not invented fixtures.

const PURPOSES = {
  data: [
    {
      id: "p1",
      workspace_id: "w",
      key: "transactional",
      label: "Deal messages",
      requires_double_opt_in: false,
      created_at: "2026-01-01T00:00:00Z",
    },
    {
      id: "p2",
      workspace_id: "w",
      key: "events",
      label: "Events",
      requires_double_opt_in: false,
      created_at: "2026-01-01T00:00:00Z",
    },
    {
      id: "p3",
      workspace_id: "w",
      key: "marketing_email",
      label: "Marketing",
      requires_double_opt_in: true,
      created_at: "2026-01-01T00:00:00Z",
    },
  ],
  page: { next_cursor: null, has_more: false },
};

const CONSENT = {
  state: [
    {
      purpose_id: "p1",
      purpose_key: "transactional",
      state: "granted",
      updated_at: "2026-05-01T10:00:00Z",
    },
    { purpose_id: "p2", purpose_key: "events", state: "unknown" },
    { purpose_id: "p3", purpose_key: "marketing_email", state: "unknown" },
  ],
  events: [
    {
      id: "e1",
      purpose_id: "p1",
      new_state: "granted",
      source: "booking form",
      actor_type: "human",
      actor_id: "u1",
      occurred_at: "2026-05-01T10:00:00Z",
    },
  ],
};

function section(routes: RouteMap) {
  return () => {
    installFetchStub(routes);
    return (
      <StoryProviders>
        <ConsentSection personId="person-1" />
      </StoryProviders>
    );
  };
}

const meta: Meta<typeof ConsentSection> = {
  title: "screens/consent",
  component: ConsentSection,
};
export default meta;

type Story = StoryObj<typeof ConsentSection>;

export const Default: Story = {
  render: section({
    "GET /consent-purposes": () => jsonResponse(PURPOSES),
    "GET /people/person-1/consent": () => jsonResponse(CONSENT),
  }),
};

// G-4: the append-only proof log, toggled open on the already-granted row.
export const ProofLogOpen: Story = {
  render: section({
    "GET /consent-purposes": () => jsonResponse(PURPOSES),
    "GET /people/person-1/consent": () => jsonResponse(CONSENT),
  }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const row = (await canvas.findByText("Deal messages")).closest(
      ".consent-row",
    );
    if (!(row instanceof HTMLElement)) {
      throw new Error("consent row not found");
    }
    await userEvent.click(
      within(row).getByRole("button", { name: /proof log/i }),
    );
  },
};

// A workspace that tracks no consent purposes at all — the honest empty
// state, only trusted once the purposes fetch itself has succeeded.
export const Empty: Story = {
  render: section({
    "GET /consent-purposes": () =>
      jsonResponse({ data: [], page: { next_cursor: null, has_more: false } }),
    "GET /people/person-1/consent": () =>
      jsonResponse({ state: [], events: [] }),
  }),
};

export const LoadError: Story = {
  render: section({
    "GET /consent-purposes": () => jsonResponse(PURPOSES),
    "GET /people/person-1/consent": () =>
      jsonResponse({ title: "internal error", status: 500 }, 500),
  }),
};
