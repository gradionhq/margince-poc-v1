// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { screen, userEvent, within } from "storybook/test";
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
  title: "Settings/Admin settings/Integrations/Contact data provider",
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

/**
 * The key field itself, which now lives behind the row's verb.
 *
 * Opened by a `play` rather than left to a reader's click, because the dialog is
 * where the one input on this card lives: without it the render gate captures a
 * row and proves nothing about the field, the warning that a connect costs
 * money, or the submit's pending state.
 */
export const ConnectDialog: Story = {
  render: cardStory(OPERATOR, [unconnected]),
  play: async ({ canvasElement }) => {
    const user = userEvent.setup();
    await user.click(
      await within(canvasElement).findByRole("button", { name: /connect/i }),
    );
    // Asserted, not merely awaited: a `play` that clicks and moves on records a
    // screenshot of whatever happened, and a dialog that failed to open looks
    // like a story nobody wrote a state for.
    await screen.findByRole("dialog");
  },
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

// No adapter is compiled in. An empty list and a 501 both land here, because
// the card collapses them into one honest no-provider state rather than a
// failure — which is why there is ONE story and not two: the second was the
// same picture under a second name. The 501 path is drawn where it differs, in
// the person record's own provider section.
export const NoProvider: Story = {
  render: cardStory(OPERATOR, []),
};

// The connected card in dark, which is where this file's colour actually lives:
// a `connected` Badge composites its tint over --bgElevated whatever it sits on,
// and here it sits on the recessed --bgCard plate; the credit Meter's track and
// fill are two greens a step apart; and the spend table separates five columns
// with nothing but --borderSubtle hairlines. The one to check hardest is
// .provider-held — a held figure is deliberately quieter than the charge beside
// it so nobody adds the two together, and "quieter" is a --textSecondary /
// --textContent pair that has to stay distinguishable after both re-resolve.
export const OperatorConnectedDark: Story = {
  globals: { theme: "dark" },
  render: cardStory(OPERATOR, [connected]),
};

// The connected card at 390px. Three claims integrations-provider.css makes
// about this width, none of them visible at desktop: the identity row wraps
// rather than letting a registry key and a status pill overflow it, the
// five-column spend table scrolls inside its own .table-scroll box rather than
// pushing the page sideways, and the submit + the OverflowMenu holding the two
// irreversible verbs wrap as a pair instead of squeezing.
//
// Storybook applies the viewport from the MANAGER, by resizing the preview
// iframe — so the fe-uat capture, which loads a bare iframe.html, renders this
// at the harness's own width and its PNG is NOT a picture of a phone. Review it
// in Storybook, or by narrowing the browser.
export const OperatorConnectedPhone: Story = {
  globals: { viewport: { value: "phone" } },
  tags: ["uat-phone"],
  render: cardStory(OPERATOR, [connected]),
};
