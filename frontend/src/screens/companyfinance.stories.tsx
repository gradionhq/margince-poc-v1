// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import type { components } from "../api/schema";
import { CompanyFinanceCard } from "./companyfinance";
import { installFetchStub, jsonResponse, StoryProviders } from "./story-utils";

// The finance card's own rule: no figure is invented, and the absence of one
// is never drawn as a zero. `no_connection` and `connected` render as
// entirely different cards under the SAME title, which is the state this
// lane exists to pin — a demo workspace's seeded connector never shows the
// disconnected read, so this story is the only place it renders.

const meta: Meta = {
  title: "Screens/Company finance",
  parameters: { layout: "padded" },
};
export default meta;

type Story = StoryObj;
type FinanceSummary = components["schemas"]["OrganizationFinanceSummary"];

const connected: FinanceSummary = {
  organization_id: "o-1",
  state: "connected",
  provider: "offline_demo",
  last_synced_at: "2026-08-10T06:00:00Z",
  net_invoiced: { amount_minor: 1_864_200, currency: "EUR" },
  open_balance: { amount_minor: 240_000, currency: "EUR" },
  overdue: { amount_minor: 89_000, currency: "EUR" },
  median_days_after_due: 4,
  recent_invoices: [
    {
      id: "inv-1",
      number: "RE-2026-0512",
      issued_at: "2026-07-01",
      due_at: "2026-07-31",
      paid_at: "2026-08-04T00:00:00Z",
      status: "paid",
      currency: "EUR",
      gross_minor: 155_000,
      // Settled, so nothing of it is still outstanding.
      open_minor: 0,
      days_late: 4,
    },
    {
      id: "inv-2",
      number: "RE-2026-0533",
      issued_at: "2026-07-20",
      due_at: "2026-08-19",
      status: "overdue",
      currency: "EUR",
      gross_minor: 89_000,
      // Unpaid in full, which is what the overdue total above is made of.
      open_minor: 89_000,
      days_late: 2,
    },
  ],
};

const noConnection: FinanceSummary = {
  organization_id: "o-1",
  state: "no_connection",
};

function Finance({
  summary,
  lifecycle,
}: Readonly<{ summary: FinanceSummary; lifecycle?: string }>) {
  installFetchStub({
    "GET /organizations/o-1/finance-summary": () => jsonResponse(summary),
  });
  return (
    <StoryProviders>
      <div style={{ maxWidth: 420 }}>
        <CompanyFinanceCard orgId="o-1" lifecycle={lifecycle} />
      </div>
    </StoryProviders>
  );
}

export const Connected: Story = {
  render: () => <Finance summary={connected} lifecycle="customer" />,
};

// §6 State B: the card that must never look like "€0 open" — the reason the
// state comes from the server rather than being derived from an empty figure.
export const NoConnection: Story = {
  render: () => <Finance summary={noConnection} lifecycle="customer" />,
};

// FIN-AC-3's second half: a former customer's figures are history, so the
// title says so even while the read is showing real money.
export const FormerCustomerHistorical: Story = {
  render: () => <Finance summary={connected} lifecycle="former_customer" />,
};
