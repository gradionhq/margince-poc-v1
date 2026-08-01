// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { DealCoverageCard, PersonNetworkCard } from "./network";
import { installFetchStub, jsonResponse, StoryProviders } from "./story-utils";

// The two relationship-graph cards (ADR-0078). Both fetch their own data, so
// the shared fetch stub carries the fixtures — chosen to show the readings that
// are easy to get wrong: a never-spoken colleague, a clean deal, and a deal
// carrying two findings of different severity.
const meta: Meta = {
  title: "Screens/Network",
  parameters: { layout: "padded" },
};
export default meta;

type Story = StoryObj;

const network = {
  person_id: "p-1",
  colleagues: [
    {
      user_id: "u-1",
      display_name: "Anna Weber",
      strength: 81,
      strength_bucket: "strong",
      interactions_90d: 24,
      last_at: "2026-07-28T09:00:00Z",
    },
    {
      user_id: "u-2",
      display_name: "Jonas Bach",
      strength: 44,
      strength_bucket: "moderate",
      interactions_90d: 6,
      last_at: "2026-06-11T16:30:00Z",
    },
    // A colleague on the record with no exchange behind it: the band is `none`
    // and NO number is drawn, because never-spoken and gone-cold are different
    // facts and a zero renders them identically.
    {
      user_id: "u-3",
      display_name: "Mira Falk",
      strength: null,
      strength_bucket: "none",
      interactions_90d: 0,
      last_at: null,
    },
  ],
};

export const WhoKnowsThem: Story = {
  render: () => {
    installFetchStub({
      "GET /people/p-1/network": () => jsonResponse(network),
    });
    return (
      <StoryProviders>
        <PersonNetworkCard id="p-1" />
      </StoryProviders>
    );
  },
};

export const NobodyKnowsThem: Story = {
  render: () => {
    installFetchStub({
      "GET /people/p-1/network": () =>
        jsonResponse({ person_id: "p-1", colleagues: [] }),
    });
    return (
      <StoryProviders>
        <PersonNetworkCard id="p-1" />
      </StoryProviders>
    );
  },
};

// Two findings of different severity: somebody is gone (danger) and the deal
// has drifted (warning). Rendering both as alarms is how a card stops being
// read at all.
export const DealAtRisk: Story = {
  render: () => {
    installFetchStub({
      "GET /deals/d-1/coverage": () =>
        jsonResponse({
          deal_id: "d-1",
          stakeholders: [],
          our_side: [],
          risks: [
            {
              kind: "champion_left",
              summary:
                "the champion has left the account — the person arguing for this deal no longer works there",
              person_ids: ["p-9"],
            },
            {
              kind: "going_cold",
              summary:
                "no captured touch for 41 days — the deal is open and nobody is talking",
              days_since_touch: 41,
            },
          ],
        }),
    });
    return (
      <StoryProviders>
        <DealCoverageCard id="d-1" />
      </StoryProviders>
    );
  },
};

// Nothing flagged is a RESULT and says so. A blank card is indistinguishable
// from one that failed to load.
export const DealClear: Story = {
  render: () => {
    installFetchStub({
      "GET /deals/d-1/coverage": () =>
        jsonResponse({
          deal_id: "d-1",
          stakeholders: [],
          our_side: [],
          risks: [],
        }),
    });
    return (
      <StoryProviders>
        <DealCoverageCard id="d-1" />
      </StoryProviders>
    );
  },
};
