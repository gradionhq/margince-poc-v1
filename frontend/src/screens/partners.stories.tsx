// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { PartnersScreen, PartnerTab } from "./partners";
import {
  installFetchStub,
  jsonResponse,
  meRoute,
  StoryProviders,
} from "./story-utils";

// PartnerTab treats GET /organizations/{id}/partner's 404 as "not a partner
// yet" (an honest empty state + setup form), never as an error — the
// NotYetPartner story exercises that branch directly via a 404 stub.
// PartnersScreen is the flat #/partners list read straight off GET /partners.
const meta: Meta = {
  title: "Records/Partners",
  parameters: { layout: "padded" },
};
export default meta;

type Story = StoryObj;

const partner = {
  organization_id: "o-1",
  partner_role: "hosting",
  cert_status: "certified",
  margin_tier: "tier2_20",
  relationship_stage: "active",
  next_step: "Renew certification",
  next_step_due_at: "2026-08-01",
  served_segments: ["mid-market"],
  version: 3,
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-06-01T00:00:00Z",
};

export const NotYetPartner: Story = {
  render: () => {
    installFetchStub({
      "GET /me": meRoute({}),
      "GET /organizations/o-1/partner": () =>
        jsonResponse({ title: "Not found", detail: "no partner" }, 404),
    });
    return (
      <StoryProviders>
        <PartnerTab organizationId="o-1" />
      </StoryProviders>
    );
  },
};

// The commission entry this partner's tier produced. Without it the story
// documents only the empty ledger, which is the state a reader is least likely
// to be looking at the panel for.
const commission = {
  id: "c-1",
  deal_id: "d-1",
  partner_org_id: "o-1",
  status: "accrued",
  attribution_at_accrual: "sourced",
  margin_tier_at_accrual: "tier2_20",
  rate_bps: 2000,
  basis_amount_minor: 100000,
  currency: "EUR",
  amount_minor: 20000,
  captured_by: "human:x",
  version: 1,
  created_at: "2026-08-01T00:00:00Z",
  updated_at: "2026-08-01T00:00:00Z",
};

// The deal that commission was earned on, held by the CUSTOMER rather than by
// the partner — which is the fact the panel exists to show, and the one an
// empty story would document away.
const sourcedDeal = {
  id: "d-1",
  name: "Northgate rollout",
  organization_id: "cust-1",
  partner_org_id: "o-1",
  partner_attribution: "sourced",
  amount_minor: 100000,
  currency: "EUR",
  status: "won",
  pipeline_id: "p-1",
  stage_id: "s-1",
  version: 1,
  created_at: "2026-08-01T00:00:00Z",
  updated_at: "2026-08-01T00:00:00Z",
};

export const ExistingPartner: Story = {
  render: () => {
    installFetchStub({
      "GET /me": meRoute({}),
      "GET /organizations/o-1/partner": () => jsonResponse(partner),
      "GET /organizations/cust-1": () =>
        jsonResponse({ id: "cust-1", display_name: "Northgate GmbH" }),
      "GET /deals": () =>
        jsonResponse({ data: [sourcedDeal], page: { has_more: false } }),
      "GET /deals/d-1": () => jsonResponse(sourcedDeal),
      "GET /commissions": () =>
        jsonResponse({ data: [commission], page: { has_more: false } }),
    });
    return (
      <StoryProviders>
        <PartnerTab organizationId="o-1" />
      </StoryProviders>
    );
  },
};

export const PartnersList: Story = {
  render: () => {
    installFetchStub({
      "GET /me": meRoute({}),
      "GET /partners": () =>
        jsonResponse({
          data: [partner, { ...partner, organization_id: "o-2" }],
          page: { next_cursor: null, has_more: false },
        }),
    });
    return (
      <StoryProviders>
        <PartnersScreen />
      </StoryProviders>
    );
  },
};
