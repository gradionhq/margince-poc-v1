// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import type { components } from "../api/schema";
import { DetailsGrid } from "./companyraildetails";
import { installFetchStub, jsonResponse, StoryProviders } from "./story-utils";

// The rail's own Details grid: every known field draws a row whether or not
// the account carries a value (the docblock in companyraildetails.tsx states
// why), and writability gates the verbs, not the values — the archived story
// below is the only place a reader sees that second half render.

const meta: Meta = {
  title: "Screens/Company rail details",
  parameters: { layout: "padded" },
};
export default meta;

type Story = StoryObj;
type Organization = components["schemas"]["Organization"];

const page = { has_more: false, next_cursor: null };

const org: Organization = {
  id: "o-1",
  workspace_id: "w-1",
  display_name: "Brandt Automotive GmbH",
  legal_name: "Brandt Automotive GmbH",
  lifecycle: "customer",
  owner_id: "u-1",
  industry: "Automotive",
  size_band: "51-200",
  linkedin_url: "https://linkedin.com/company/brandt",
  address: {
    line1: "Werkstraße 12",
    line2: null,
    city: "Munich",
    region: "Bavaria",
    postal_code: "80331",
    country: "DE",
  },
  domains: [{ domain: "brandt.example", is_primary: true, source: "manual" }],
  description: "Fleet electrification pilot, renewing in Q3.",
  captured_by: "human:u1",
  source: "manual",
  version: 1,
  created_at: "2026-06-01T08:00:00Z",
  updated_at: "2026-06-01T08:00:00Z",
} as unknown as Organization;

function Details({ organization }: Readonly<{ organization: Organization }>) {
  installFetchStub({
    "GET /me": () =>
      jsonResponse({
        user: { id: "u-1", display_name: "Mira Voss" },
        authorization: { objects: { organization: { update: true } } },
      }),
    "GET /users": () =>
      jsonResponse({
        data: [{ id: "u-1", display_name: "Mira Voss" }],
        page,
      }),
  });
  return (
    <StoryProviders>
      <div style={{ maxWidth: 340 }}>
        <DetailsGrid organization={organization} />
      </div>
    </StoryProviders>
  );
}

export const Editable: Story = {
  render: () => <Details organization={org} />,
};

// Archived: every row still shows its value, none offers the edit affordance
// — the one state that exercises the grid's `readOnlyReason` half, which no
// amount of RBAC grant in the Editable story above can reach.
export const Archived: Story = {
  render: () => (
    <Details organization={{ ...org, archived_at: "2026-07-15T00:00:00Z" }} />
  ),
};

// The grid's own reason for existing (its docblock: "an absent field is a
// fact about the record"), and the one thing Editable above never shows: the
// account with nobody's hand in it yet. Every scalar text row falls back to
// InlineText's own `data-empty="true"` invitation button (legal name,
// industry, LinkedIn, description, every address part, the domain), and
// SizeBandRow's InlineChoice draws its own version of the same idea: no
// `data-empty` attribute of its own, just `render(value)` falling through to
// `field.unset` as the button's own label, chevron included, because the
// button is drawn whenever `canEdit` is true regardless of what `value` is.
export const EmptyFields: Story = {
  render: () => (
    <Details
      organization={{
        ...org,
        legal_name: null,
        industry: null,
        size_band: null,
        linkedin_url: null,
        description: null,
        address: {
          line1: null,
          line2: null,
          city: null,
          region: null,
          postal_code: null,
          country: null,
        },
        domains: [],
      }}
    />
  ),
};
