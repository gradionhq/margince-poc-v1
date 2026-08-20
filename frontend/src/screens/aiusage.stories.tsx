// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { userEvent, within } from "storybook/test";
import { type GrantSpec, meFixture } from "../app/mefixture";
import { AiUsageCard } from "./aiusage";
import { installFetchStub, jsonResponse, StoryProviders } from "./story-utils";

// The card gates itself on automation:update — the server treats the AI
// runtime's spend as operator information — so /me is not optional furniture
// here: it decides which of the card's two whole branches renders. A story
// that leaves /me to the stub's list-shaped fallback gets a body with no
// `user`, which useMe rejects as malformed, which fails every grant closed.
// The five band/state stories below were all drawing that one probe-error
// branch, under five names that each promised something else.
const OPERATOR: GrantSpec = { automation: ["read", "update"] };

function story(
  band: string,
  tasks: Record<string, unknown>[],
  allow: GrantSpec = OPERATOR,
) {
  return () => {
    installFetchStub({
      "GET /me": () => jsonResponse(meFixture({ allow })),
      "GET /ai/usage": () =>
        jsonResponse({
          days: tasks.length ? [{ date: "2026-07-20", tasks }] : [],
          budget: {
            monthly_tokens: 1000,
            spent_tokens:
              band === "queued" ? 1000 : band === "degraded" ? 850 : 200,
            band,
            currency: "EUR",
          },
        }),
    });
    return (
      <StoryProviders>
        <AiUsageCard />
      </StoryProviders>
    );
  };
}

const task = {
  task: "capture_classify",
  tier: "cheap_cloud",
  calls: 8,
  cached_hits: 2,
  tokens_in: 1200,
  tokens_out: 240,
};
const meta: Meta<typeof AiUsageCard> = {
  title: "Settings/Admin settings/AI/Usage",
  component: AiUsageCard,
};
export default meta;
type Story = StoryObj<typeof AiUsageCard>;
export const Normal: Story = { render: story("normal", [task]) };
export const EconomyMode: Story = { render: story("degraded", [task]) };
export const Queued: Story = { render: story("queued", [task]) };
export const WithCost: Story = {
  render: story("normal", [{ ...task, cost_est_minor: 124 }]),
};
export const Empty: Story = { render: story("normal", []) };

// A seat holding no automation grant. The card keeps its place and says the
// figures are withheld — an absent spend card would read as "this
// installation meters nothing", a claim about the data rather than about who
// may read it.
export const Withheld: Story = { render: story("normal", [task], {}) };

// The per-day breakdown, opened. It is the card's diagnostic half and lives in
// a disclosure standing in the row list, so this is the only story where the day
// lines are on screen — and the one that shows the opened section indented from
// its own summary rather than from the card.
export const DaysOpen: Story = {
  render: story("normal", [task]),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(await canvas.findByText("Show days"));
    await canvas.findByText(/2026-07-20/);
  },
};

// Economy mode in dark. The band is carried twice and both times by colour: the
// Badge tone and the Meter's fill at 85% of budget. Nothing else on the card
// says spend has crossed into throttling, so if either tint flattens against
// the dark panel the reader sees an ordinary month.
export const EconomyModeDark: Story = {
  globals: { theme: "dark" },
  render: story("degraded", [task]),
};

// The widest the table gets — the cost column exists only when the server
// priced the calls — at 390px. Seven columns do not fit a phone and no spend row
// is reconcilable in pieces, so DataTable's `.table-scroll` has to keep them
// inside the card; this is the story that shows whether the claim holds.
export const WithCostPhone: Story = {
  globals: { viewport: { value: "phone" } },
  tags: ["uat-phone"],
  render: story("normal", [{ ...task, cost_est_minor: 124 }]),
};
