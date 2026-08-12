// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import type { components } from "../api/schema";
import {
  CompanyActionBadges,
  CompanyDescription,
  CompanyIdentityLine,
  CompanyLifecycleControl,
  CompanyPrimaryActions,
} from "./companyheader";
import { installFetchStub, jsonResponse, StoryProviders } from "./story-utils";

// The account header's own pieces (RecordView's nameBadge/subtitle/pulse/
// actions slots in organizations.tsx), mounted together rather than through
// the whole record page: the header does not own a screen of its own, so
// reaching for it through CompanyScreen would drag in every other tab's reads.

const meta: Meta = {
  title: "Screens/Company header",
  parameters: { layout: "padded" },
};
export default meta;

type Story = StoryObj;
type Organization = components["schemas"]["Organization"];
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
  description: "Retrofits commercial fleets for zero-emission depots.",
  domains: [{ domain: "brandt.example", is_primary: true, source: "manual" }],
  captured_by: "human:u1",
  source: "manual",
  version: 1,
  // formatDateAbbrev(org.created_at) throws RangeError on anything that isn't
  // a real ISO string — an org fixture missing this renders the whole header
  // as nothing rather than a legible date.
  created_at: "2026-06-01T08:00:00Z",
  updated_at: "2026-06-01T08:00:00Z",
} as unknown as Organization;

// The "way in" — the contact the relationship actually runs through — plus a
// last exchange date. Both are withheld together whenever the 360 is still
// loading, so this is the state a reader sees once it lands.
const withWayIn = {
  as_of: "2026-06-01T09:00:00Z",
  organization: org,
  sections_omitted: [],
  strength: {
    score: 71,
    bucket: "strong",
    contact_count: 2,
    contributor_person_id: "p-1",
    factors: { recency: 0.9, frequency: 0.6, reciprocity: 0.8, direction: 0.8 },
  },
  last_inbound_at: "2026-05-28T10:00:00Z",
  last_outbound_at: "2026-05-30T14:00:00Z",
} as unknown as View;

// No contact has yet earned the "way in" — an account with an owner and a
// touch history but nobody who carries the relationship. `strength` is
// present (the 360 always returns it) but empty of a contributor.
const noWayIn = {
  ...withWayIn,
  strength: {
    score: 0,
    bucket: "dormant",
    contact_count: 0,
    contributor_person_id: null,
    factors: { recency: 0, frequency: 0, reciprocity: 0, direction: 0 },
  },
} as unknown as View;

function Header({
  view,
  loading,
}: Readonly<{ view?: View; loading?: boolean }>) {
  installFetchStub({
    "GET /users": () =>
      jsonResponse({ data: [{ id: "u-1", display_name: "Mira Voss" }], page }),
    "GET /people/p-1": () =>
      jsonResponse({ id: "p-1", full_name: "Dana Buyer" }),
  });
  return (
    <StoryProviders>
      <div style={{ maxWidth: 640 }}>
        <CompanyLifecycleControl org={org} />
        <CompanyDescription org={org} />
        <CompanyIdentityLine org={org} view={view} loading={loading} />
        <div
          style={{
            marginTop: "var(--space-2)",
            display: "flex",
            gap: "var(--space-2)",
          }}
        >
          <CompanyPrimaryActions
            org={org}
            composerOpen={false}
            onComposerOpen={() => {}}
          />
          <CompanyActionBadges
            org={org}
            view={view}
            onOpenHistory={() => {}}
            onSetUpPartner={() => {}}
          />
        </div>
      </div>
    </StoryProviders>
  );
}

export const WithWayIn: Story = { render: () => <Header view={withWayIn} /> };

export const NoWayIn: Story = { render: () => <Header view={noWayIn} /> };

// Still fetching the composite read: the way-in and the last-exchange dates
// withhold together rather than reading "never contacted" off no answer yet.
export const Loading: Story = {
  render: () => <Header view={withWayIn} loading />,
};
