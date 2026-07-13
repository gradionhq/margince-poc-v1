// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { LocaleProvider } from "../i18n";
import { ForecastTile } from "./reports";

// ForecastTile is prop-driven (Card + typography, no fetch) — the reports
// screen maps forecast-category rows onto it. Shown here for the commit and
// best-case categories.
const meta: Meta = {
  title: "Screens/Reports",
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
      amountMinor={500000}
      currency="EUR"
      locale="en"
    />
  ),
};

export const BestCase: Story = {
  render: () => (
    <ForecastTile
      label="Best case"
      amountMinor={1250000}
      currency="EUR"
      locale="en"
    />
  ),
};
