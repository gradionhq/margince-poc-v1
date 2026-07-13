// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { PartnersScreen, PartnerTab } from "./partners";
import { installFetchStub, jsonResponse, StoryProviders } from "./story-utils";

// PartnerTab treats GET /organizations/{id}/partner's 404 as "not a partner
// yet" (an honest empty state + setup form), never as an error — the
// NotYetPartner story exercises that branch directly via a 404 stub.
// PartnersScreen is the flat #/partners list read straight off GET /partners.
const meta: Meta = {
  title: "Screens/Partners",
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

export const ExistingPartner: Story = {
  render: () => {
    installFetchStub({
      "GET /organizations/o-1/partner": () => jsonResponse(partner),
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
