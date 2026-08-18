// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { LocaleProvider } from "../i18n";
import { ForecastTile } from "./reports";

// ForecastTile is prop-driven (Card + typography, no fetch) — the reports
// screen maps forecast-category rows onto it. The report groups by currency, so
// a tile takes a LIST of readings: the four states below are the ones the
// screen can actually hand it.
const meta: Meta = {
  title: "Records/Reports",
  parameters: { layout: "padded" },
  decorators: [
    (Story) => (
      <LocaleProvider initial="en">
        <Story />
      </LocaleProvider>
    ),
  ],
};
export default meta;

type Story = StoryObj;

export const Commit: Story = {
  render: () => (
    <ForecastTile
      label="Commit"
      amounts={[{ currency: "EUR", rawMinor: 500000, weightedMinor: 425000 }]}
      locale="en"
    />
  ),
};

// Two currencies in one category: two totals, never a third that adds them.
export const TwoCurrencies: Story = {
  render: () => (
    <ForecastTile
      label="Best case"
      amounts={[
        { currency: "EUR", rawMinor: 1250000, weightedMinor: 500000 },
        { currency: "VND", rawMinor: 4500000000, weightedMinor: 1800000000 },
      ]}
      locale="en"
    />
  ),
};

// A category the report returned no row for. Not a zero — nothing was measured
// in any currency for it to be zero of.
export const NoDeals: Story = {
  render: () => <ForecastTile label="Omitted" amounts={[]} locale="en" />,
};

// Deals nobody has priced: the report groups them under a null currency, and a
// reading with no currency cannot be rendered as money at all.
export const Unpriced: Story = {
  render: () => (
    <ForecastTile
      label="Pipeline"
      amounts={[{ currency: null, rawMinor: null, weightedMinor: null }]}
      locale="en"
    />
  ),
};
