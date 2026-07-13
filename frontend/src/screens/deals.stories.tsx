// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { LocaleProvider } from "../i18n";
import { FxLine } from "./deals";

// FxLine is prop-driven (no fetch/react-query inside) — the deal 360 supplies
// the amount/currency/rate. Rendered here in its converted state and, since the
// deal screen only mounts it when a rate exists, a low/zero-rate variant.
const meta: Meta = {
  title: "Screens/Deals",
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

export const FxConverted: Story = {
  render: () => (
    <FxLine
      amountMinor={100000}
      currency="USD"
      fxRateToBase="0.92"
      fxRateDate="2026-07-01"
      locale="en"
    />
  ),
};

export const FxNoDate: Story = {
  render: () => (
    <FxLine
      amountMinor={250000}
      currency="GBP"
      fxRateToBase="1.17"
      fxRateDate={null}
      locale="en"
    />
  ),
};
