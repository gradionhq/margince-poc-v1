// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import type { components } from "../api/schema";
import { AutomationRow } from "./automations";
import { installFetchStub, jsonResponse, StoryProviders } from "./story-utils";

// AutomationRow with its two lazy panel toggles (Runs / Preview). The panels
// only fetch once opened; a benign stub answers the run list and preview POST
// so an interactive reviewer can open either without a live stack.

type Automation = components["schemas"]["Automation"];
type CatalogEntry = components["schemas"]["AutomationCatalogEntry"];

const meta: Meta = {
  title: "Screens/Automations",
  parameters: { layout: "padded" },
};
export default meta;

type Story = StoryObj;

const entry: CatalogEntry = {
  key: "stalled_deal_nudge",
  name: "Stalled-deal nudge",
  description: "Stages a follow-up when a deal stalls.",
  trigger: "deal.stalled",
  action: "send_email",
  tier: "confirmation_required",
  params_schema: {
    type: "object",
    properties: {
      due_in_days: { type: "integer", minimum: 1, maximum: 30, default: 3 },
    },
    required: ["due_in_days"],
  },
};

const automation: Automation = {
  id: "au-1",
  key: "stalled_deal_nudge",
  name: "Nudge stalled fleet deals",
  status: "enabled",
  params: { due_in_days: 3 },
  version: 3,
  created_at: "2026-07-01T08:00:00Z",
};

function stubPanels() {
  installFetchStub({
    "GET /automations/au-1/runs": () =>
      jsonResponse({ data: [], page: { next_cursor: null } }),
    "POST /automations/au-1/preview": (body) =>
      jsonResponse({
        matches_now: 8,
        would_have_fired: 21,
        window_days: (body as { window_days: number }).window_days,
      }),
  });
}

export const Configurable: Story = {
  render: () => {
    stubPanels();
    return (
      <StoryProviders>
        <ul style={{ listStyle: "none" }}>
          <AutomationRow automation={automation} entry={entry} canConfigure />
        </ul>
      </StoryProviders>
    );
  },
};

export const ReadOnly: Story = {
  render: () => {
    stubPanels();
    return (
      <StoryProviders>
        <ul style={{ listStyle: "none" }}>
          <AutomationRow
            automation={automation}
            entry={entry}
            canConfigure={false}
          />
        </ul>
      </StoryProviders>
    );
  },
};
