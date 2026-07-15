// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { LocaleProvider } from "../i18n";
import { DealScreen, FxLine } from "./deals";
import {
  emptyPage,
  installFetchStub,
  jsonResponse,
  StoryProviders,
} from "./story-utils";

// FxLine is prop-driven (no fetch/react-query inside) — the deal 360 supplies
// the amount/currency/rate. Rendered here in its converted state and, since the
// deal screen only mounts it when a rate exists, a low/zero-rate variant. The
// DealScreen stories exercise the offers panel over the shared fetch stub.
const meta: Meta = {
  title: "Screens/Deals",
  parameters: { layout: "padded" },
};
export default meta;

type Story = StoryObj;

export const FxConverted: Story = {
  render: () => (
    <LocaleProvider initial="en">
      <FxLine
        amountMinor={100000}
        fxRateToBase="0.92"
        fxRateDate="2026-07-01"
        locale="en"
      />
    </LocaleProvider>
  ),
};

export const FxNoDate: Story = {
  render: () => (
    <LocaleProvider initial="en">
      <FxLine
        amountMinor={250000}
        fxRateToBase="1.17"
        fxRateDate={null}
        locale="en"
      />
    </LocaleProvider>
  ),
};

const deal = {
  id: "d1",
  workspace_id: "w",
  name: "Fleet retrofit",
  amount_minor: 4_800_000,
  currency: "EUR",
  pipeline_id: "pl",
  stage_id: "s1",
  status: "open",
  source: "manual",
  captured_by: "human:u1",
  created_at: "2026-06-01T00:00:00Z",
  updated_at: "2026-06-01T00:00:00Z",
};

const offer = {
  id: "o1",
  workspace_id: "w",
  deal_id: "d1",
  offer_number: "OFF-0001",
  revision: 1,
  status: "draft",
  currency: "EUR",
  net_minor: 100_000,
  tax_minor: 19_000,
  gross_minor: 119_000,
  ai_generated: false,
  line_items: [],
  source: "manual",
  captured_by: "human:u1",
  created_at: "2026-06-01T00:00:00Z",
  updated_at: "2026-06-01T00:00:00Z",
};

function installDealStub(offers: unknown[]) {
  installFetchStub({
    "GET /deals/d1": () => jsonResponse(deal),
    "GET /deals/d1/offers": () =>
      jsonResponse({
        data: offers,
        page: { next_cursor: null, has_more: false },
      }),
    "GET /deals/d1/stakeholders": () => jsonResponse(emptyPage),
    "GET /pipelines": () => jsonResponse(emptyPage),
    "GET /approvals": () => jsonResponse(emptyPage),
    "GET /activities": () => jsonResponse(emptyPage),
    "GET /records/deal/d1/context": () =>
      jsonResponse({ anchor: { type: "deal", id: "d1" }, sections: [] }),
    "GET /me": () =>
      jsonResponse({
        user: { id: "u-9", display_name: "Me" },
        roles: ["rep"],
        teams: [],
      }),
  });
}

export const WithOffers: Story = {
  render: () => {
    installDealStub([offer]);
    return (
      <StoryProviders>
        <DealScreen id="d1" />
      </StoryProviders>
    );
  },
};

export const NoOffers: Story = {
  render: () => {
    installDealStub([]);
    return (
      <StoryProviders>
        <DealScreen id="d1" />
      </StoryProviders>
    );
  },
};
