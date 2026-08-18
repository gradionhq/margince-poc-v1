// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { userEvent, within } from "storybook/test";
import { StatStrip } from "../design-system/statstrip";
import { ForecastTile, ReportsScreen } from "./reports";
import {
  installFetchStub,
  jsonResponse,
  meRoute,
  type RouteMap,
  StoryProviders,
} from "./story-utils";

// The Reports screen for the fe-uat render gate. All three report segments draw
// into ONE surface — a titled card whose trailing action row carries "Explain
// this number" — so each story below is that same card holding a different
// report, which is exactly the drift these stories exist to catch: the screen
// body had no render coverage while three segments grew three different looks.
//
// Every read is stubbed off the same shapes reports.test.tsx exercises; the
// segment stories click the picker in play() because the screen owns the
// selection, and fe-uat waits for the interaction to settle before capturing.

const pipelines = {
  data: [
    {
      id: "pl",
      name: "Sales",
      is_default: true,
      position: 0,
      stages: [
        {
          id: "pl-s1",
          pipeline_id: "pl",
          name: "Qualify",
          position: 1,
          semantic: "open",
          win_probability: 20,
        },
        {
          id: "pl-s2",
          pipeline_id: "pl",
          name: "Proposal sent",
          position: 2,
          semantic: "open",
          win_probability: 60,
        },
      ],
    },
  ],
  page: { next_cursor: null, has_more: false },
};

function run(report: string, rows: Record<string, unknown>[]) {
  return jsonResponse({
    report,
    plan: {},
    columns: [],
    rows,
    derivation_url: `/v1/reports/${report}/derivation?by=stage_id&agg=sum:amount_minor:raw_minor&stage_id=pl-s1`,
  });
}

const stageRows = [
  {
    stage_id: "pl-s1",
    raw_minor: 24686,
    weighted_minor: 4938,
    deal_count: 2,
    currency: "EUR",
  },
  {
    stage_id: "pl-s2",
    raw_minor: 1850000,
    weighted_minor: 1110000,
    deal_count: 5,
    currency: "EUR",
  },
];

const forecastRows = [
  {
    forecast_category: "commit",
    raw_minor: 2800000,
    weighted_minor: 2380000,
    deal_count: 4,
    currency: "EUR",
  },
  {
    forecast_category: "best_case",
    raw_minor: 1250000,
    weighted_minor: 500000,
    deal_count: 3,
    currency: "EUR",
  },
  {
    forecast_category: "pipeline",
    raw_minor: 940000,
    weighted_minor: 188000,
    deal_count: 6,
    currency: "EUR",
  },
  {
    forecast_category: "slipped",
    raw_minor: 90000,
    weighted_minor: 45000,
    deal_count: 1,
    currency: "EUR",
  },
];

const companyRows = [
  {
    organization_id: "BÄR Pharma GmbH",
    raw_minor: 2500000,
    deal_count: 4,
    currency: "EUR",
  },
  {
    organization_id: "Brandt Systemtechnik",
    raw_minor: 870000,
    deal_count: 2,
    currency: "EUR",
  },
];

const derivation = {
  report: "deals-by-stage",
  definition: "Sum of open-deal amounts, grouped by stage, in Qualify",
  plan: {},
  columns: ["deal", "amount"],
  rows: [
    { deal: "BÄR Pharma — Packaging QA", amount: "€123.43" },
    { deal: "Brandt — Line QA Retrofit", amount: "€123.43" },
  ],
};

const routes: RouteMap = {
  "GET /me": meRoute({}),
  "GET /pipelines": () => jsonResponse(pipelines),
  "POST /reports/deals-by-stage": () => run("deals-by-stage", stageRows),
  "POST /reports/forecast": () => run("forecast", forecastRows),
  "POST /reports/open-deals-per-company": () =>
    run("open-deals-per-company", companyRows),
  "GET /reports/deals-by-stage/derivation": () => jsonResponse(derivation),
};

function screenStory() {
  installFetchStub(routes);
  return (
    <StoryProviders>
      <ReportsScreen />
    </StoryProviders>
  );
}

const clickButton =
  (name: string) =>
  async ({ canvasElement }: { canvasElement: HTMLElement }) => {
    await userEvent.click(
      await within(canvasElement).findByRole("button", { name }),
    );
  };

const meta: Meta = { title: "Records/Reports" };
export default meta;

type Story = StoryObj;

// The default segment: the stage table inside the report card, the explain verb
// in the card's own action row.
export const DealsByStage: Story = { render: screenStory };

// Five money figures read across as one comparison — the strip, under the
// callout that says how to read the second figure in each slot.
export const Forecast: Story = {
  render: screenStory,
  play: clickButton("Forecast"),
};

export const OpenDealsPerCompany: Story = {
  render: screenStory,
  play: clickButton("Open deals per company"),
};

// "Explain this number" open: the report card above, the derivation card below
// it, both the same titled-card surface.
export const Explain: Story = {
  render: screenStory,
  play: clickButton("Explain this number"),
};

// One forecast slot on its own, in the plate it actually renders in: the raw
// total is the reading, the weighted total the basis it was drawn from.
export const ForecastSlots: Story = {
  render: () => (
    <StoryProviders>
      <StatStrip>
        <ForecastTile
          label="Commit"
          amountMinor={2800000}
          weightedMinor={2380000}
          currency="EUR"
          locale="en"
        />
        <ForecastTile
          label="Best case"
          amountMinor={1250000}
          weightedMinor={500000}
          currency="EUR"
          locale="en"
        />
        <ForecastTile
          label="Omitted"
          amountMinor={0}
          currency="EUR"
          locale="en"
        />
      </StatStrip>
    </StoryProviders>
  ),
};
