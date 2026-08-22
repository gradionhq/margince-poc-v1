// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { LocaleProvider } from "../i18n";
import { DealRoomAside } from "./dealroom";

// The states a rep meets this surface in, so each can be judged without
// arranging a room, a buyer and a close on a running stack.

const meta: Meta<typeof DealRoomAside> = {
  title: "Screens/Deal Room aside",
  component: DealRoomAside,
  parameters: { layout: "padded" },
};
export default meta;
type Story = StoryObj<typeof DealRoomAside>;

function room(state: string) {
  return {
    id: "room-1",
    deal_id: "deal-1",
    title: "Acme Expansion — Deal Room",
    state,
    source: "manual",
    version: 1,
    created_at: "2026-08-22T09:00:00Z",
    updated_at: "2026-08-22T09:00:00Z",
    release_count: 1,
  };
}

// Answers the reads the aside makes. A story that hits the real API shows
// whatever that installation happens to hold, which is not a state anybody
// chose to review.
function Served({
  rooms,
  children,
}: Readonly<{ rooms: unknown[]; children: ReactNode }>) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  client.setQueryData(["deal-rooms", "deal-1"], { data: rooms, page: {} });
  client.setQueryData(["deal-room-documents", "room-1"], {
    data: [],
    page: {},
  });
  client.setQueryData(["deal-room-threads", "room-1"], { data: [], page: {} });
  client.setQueryData(["deal-room-decisions", "room-1"], { data: [] });
  return (
    <QueryClientProvider client={client}>
      <LocaleProvider initial="en">{children}</LocaleProvider>
    </QueryClientProvider>
  );
}

/** A live room mid-negotiation. */
export const Live: Story = {
  render: () => (
    <Served rooms={[room("live")]}>
      <DealRoomAside dealId="deal-1" dealName="Acme Expansion" />
    </Served>
  ),
};

/** A room nobody has published yet — a draft the buyer cannot see. */
export const Draft: Story = {
  render: () => (
    <Served rooms={[room("draft")]}>
      <DealRoomAside dealId="deal-1" dealName="Acme Expansion" />
    </Served>
  ),
};

/**
 * A closed room. Every control states why it refuses, and the add form is gone
 * rather than present and refusing — the state this surface most needs a second
 * look at, because it is the one a rep meets months after the deal.
 */
export const Closed: Story = {
  render: () => (
    <Served rooms={[room("closed")]}>
      <DealRoomAside dealId="deal-1" dealName="Acme Expansion" />
    </Served>
  ),
};
