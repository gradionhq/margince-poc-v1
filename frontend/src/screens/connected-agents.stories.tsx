// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { ConnectedAgentsCard } from "./connected-agents";
import {
  installFetchStub,
  jsonResponse,
  meRoute,
  StoryProviders,
} from "./story-utils";

// Which OAuth clients are holding one of this person's passports. A passport
// with no `connection` is a minted credential nobody has redeemed, so it is not
// a connection and does not appear here.
const CLAUDE = {
  id: "pp-1",
  label: "Claude Desktop",
  revoked_at: null,
  expires_at: "2026-12-01T00:00:00Z",
  scopes: ["read", "draft"],
  connection: {
    client_name: "Claude Desktop",
    connected_at: "2026-07-30T14:10:00Z",
    renewable: true,
    lent_passport_label: null,
  },
};

// Expired and not renewable: the client has to be reconnected from its own end,
// which is a different sentence from "this lapsed and will come back".
const LAPSED = {
  id: "pp-2",
  label: "Scout",
  revoked_at: null,
  expires_at: "2026-01-04T00:00:00Z",
  scopes: ["read"],
  connection: {
    client_name: "Scout",
    connected_at: "2025-12-01T09:00:00Z",
    renewable: false,
    lent_passport_label: "Ops runner",
  },
};

function story(passports: Record<string, unknown>[]) {
  return () => {
    globalThis.localStorage.setItem("margince.workspaceSlug", "acme");
    installFetchStub({
      "GET /me": meRoute({}),
      "GET /passports": () => jsonResponse({ data: passports }),
    });
    return (
      <StoryProviders>
        <ConnectedAgentsCard />
      </StoryProviders>
    );
  };
}

const meta: Meta<typeof ConnectedAgentsCard> = {
  title: "Settings/You/Agents/Connected agents",
  component: ConnectedAgentsCard,
};
export default meta;
type Story = StoryObj<typeof ConnectedAgentsCard>;

export const Connected: Story = { render: story([CLAUDE, LAPSED]) };

// Nobody has connected YET — written out rather than left to the generic empty
// state, because "nothing here" beside a connect guide reads as a failed load.
export const NoneConnected: Story = { render: story([]) };
