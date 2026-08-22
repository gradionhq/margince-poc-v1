// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { BuyerRoomScreen } from "./buyerroom";
import {
  installFetchStub,
  jsonResponse,
  type RouteMap,
  StoryProviders,
} from "./story-utils";

// The buyer's Deal Room, one story per access state, so the five screens an
// outside person can land on are exercised without a backend in any state.

const PARTICIPANT = {
  id: "p-1",
  full_name: "Laura Buyer",
  email: "laura@buyer.example",
  capability: "comment",
};

const ROOM = {
  title: "Acme rollout",
  welcome_message: "Welcome, Laura. Everything about the rollout is here.",
  release_no: 2,
  released_at: "2026-08-22T09:00:00Z",
  steward_name: "Ada Admin",
};

function room(routes: RouteMap, session = true) {
  return () => {
    installFetchStub(routes);
    if (session) {
      globalThis.sessionStorage.setItem("margince.room.session", "mdrs_story");
    } else {
      globalThis.sessionStorage.removeItem("margince.room.session");
    }
    return (
      <StoryProviders>
        <BuyerRoomScreen />
      </StoryProviders>
    );
  };
}

const meta: Meta<typeof BuyerRoomScreen> = {
  title: "Signed out/Deal Room (buyer)",
  component: BuyerRoomScreen,
};
export default meta;

type Story = StoryObj<typeof BuyerRoomScreen>;

export const Live: Story = {
  render: room({
    "GET /public/rooms/me": () =>
      jsonResponse({
        access: "live",
        participant: PARTICIPANT,
        steward_name: "Ada Admin",
        room: ROOM,
      }),
  }),
};

export const Closed: Story = {
  render: room({
    "GET /public/rooms/me": () =>
      jsonResponse({
        access: "closed",
        participant: PARTICIPANT,
        steward_name: "Ada Admin",
        room: { ...ROOM, closed_at: "2026-08-22T10:00:00Z" },
      }),
  }),
};

export const Paused: Story = {
  render: room({
    "GET /public/rooms/me": () =>
      jsonResponse({
        access: "paused",
        participant: PARTICIPANT,
        steward_name: "Ada Admin",
      }),
  }),
};

export const Expired: Story = {
  render: room({
    "GET /public/rooms/me": () =>
      jsonResponse({
        access: "expired",
        participant: PARTICIPANT,
        steward_name: "Ada Admin",
      }),
  }),
};

export const DeadLink: Story = {
  render: room({}, false),
};
