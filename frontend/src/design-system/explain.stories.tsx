// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { userEvent, within } from "storybook/test";
import type { ExplainedMoney } from "../format/format";
import { LocaleProvider } from "../i18n";
import { ExplainNumber } from "./explain";

// ExplainNumber: a converted aggregate that opens into the rows it was built
// from. The headline is the IR's base_value rendered verbatim; the rows are
// lineage, so a story's rows deliberately do NOT re-derive the headline —
// reading them as an alternative computation is the mistake the popover exists
// to prevent.
//
// No fetch and no query: the whole fixture arrives as a prop, so LocaleProvider
// is the only context needed. `workspaceZone` is the zone the rate DATES are
// read in (a reporting-period label, not the viewer's calendar), pinned here the
// way every other date-bearing catalog entry pins it.
//
// The popover is component-local state with no `startOpen` prop, so the open
// stories click the toggle in play(); fe-uat waits longer on a play-fn story
// before it captures.
const meta: Meta<typeof ExplainNumber> = {
  title: "Design System/Explain number",
  component: ExplainNumber,
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
type Story = StoryObj<typeof ExplainNumber>;

// One foreign row: the ordinary case, where "converted" means a single rate on
// a single day.
const oneCurrency: ExplainedMoney = {
  baseValueMinor: 4_182_000,
  baseCurrency: "EUR",
  rows: [
    {
      label: "Fleet retrofit, 40 vehicles",
      nativeAmountMinor: 4_500_000,
      nativeCurrency: "USD",
      rate: 0.9293,
      rateDate: "2026-06-30",
    },
  ],
};

// Three currencies and three rate dates — the case the popover is actually for:
// one figure that no single rate explains, where the dates differ per row
// because each contribution was fixed on its own day.
const severalCurrencies: ExplainedMoney = {
  baseValueMinor: 9_640_500,
  baseCurrency: "EUR",
  rows: [
    {
      label: "Fleet retrofit, 40 vehicles",
      nativeAmountMinor: 4_500_000,
      nativeCurrency: "USD",
      rate: 0.9293,
      rateDate: "2026-06-30",
    },
    {
      label: "Depot survey, Zürich",
      nativeAmountMinor: 1_280_000,
      nativeCurrency: "CHF",
      rate: 1.0651,
      rateDate: "2026-05-29",
    },
    {
      label: "Pallet pooling framework",
      nativeAmountMinor: 3_100_000,
      nativeCurrency: "GBP",
      rate: 1.1782,
      rateDate: "2026-04-30",
    },
  ],
};

async function openPopover({ canvasElement }: { canvasElement: HTMLElement }) {
  const canvas = within(canvasElement);
  await userEvent.click(
    canvas.getByRole("button", { name: "Explain this number" }),
  );
}

// Closed: a figure in a running line, with a toggle small enough not to set the
// line's height.
export const Closed: Story = {
  args: { money: oneCurrency, workspaceZone: "Europe/Berlin" },
};

export const OpenOneCurrency: Story = {
  args: { money: oneCurrency, workspaceZone: "Europe/Berlin" },
  play: openPopover,
};

export const OpenSeveralCurrencies: Story = {
  args: { money: severalCurrencies, workspaceZone: "Europe/Berlin" },
  play: openPopover,
};

// Dark is where this popover has something to lose: it is a `.card` floating
// over the page ground with no scrim and no arrow, so the only thing separating
// lineage from the figures underneath it is the step between --bgCard and
// --bgPage plus one hairline — and that step is what a darker palette
// compresses. The mono amounts and the rate line are the two inks on trial with
// it.
export const OpenSeveralCurrenciesDark: Story = {
  globals: { theme: "dark" },
  args: { money: severalCurrencies, workspaceZone: "Europe/Berlin" },
  play: openPopover,
};
