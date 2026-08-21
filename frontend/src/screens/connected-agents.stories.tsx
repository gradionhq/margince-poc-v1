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
//
// Each connection is one `SettingRow` — the client's name on the left, its
// state in the value slot, the verb that ends it on the right — and the connect
// guide is a `Disclosure` at the foot of the same list. Which stories show the
// guide OPEN is therefore not a story setting: it opens itself while nothing is
// connected, because that is the one state in which it is the only thing on the
// card to act on. So `Connected` is the closed case and `NoneConnected` /
// `ConnectorNotEnabled` are the open ones.
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

// The connect guide asks the OAuth discovery document whether this installation
// serves the governed tool surface at all — a well-known path, not a /v1 one, so
// it needs routing like any other. Left unrouted it fell through to the stub's
// empty-list fallback, which carries no `resource`, and the guide rendered its
// own failure state under a story named for a working card.
function story(passports: Record<string, unknown>[], connectorEnabled = true) {
  return () => {
    globalThis.localStorage.setItem("margince.workspaceSlug", "acme");
    installFetchStub({
      "GET /me": meRoute({}),
      "GET /passports": () => jsonResponse({ data: passports }),
      "GET /.well-known/oauth-protected-resource": () =>
        connectorEnabled
          ? jsonResponse({ resource: "https://margince.example/mcp" })
          : jsonResponse({ code: "not_found" }, 404),
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
// The guide stands open under it, which is what makes the empty state an
// instruction rather than a dead end.
export const NoneConnected: Story = { render: story([]) };

// The installation does not serve the tool surface, so discovery 404s and the
// guide is absent rather than broken — a capability this deployment does not
// have, which is the one cause that justifies a surface not being there.
export const ConnectorNotEnabled: Story = {
  render: story([], false),
};

// The roster in dark. The lapsed row is the case: it says "over" by striking its
// facts through and putting a danger badge beside them, and the code deliberately
// strikes rather than dims to hold an AA floor (B-EP09.21) — a rule written
// against one set of token values and never once rendered against the other.
// The `SettingList` hairline between the two rows is the other thing to read
// here: it has to separate two connections without reading as heavier than the
// disclosure rule under them.
export const ConnectedDark: Story = {
  globals: { theme: "dark" },
  render: story([CLAUDE, LAPSED]),
};

// At 390px the row gives up its two columns and stacks (settingrow.css's own
// breakpoint), which is the width the wrap used to go wrong at: the verb landed
// BETWEEN the facts and the scope chips, so the chips read as belonging to the
// control rather than to the connection above it, and the struck-through lapsed
// row was where that misreading cost something. The chips now sit inside the
// row's naming half, under the facts they qualify, so the stack cannot separate
// them from the connection they describe.
export const ConnectedPhone: Story = {
  globals: { viewport: { value: "phone" } },
  tags: ["uat-phone"],
  render: story([CLAUDE, LAPSED]),
};
