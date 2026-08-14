// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
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
  title: "Settings/Organization/AI/Usage",
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
