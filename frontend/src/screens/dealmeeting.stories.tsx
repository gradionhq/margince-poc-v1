// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import type { components } from "../api/schema";
import { LocaleProvider } from "../i18n";
import { DealNextMeeting } from "./dealmeeting";

const meta: Meta<typeof DealNextMeeting> = {
  title: "Screens/Deal next meeting",
  component: DealNextMeeting,
  parameters: { layout: "padded" },
};
export default meta;
type Story = StoryObj<typeof DealNextMeeting>;

type Activity = components["schemas"]["Activity"];

const BOOKED = {
  id: "act-1",
  kind: "meeting",
  subject: "Kick-off with Laura",
  occurred_at: "2999-03-04T10:00:00Z",
  meeting_status: "booked",
  source: "manual",
  created_at: "2026-08-20T09:00:00Z",
  updated_at: "2026-08-20T09:00:00Z",
} as Activity;

function Served({ children }: Readonly<{ children: ReactNode }>) {
  const client = new QueryClient();
  client.setQueryData(["deal-meetings", "deal-1"], {
    data: [BOOKED],
    page: {},
  });
  return (
    <QueryClientProvider client={client}>
      <LocaleProvider initial="en">{children}</LocaleProvider>
    </QueryClientProvider>
  );
}

/** A meeting on the calendar, brief one click away. */
export const Booked: Story = {
  render: () => (
    <Served>
      <DealNextMeeting dealId="deal-1" />
    </Served>
  ),
};
