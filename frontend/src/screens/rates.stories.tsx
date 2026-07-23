// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { RatesScreen } from "./rates";
import { installFetchStub, jsonResponse, StoryProviders } from "./story-utils";

function admin() {
  return () =>
    jsonResponse({
      user: { id: "u1", email: "admin@example.test", display_name: "Admin" },
      roles: ["admin"],
    });
}

const FX = {
  data: [
    {
      from_currency: "USD",
      to_currency: "EUR",
      rate: "0.9200000000",
      effective_date: "2026-07-23",
    },
    {
      from_currency: "GBP",
      to_currency: "EUR",
      rate: "1.1700000000",
      effective_date: "2026-07-01",
    },
  ],
};

const MODELS = {
  data: [
    {
      provider: "anthropic",
      model_id: "claude-opus-4-8",
      input_per_mtok: "5",
      output_per_mtok: "25",
      cache_read_per_mtok: "0.5",
      cache_write_per_mtok: "6.25",
      effective_date: "2026-07-23",
    },
    {
      provider: "gemini",
      model_id: "gemini-3.5-flash",
      input_per_mtok: "1.5",
      output_per_mtok: "9",
      cache_read_per_mtok: "0.15",
      cache_write_per_mtok: "0",
      effective_date: "2026-07-23",
    },
  ],
};

const meta: Meta<typeof RatesScreen> = {
  title: "screens/rates",
  component: RatesScreen,
};
export default meta;
type Story = StoryObj<typeof RatesScreen>;

// An admin sees both price sheets populated, each with its "Set rate" /
// "Add model rate" affordance.
export const Populated: Story = {
  render: () => {
    installFetchStub({
      "GET /me": admin(),
      "GET /fx-rates": () => jsonResponse(FX),
      "GET /ai-model-rates": () => jsonResponse(MODELS),
    });
    return (
      <StoryProviders>
        <RatesScreen />
      </StoryProviders>
    );
  },
};

// A fresh workspace: both sheets empty, the honest empty states render.
export const Empty: Story = {
  render: () => {
    installFetchStub({
      "GET /me": admin(),
      "GET /fx-rates": () => jsonResponse({ data: [] }),
      "GET /ai-model-rates": () => jsonResponse({ data: [] }),
    });
    return (
      <StoryProviders>
        <RatesScreen />
      </StoryProviders>
    );
  },
};
