// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { userEvent, within } from "storybook/test";
import type { components } from "../api/schema";
import { meFixture } from "../app/mefixture";
import { OverlayCard } from "./overlay";
import { installFetchStub, jsonResponse, StoryProviders } from "./story-utils";

// OverlayCard stories for the fe-uat render gate: the not-yet-connected
// empty state, the connect confirm-first gate, the connected states crossed
// with the sync/budget bands the health panel branches on, an errored
// connection (which still shows its health, per overlay.tsx's `live` doc),
// a revoked connection's Reconnect affordance, and the deployment-
// unconfigured (501) calm state — all off the same wire shapes
// overlay.test.tsx exercises.

type Connection = components["schemas"]["OverlayConnection"];
type SyncStatus = components["schemas"]["OverlaySyncStatus"];
type Budget = components["schemas"]["OverlayBudget"];

function admin() {
  return () =>
    jsonResponse(
      meFixture({
        allow: {
          overlay_connection: ["read", "create", "update", "delete"],
        },
      }),
    );
}

const activeConnection: Connection = {
  incumbent: "hubspot",
  region: "eu1",
  status: "active",
  connectedAt: "2026-07-20T10:00:00Z",
  scopes: ["crm.objects.contacts.read"],
};

const revokedConnection: Connection = {
  ...activeConnection,
  status: "revoked",
};
const errorConnection: Connection = { ...activeConnection, status: "error" };

const freshSyncStatus: SyncStatus = {
  objects: [
    {
      object: "person",
      lastSyncedAt: "2026-07-25T08:00:00Z",
      state: "fresh",
      backfillComplete: true,
    },
    {
      object: "deal",
      lastSyncedAt: "2026-07-25T08:00:00Z",
      state: "fresh",
      backfillComplete: true,
    },
  ],
};

const backfillingSyncStatus: SyncStatus = {
  objects: [
    {
      object: "person",
      lastSyncedAt: "2026-07-25T08:00:00Z",
      state: "fresh",
      backfillComplete: true,
    },
    {
      object: "deal",
      lastSyncedAt: "2026-07-25T07:00:00Z",
      state: "pending_sync",
      backfillComplete: false,
    },
  ],
};

function budgetFixture(band: Budget["band"]): Budget {
  return {
    window: "2026-07-25T08:00:00Z/PT1H",
    consumed: band === "shed" ? 980 : band === "warn" ? 750 : 100,
    limit: 1000,
    band,
    sources: { force_fresh: 20, poller: 700, capture: 30 },
    headroom: band === "shed" ? "0" : "~unknown",
    search: {
      window: "2026-07-25T08:00:00Z/PT1S",
      consumed: 2,
      limit: 20,
      band: "ok",
    },
  };
}

const meta: Meta<typeof OverlayCard> = {
  title: "screens/overlay",
  component: OverlayCard,
};
export default meta;
type Story = StoryObj<typeof OverlayCard>;

export const NotConnected: Story = {
  render: () => {
    installFetchStub({
      "GET /me": admin(),
      "GET /overlay/connection": () =>
        jsonResponse({ detail: "not found" }, 404),
    });
    return (
      <StoryProviders>
        <OverlayCard />
      </StoryProviders>
    );
  },
};

// Submitting the connect form never mutates directly — it opens the
// confirm-first dialog naming the org-wide consequence (every seat's reads
// switch source). This story captures that dialog open, before any confirm
// click, so the gate itself is visible in the render gallery.
export const ConnectConfirm: Story = {
  render: () => {
    installFetchStub({
      "GET /me": admin(),
      "GET /overlay/connection": () =>
        jsonResponse({ detail: "not found" }, 404),
      "POST /overlay/connection": () => new Promise<Response>(() => {}),
    });
    return (
      <StoryProviders>
        <OverlayCard />
      </StoryProviders>
    );
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.type(
      await canvas.findByLabelText("Private-app token"),
      "pat-secret",
    );
    await userEvent.click(
      canvas.getByRole("button", { name: "Connect HubSpot" }),
    );
    await canvas.findByText(/switches every seat's reads to HubSpot/);
  },
};

export const ActiveBackfilling: Story = {
  render: () => {
    installFetchStub({
      "GET /me": admin(),
      "GET /overlay/connection": () => jsonResponse(activeConnection),
      "GET /overlay/sync-status": () => jsonResponse(backfillingSyncStatus),
      "GET /overlay/budget": () => jsonResponse(budgetFixture("ok")),
    });
    return (
      <StoryProviders>
        <OverlayCard />
      </StoryProviders>
    );
  },
};

export const ActiveFresh: Story = {
  render: () => {
    installFetchStub({
      "GET /me": admin(),
      "GET /overlay/connection": () => jsonResponse(activeConnection),
      "GET /overlay/sync-status": () => jsonResponse(freshSyncStatus),
      "GET /overlay/budget": () => jsonResponse(budgetFixture("ok")),
    });
    return (
      <StoryProviders>
        <OverlayCard />
      </StoryProviders>
    );
  },
};

export const BudgetWarn: Story = {
  render: () => {
    installFetchStub({
      "GET /me": admin(),
      "GET /overlay/connection": () => jsonResponse(activeConnection),
      "GET /overlay/sync-status": () => jsonResponse(freshSyncStatus),
      "GET /overlay/budget": () => jsonResponse(budgetFixture("warn")),
    });
    return (
      <StoryProviders>
        <OverlayCard />
      </StoryProviders>
    );
  },
};

export const BudgetShed: Story = {
  render: () => {
    installFetchStub({
      "GET /me": admin(),
      "GET /overlay/connection": () => jsonResponse(activeConnection),
      "GET /overlay/sync-status": () => jsonResponse(freshSyncStatus),
      "GET /overlay/budget": () => jsonResponse(budgetFixture("shed")),
    });
    return (
      <StoryProviders>
        <OverlayCard />
      </StoryProviders>
    );
  },
};

// An errored connection still shows sync + budget (overlay.tsx's `live`
// doc): a mirror and a spent budget window remain reportable even though
// the connection itself needs attention.
export const ErrorStatus: Story = {
  render: () => {
    installFetchStub({
      "GET /me": admin(),
      "GET /overlay/connection": () => jsonResponse(errorConnection),
      "GET /overlay/sync-status": () => jsonResponse(freshSyncStatus),
      "GET /overlay/budget": () => jsonResponse(budgetFixture("warn")),
    });
    return (
      <StoryProviders>
        <OverlayCard />
      </StoryProviders>
    );
  },
};

export const Revoked: Story = {
  render: () => {
    installFetchStub({
      "GET /me": admin(),
      "GET /overlay/connection": () => jsonResponse(revokedConnection),
    });
    return (
      <StoryProviders>
        <OverlayCard />
      </StoryProviders>
    );
  },
};

// This deployment never wired the secret vault the overlay module needs
// (no MARGINCE_KEYVAULT_ROOT_KEY) — every /overlay/* op answers 501
// not_implemented, the same calm feature-off posture connectors.tsx's
// NotConfigured story renders for capture.
export const Unconfigured: Story = {
  render: () => {
    installFetchStub({
      "GET /me": admin(),
      "GET /overlay/connection": () =>
        jsonResponse(
          { code: "not_implemented", detail: "overlay not wired" },
          501,
        ),
    });
    return (
      <StoryProviders>
        <OverlayCard />
      </StoryProviders>
    );
  },
};
