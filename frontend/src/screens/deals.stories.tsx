// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { LocaleProvider } from "../i18n";
import { DealScreen, DealsScreen, FxLine } from "./deals";
import {
  emptyPage,
  installFetchStub,
  jsonResponse,
  StoryProviders,
} from "./story-utils";

// FxLine is prop-driven (no fetch/react-query inside) — the deal 360 supplies
// the amount/rate and the installation's base currency. Rendered here converted
// into a euro base, converted into a base that is NOT the euro (the case a
// hard-coded currency got wrong on every non-euro installation), and with the
// base still unnamed, where a converted figure cannot be stated at all. The
// DealScreen stories exercise the offers panel over the shared fetch stub.
const meta: Meta = {
  title: "Records/Deals",
  parameters: { layout: "padded" },
};
export default meta;

type Story = StoryObj;

export const FxConverted: Story = {
  render: () => (
    <LocaleProvider initial="en">
      <FxLine
        amountMinor={100000}
        baseCurrency="EUR"
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
        baseCurrency="CHF"
        fxRateToBase="1.17"
        fxRateDate={null}
        locale="en"
      />
    </LocaleProvider>
  ),
};

export const FxBaseUnknown: Story = {
  render: () => (
    <LocaleProvider initial="en">
      <FxLine
        amountMinor={250000}
        baseCurrency={null}
        fxRateToBase="1.17"
        fxRateDate="2026-07-01"
        locale="en"
      />
    </LocaleProvider>
  ),
};

const deal = {
  id: "d1",
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

// A deal whose company, partner and amount the reader may not read. The wire
// sends each as null and names it in `masked_fields`, so every one of them has
// to read as withheld rather than as a deal nobody has linked or priced.
const withheldDeal = {
  ...deal,
  amount_minor: null,
  currency: null,
  organization_id: null,
  partner_org_id: null,
  masked_fields: ["amount_minor", "organization_id", "partner_org_id"],
};

function installDealStub(offers: unknown[], record: unknown = deal) {
  installFetchStub({
    "GET /deals/d1": () => jsonResponse(record),
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

export const WithheldReferences: Story = {
  render: () => {
    installDealStub([], withheldDeal);
    return (
      <StoryProviders>
        <DealScreen id="d1" />
      </StoryProviders>
    );
  },
};

// The pipeline board as the list surface's BODY, which is the whole point of
// this story: the saved-view rail, the count, the filter bar and the archived
// toggle stand above the board exactly as they stand above the table, because
// both views read one query. Rendered instead of the surface, the board took all
// four off screen and left the reader unable to see — or undo — what had
// narrowed the pipeline.
const boardStages = [
  {
    id: "s1",
    pipeline_id: "pl",
    name: "Qualify",
    position: 1,
    semantic: "open",
    win_probability: 20,
  },
  {
    id: "s2",
    pipeline_id: "pl",
    name: "Proposal",
    position: 2,
    semantic: "open",
    win_probability: 60,
  },
];

const boardDeals = [
  { ...deal, id: "b1", name: "Fleet retrofit", organization_id: "o1" },
  {
    ...deal,
    id: "b2",
    name: "Depot rollout",
    stage_id: "s2",
    amount_minor: 1_250_000,
    organization_id: "o1",
    stalled: true,
  },
  // The reader may not read this one's company: the wire sends no id and names
  // the field, so the card carries the mask rather than an empty slot.
  {
    ...deal,
    id: "b3",
    name: "Northgate framework",
    organization_id: null,
    masked_fields: ["organization_id"],
  },
];

function installBoardStub() {
  installFetchStub({
    "GET /pipelines": () =>
      jsonResponse({
        data: [
          {
            id: "pl",
            name: "Sales",
            is_default: true,
            position: 0,
            stages: boardStages,
          },
        ],
        page: { next_cursor: null },
      }),
    "GET /deals": () =>
      jsonResponse({
        data: boardDeals,
        page: { next_cursor: null, has_more: false },
      }),
    "POST /reports/deals-by-stage": () =>
      jsonResponse({
        report: "deals-by-stage",
        plan: {},
        columns: [],
        rows: [
          {
            stage_id: "s1",
            currency: "EUR",
            deals: 7,
            raw_minor: 700_000,
            weighted_minor: 140_000,
          },
          {
            stage_id: "s2",
            currency: "EUR",
            deals: 2,
            raw_minor: 250_000,
            weighted_minor: 150_000,
          },
        ],
      }),
    "GET /views": () =>
      jsonResponse({
        data: [
          {
            id: "v1",
            resource: "deals",
            name: "Slipping this quarter",
            query: { list: { sort: "", filters: { stalled: "true" } } },
            created_at: "2026-06-01T00:00:00Z",
            updated_at: "2026-06-01T00:00:00Z",
          },
        ],
        page: { next_cursor: null },
      }),
    "GET /organizations": () =>
      jsonResponse({
        data: [{ id: "o1", display_name: "Acme GmbH" }],
        page: { next_cursor: null },
      }),
    "GET /me": () =>
      jsonResponse({
        user: { id: "u-9", display_name: "Me" },
        roles: ["rep"],
        teams: [],
      }),
  });
}

export const BoardInListSurface: Story = {
  render: () => {
    installBoardStub();
    return (
      <StoryProviders>
        <DealsScreen />
      </StoryProviders>
    );
  },
};
