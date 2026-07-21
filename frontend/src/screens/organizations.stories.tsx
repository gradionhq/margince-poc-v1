// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { CompaniesScreen, CompanyScreen } from "./organizations";
import { installFetchStub, jsonResponse, StoryProviders } from "./story-utils";

// CompaniesScreen (list) and CompanyScreen (360 Overview) both read through
// the api client on mount — fixtures mirror organizations.test.tsx's `org`
// plus the dormant-strength default the Overview tab always fires.
const meta: Meta = {
  title: "Screens/Organizations",
  parameters: { layout: "padded" },
};
export default meta;

type Story = StoryObj;

const org = {
  id: "o-1",
  workspace_id: "w-1",
  display_name: "Brandt Automotive GmbH",
  industry: "Automotive",
  size_band: "201-500",
  domains: [{ domain: "brandt.example", is_primary: true }],
  captured_by: "human:u1",
  source: "manual",
  version: 1,
};

const dormantStrength = {
  score: 0,
  bucket: "dormant",
  factors: { recency: 0, frequency: 0, reciprocity: 0, direction: 0 },
  last_interaction: null,
};

// Confirmed profile fields (B5) and site-read facts (B6) — evidence-or-omit:
// each carries provenance + (optional) confidence + a grounding snippet.
const profileFields = [
  {
    field: "legal_name",
    value: "Brandt Automotive GmbH",
    source: "site_read",
    captured_by: "agent:capture",
    evidence_snippet: "Brandt Automotive GmbH, Stuttgart",
    source_url: "https://brandt.example/impressum",
    confidence: 0.95,
    updated_at: "2026-07-01T00:00:00Z",
  },
  {
    field: "value_proposition",
    value: "Fleet retrofits without downtime",
    source: "site_read",
    captured_by: "agent:capture",
    evidence_snippet: "We retrofit fleets without downtime",
    source_url: "https://brandt.example",
    confidence: 0.82,
    updated_at: "2026-07-01T00:00:00Z",
  },
];

const facts = [
  {
    category: "company",
    field: "founded_year",
    value: "1998",
    value_key: "founded_year:1998",
    source: "site_read",
    captured_by: "agent:capture",
    evidence_snippet: "Founded in 1998",
    source_url: "https://brandt.example/about",
    confidence: 0.9,
    updated_at: "2026-07-01T00:00:00Z",
  },
  {
    category: "offering",
    field: "service",
    value: "Fleet retrofits",
    value_key: "service:fleet-retrofits",
    source: "site_read",
    captured_by: "agent:capture",
    updated_at: "2026-07-01T00:00:00Z",
  },
  {
    category: "market",
    field: "served_industry",
    value: "Automotive OEMs",
    value_key: "served_industry:automotive-oems",
    source: "site_read",
    captured_by: "agent:capture",
    updated_at: "2026-07-01T00:00:00Z",
  },
];

export const CompaniesList: Story = {
  render: () => {
    installFetchStub({
      "GET /organizations": () =>
        jsonResponse({
          data: [org],
          page: { next_cursor: null, has_more: false },
        }),
    });
    return (
      <StoryProviders>
        <CompaniesScreen />
      </StoryProviders>
    );
  },
};

const overviewRoutes = {
  "GET /organizations/o-1": () => jsonResponse(org),
  "GET /organizations/o-1/strength": () => jsonResponse(dormantStrength),
  "GET /activities": () => jsonResponse({ data: [] }),
  "GET /records/organization/o-1/context": () =>
    jsonResponse({
      anchor: { type: "organization", id: "o-1" },
      sections: [],
    }),
};

// Populated 360: the firmographics/legal card and the facts card both carry
// site-read content, alongside the existing static firmographics dl.
export const CompanyOverview: Story = {
  render: () => {
    installFetchStub({
      ...overviewRoutes,
      "GET /organizations/o-1/profile-fields": () =>
        jsonResponse({ data: profileFields }),
      "GET /organizations/o-1/facts": () => jsonResponse({ data: facts }),
    });
    return (
      <StoryProviders>
        <CompanyScreen id="o-1" />
      </StoryProviders>
    );
  },
};

// Nothing read yet: the profile card states its honest empty note and the
// facts card renders nothing at all.
export const CompanyOverviewEmpty: Story = {
  render: () => {
    installFetchStub({
      ...overviewRoutes,
      "GET /organizations/o-1/profile-fields": () => jsonResponse({ data: [] }),
      "GET /organizations/o-1/facts": () => jsonResponse({ data: [] }),
    });
    return (
      <StoryProviders>
        <CompanyScreen id="o-1" />
      </StoryProviders>
    );
  },
};
