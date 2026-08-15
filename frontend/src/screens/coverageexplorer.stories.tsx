// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { userEvent, within } from "storybook/test";
import type { components } from "../api/schema";
import { CoverageExplorer } from "./coverageexplorer";
import {
  installFetchStub,
  jsonResponse,
  meRoute,
  StoryProviders,
} from "./story-utils";

type Contact = components["schemas"]["Organization360Contact"];

// Who on our side knows whom on theirs. The explorer is a dialog behind a link,
// so a `play` opens it — a closed one screenshots the trigger and nothing else.
const CONTACTS = [
  {
    person_id: "p-1",
    full_name: "Anna Weber",
    title: "Head of Fleet",
    strength: { bucket: "strong", score: 82 },
    deal_roles: [],
    consent: {},
  },
  {
    person_id: "p-2",
    full_name: "Otto Fischer",
    title: "Depot Manager",
    strength: { bucket: "weak", score: 21 },
    deal_roles: [],
    consent: {},
  },
] as unknown as Contact[];

const GRAPH = {
  as_of: "2026-08-13T09:00:00Z",
  root_id: "o-brandt",
  dropped_count: 0,
  groups_omitted: [],
  nodes: [
    {
      id: "o-brandt",
      kind: "organization",
      label: "Brandt Automotive",
      root: true,
    },
    { id: "u-1", kind: "user", label: "Lars Brandt", root: false },
    { id: "u-2", kind: "user", label: "Dana Kessler", root: false },
    { id: "p-1", kind: "person", label: "Anna Weber", root: false },
    { id: "p-2", kind: "person", label: "Otto Fischer", root: false },
  ],
  edges: [
    {
      from: "u-1",
      to: "p-1",
      kind: "in_contact_with",
      strength_bucket: "strong",
    },
    {
      from: "u-2",
      to: "p-2",
      kind: "in_contact_with",
      strength_bucket: "weak",
    },
  ],
};

function story(graph: Record<string, unknown>, contacts: Contact[]) {
  return () => {
    installFetchStub({
      "GET /me": meRoute({ organization: ["read"], person: ["read"] }),
      "GET /organizations/o-brandt/graph": () => jsonResponse(graph),
    });
    return (
      <StoryProviders>
        <CoverageExplorer orgId="o-brandt" contacts={contacts} />
      </StoryProviders>
    );
  };
}

const meta: Meta<typeof CoverageExplorer> = {
  title: "Records/Company/Coverage explorer",
  component: CoverageExplorer,
};
export default meta;
type Story = StoryObj<typeof CoverageExplorer>;

export const Closed: Story = { render: story(GRAPH, CONTACTS) };

// The grid a reader came for: one column per colleague, one row per contact, and
// the strength where they meet.
export const Opened: Story = {
  render: story(GRAPH, CONTACTS),
  play: async ({ canvasElement }) => {
    await userEvent.setup().click(within(canvasElement).getByRole("button"));
  },
};

// Truncated or partly withheld: the answer below it is a subset, and both the
// found-somebody and the found-nobody reading have to say so.
export const IncompleteGraph: Story = {
  render: story({ ...GRAPH, dropped_count: 12 }, CONTACTS),
  play: async ({ canvasElement }) => {
    await userEvent.setup().click(within(canvasElement).getByRole("button"));
  },
};
