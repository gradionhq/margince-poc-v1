// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import type { components } from "../api/schema";
import { type GrantSpec, meFixture } from "../app/mefixture";
import { ProviderCard } from "./integrations-provider";
import { installFetchStub, jsonResponse, StoryProviders } from "./story-utils";

// ProviderCard stories for the fe-uat render gate: the same three postures
// integrations-provider.test.tsx asserts (every write, connect-only, read-only)
// plus the two calm no-provider states, all off the GET /provider-connections
// shape.
//
// Every story routes GET /me, and the grants are the story's whole subject:
// this card's affordances are scoped by three separate ones, and a story that
// left the probe unrouted would silently capture the denied branch under a name
// claiming otherwise.

type ProviderConnection = components["schemas"]["ProviderConnection"];

const OPERATOR: GrantSpec = {
  integrations: ["create", "read", "update", "delete"],
};
// A seat that may bind a key but may not destroy what it bought — nothing seeds
// this, and an operator editing a role can produce it.
const CONNECT_ONLY: GrantSpec = { integrations: ["create", "read"] };
const READER: GrantSpec = { integrations: ["read"] };

const connected: ProviderConnection = {
  provider: "surfe",
  status: "connected",
  credential_present: true,
  configuration: {
    mode: "automatic_on_create",
    preset: "full",
    automatic_individual_create: true,
    automatic_import: false,
    categories: { professional: true },
  },
  credits: { pools: { email: 1840, mobile: 210 } },
  effective_constraints: ["EU residency", "no consumer mobile"],
  spend: {
    months: [
      {
        month: "2026-08-01",
        pool: "email",
        charged_credits: 312,
        held_credits: 8,
        runs: 74,
      },
      {
        month: "2026-07-01",
        pool: "email",
        charged_credits: 1204,
        held_credits: 0,
        runs: 291,
      },
      {
        month: "2026-07-01",
        pool: "mobile",
        charged_credits: 96,
        held_credits: 12,
        runs: 31,
      },
    ],
  },
  version: 4,
  created_at: "2026-01-05T09:00:00Z",
  updated_at: "2026-08-05T09:04:00Z",
};

// No key yet: the provider is registered, so the row exists and the key field
// with it, but there is no balance to read and nothing to destroy.
const unconnected: ProviderConnection = {
  provider: "surfe",
  status: "disconnected",
  credential_present: false,
  configuration: {
    mode: "on_demand",
    preset: "full",
    automatic_individual_create: false,
    automatic_import: false,
    categories: { professional: true },
  },
  credits: { pools: {} },
  version: 1,
  created_at: "2026-01-05T09:00:00Z",
  updated_at: "2026-01-05T09:00:00Z",
};

function cardStory(allow: GrantSpec, connections: ProviderConnection[]) {
  return () => {
    installFetchStub({
      "GET /me": () => jsonResponse(meFixture({ allow })),
      "GET /provider-connections": () => jsonResponse({ data: connections }),
    });
    return (
      <StoryProviders>
        <ProviderCard />
      </StoryProviders>
    );
  };
}

const meta: Meta<typeof ProviderCard> = {
  title: "Settings/Organization/Integrations/Contact data provider",
  component: ProviderCard,
};
export default meta;
type Story = StoryObj<typeof ProviderCard>;

export const OperatorConnected: Story = {
  render: cardStory(OPERATOR, [connected]),
};

export const OperatorNotYetConnected: Story = {
  render: cardStory(OPERATOR, [unconnected]),
};

// May bind a key, may not destroy what it bought — so the overflow that holds
// disconnect and delete-data is not offered at all.
export const ConnectOnly: Story = {
  render: cardStory(CONNECT_ONLY, [connected]),
};

// The reading stays — a rep's explanation for a dated value on a person record
// — and the card says once why nothing here is writable.
export const ReadOnlySeat: Story = {
  render: cardStory(READER, [connected]),
};

// No adapter is compiled in. An empty list and a 501 mean the same thing, and
// both read as the honest no-provider state rather than as a failure.
export const NoProvider: Story = {
  render: cardStory(OPERATOR, []),
};

export const NotConfigured: Story = {
  render: () => {
    installFetchStub({
      "GET /me": () => jsonResponse(meFixture({ allow: OPERATOR })),
      "GET /provider-connections": () =>
        jsonResponse({ code: "not_implemented" }, 501),
    });
    return (
      <StoryProviders>
        <ProviderCard />
      </StoryProviders>
    );
  },
};
