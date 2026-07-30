// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import type { components } from "../api/schema";
import { ConnectionsCard } from "./connections";
import { installFetchStub, jsonResponse, StoryProviders } from "./story-utils";

// The connections card at the four widths it actually has to work at: a full
// neighbourhood, a capped one, a withheld group, and an account with nothing
// attached to it yet.
const meta: Meta = {
  title: "Screens/Company connections",
  parameters: { layout: "padded" },
};
export default meta;

type Story = StoryObj;
type Graph = components["schemas"]["OrganizationGraph"];
type GraphNode = Graph["nodes"][number];

const ROOT = "o-1";

// node is one neighbour: never the centre, so `root` is stated once here
// rather than on every fixture row.
function node(fields: Omit<GraphNode, "root">): GraphNode {
  return { ...fields, root: false };
}

const populated: Graph = {
  as_of: "2026-07-13T09:00:00Z",
  root_id: ROOT,
  nodes: [
    {
      id: ROOT,
      kind: "organization",
      label: "Brandt Automotive GmbH",
      root: true,
    },
    node({
      id: "p-1",
      kind: "person",
      label: "Dana Weber",
      detail: "CTO",
      strength: 74,
      strength_bucket: "strong",
      intro_path: true,
    }),
    node({
      id: "p-2",
      kind: "person",
      label: "Milan Prohaska",
      detail: "Head of Fleet",
      strength: 41,
      strength_bucket: "warm",
    }),
    node({
      id: "d-1",
      kind: "deal",
      label: "Fleet retrofit 2026",
      detail: "Proposal",
    }),
    node({
      id: "p-3",
      kind: "person",
      label: "Ines Kruse",
      detail: "Procurement",
    }),
    node({ id: "o-2", kind: "organization", label: "Brandt Holding" }),
    node({ id: "o-3", kind: "organization", label: "Nordwerk Systems" }),
  ],
  edges: [
    { from: ROOT, to: "p-1", kind: "employment", role: "cto" },
    { from: ROOT, to: "p-2", kind: "employment", role: "head_of_fleet" },
    { from: ROOT, to: "d-1", kind: "has_deal" },
    { from: "d-1", to: "p-1", kind: "deal_stakeholder", role: "champion" },
    {
      from: "d-1",
      to: "p-3",
      kind: "deal_stakeholder",
      role: "economic_buyer",
    },
    { from: "o-2", to: ROOT, kind: "parent_of" },
    { from: "o-3", to: ROOT, kind: "referred_by" },
  ],
  dropped_count: 0,
  groups_omitted: [],
  intro_path: { signal_id: "s-1", contact_id: "p-1" },
};

// The same account read by someone whose role cannot see people: the card must
// read as "hidden from you", never as a company nobody works at.
const withheld: Graph = {
  ...populated,
  nodes: populated.nodes.filter((node) => node.kind !== "person"),
  edges: populated.edges.filter(
    (edge) => edge.kind !== "employment" && edge.kind !== "deal_stakeholder",
  ),
  groups_omitted: ["contacts", "intro_path"],
  intro_path: undefined,
};

const capped: Graph = { ...populated, dropped_count: 6 };

const empty: Graph = {
  as_of: "2026-07-13T09:00:00Z",
  root_id: ROOT,
  nodes: [
    {
      id: ROOT,
      kind: "organization",
      label: "Brandt Automotive GmbH",
      root: true,
    },
  ],
  edges: [],
  dropped_count: 0,
  groups_omitted: [],
};

// The card renders an EntityRef per node, so the record reads every node names
// have to answer too — otherwise every row would sit on its id fallback.
const NAMES: Record<string, unknown> = {
  "GET /people/p-1": { id: "p-1", full_name: "Dana Weber" },
  "GET /people/p-2": { id: "p-2", full_name: "Milan Prohaska" },
  "GET /people/p-3": { id: "p-3", full_name: "Ines Kruse" },
  "GET /deals/d-1": { id: "d-1", name: "Fleet retrofit 2026" },
  "GET /organizations/o-2": { id: "o-2", display_name: "Brandt Holding" },
  "GET /organizations/o-3": { id: "o-3", display_name: "Nordwerk Systems" },
};

function Card({ graph }: Readonly<{ graph: Graph }>) {
  installFetchStub({
    [`GET /organizations/${ROOT}/graph`]: () => jsonResponse(graph),
    ...Object.fromEntries(
      Object.entries(NAMES).map(([route, body]) => [
        route,
        () => jsonResponse(body),
      ]),
    ),
  });
  return (
    <StoryProviders>
      <div style={{ display: "grid", gap: "var(--space-3)", maxWidth: 380 }}>
        <ConnectionsCard orgId={ROOT} />
      </div>
    </StoryProviders>
  );
}

export const Populated: Story = { render: () => <Card graph={populated} /> };

export const GroupWithheld: Story = { render: () => <Card graph={withheld} /> };

export const Capped: Story = { render: () => <Card graph={capped} /> };

export const NothingYet: Story = { render: () => <Card graph={empty} /> };
