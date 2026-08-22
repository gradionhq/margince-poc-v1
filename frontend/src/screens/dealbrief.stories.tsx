// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { LocaleProvider } from "../i18n";
import { DealBriefCard } from "./dealbrief";

const meta: Meta<typeof DealBriefCard> = {
  title: "Screens/Deal brief",
  component: DealBriefCard,
  parameters: { layout: "padded" },
};
export default meta;
type Story = StoryObj<typeof DealBriefCard>;

const BRIEF = {
  deal_id: "deal-1",
  generated_at: "2026-08-23T09:00:00Z",
  generated_by: "deterministic",
  sections: [
    {
      kind: "standing",
      sentences: [
        {
          text: "Acme rollout is open at 12000.00 EUR, expected to close 2 Sep 2026.",
          evidence: [
            { entity_type: "deal", entity_id: "deal-1", name: "Acme rollout" },
          ],
        },
        {
          text: "Health reads 80 of 100; 3 days in the current stage against about 14 for won deals.",
          evidence: [{ entity_type: "deal", entity_id: "deal-1" }],
        },
      ],
    },
    {
      kind: "activity",
      sentences: [
        {
          text: "Last activity: Re: MSA, 3 days ago.",
          evidence: [
            { entity_type: "activity", entity_id: "act-1", name: "Re: MSA" },
          ],
        },
      ],
    },
    {
      kind: "room",
      sentences: [
        {
          text: 'Deal Room "Acme room" is live, published 2 time(s).',
          evidence: [{ entity_type: "deal", entity_id: "deal-1" }],
        },
      ],
    },
  ],
};

function Served({
  brief,
  children,
}: Readonly<{ brief: unknown; children: ReactNode }>) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  client.setQueryData(["deal-brief", "deal-1"], brief);
  return (
    <QueryClientProvider client={client}>
      <LocaleProvider initial="en">{children}</LocaleProvider>
    </QueryClientProvider>
  );
}

/** A deal mid-negotiation with a live room. */
export const Live: Story = {
  render: () => (
    <Served brief={BRIEF}>
      <DealBriefCard dealId="deal-1" />
    </Served>
  ),
};

/** A deal nothing has happened on. */
export const Empty: Story = {
  render: () => (
    <Served brief={{ ...BRIEF, sections: [] }}>
      <DealBriefCard dealId="deal-1" />
    </Served>
  ),
};
