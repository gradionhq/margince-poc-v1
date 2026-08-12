// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import type { components } from "../api/schema";
import { CompanyRail } from "./companyrail";
import { installFetchStub, jsonResponse, StoryProviders } from "./story-utils";

// The record's left rail (mockup State A): one panel, a details grid over
// collapsible sections. Every seeded demo account grants full RBAC and
// omits nothing, so the withheld story below is the only place a reader
// ever sees this rail's own honesty rule — a section the caller's role
// cannot read reads "Hidden — your role cannot read this", never a silent
// empty state.

const meta: Meta = {
  title: "Screens/Company rail",
  parameters: { layout: "padded" },
};
export default meta;

type Story = StoryObj;
type View = components["schemas"]["Organization360"];

const page = { has_more: false, next_cursor: null };

const org = {
  id: "o-1",
  workspace_id: "w-1",
  display_name: "Brandt Automotive GmbH",
  legal_name: "Brandt Automotive GmbH",
  lifecycle: "customer",
  owner_id: "u-1",
  industry: "Automotive",
  size_band: "51-200",
  linkedin_url: "https://linkedin.com/company/brandt",
  address: { city: "Munich", country: "DE" },
  domains: [{ domain: "brandt.example", is_primary: true, source: "manual" }],
  captured_by: "human:u1",
  source: "manual",
  version: 1,
  created_at: "2026-06-01T08:00:00Z",
  updated_at: "2026-06-01T08:00:00Z",
};

const populated = {
  as_of: "2026-06-01T09:00:00Z",
  organization: org,
  sections_omitted: [],
  people: {
    data: [
      {
        person_id: "p-1",
        full_name: "Dana Buyer",
        title: "Head of Fleet",
        deal_roles: [],
        consent: { marketing_email: "granted" },
        strength: {
          score: 71,
          bucket: "strong",
          factors: {
            recency: 0.9,
            frequency: 0.6,
            reciprocity: 0.8,
            direction: 0.8,
          },
        },
      },
    ],
    page,
  },
  health: {
    relationship: { rating: "good", reason: "Replying steadily." },
    commercial: {
      rating: "strong",
      reason: "Two open deals, neither stalled.",
    },
    payment: { rating: "good", reason: "Nothing overdue." },
  },
  tags: [{ id: "t-1", workspace_id: "w-1", name: "Key account" }],
  list_memberships: [
    {
      id: "l-1",
      workspace_id: "w-1",
      name: "Q3 renewals",
      entity_type: "organization",
      list_type: "static",
    },
  ],
} as unknown as View;

// Health and People withheld, exactly the shape a role scoped away from
// them reads on any real workspace — no seeded demo account can reach this,
// so this story is the only place it renders.
const withheld = {
  ...populated,
  health: undefined,
  people: undefined,
  sections_omitted: ["health", "people"],
} as unknown as View;

function Rail({ view }: Readonly<{ view: View }>) {
  installFetchStub({
    "GET /organizations/o-1/finance-summary": () =>
      jsonResponse({ organization_id: "o-1", state: "no_connection" }),
    "GET /users": () =>
      jsonResponse({
        data: [{ id: "u-1", display_name: "Mira Voss" }],
        page,
      }),
    "GET /signals": () => jsonResponse({ data: [], page }),
  });
  return (
    <StoryProviders>
      <div style={{ maxWidth: 340 }}>
        <CompanyRail orgId="o-1" view={view} withPeople composerOpen={false} />
      </div>
    </StoryProviders>
  );
}

export const Populated: Story = { render: () => <Rail view={populated} /> };

export const SectionWithheld: Story = {
  render: () => <Rail view={withheld} />,
};
