// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { screen, userEvent, within } from "storybook/test";
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
  title: "Settings/Admin settings/Integrations/Overlay",
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

// Region and token are two inputs submitted together, so they live behind the
// row's verb in the confirm that names the org-wide consequence (every seat's
// reads switch source) — the sentence is on screen while the token is pasted,
// and the dialog's own button is the only press that POSTs. This story captures
// that dialog open, before any confirm click, so the gate itself is visible in
// the render gallery.
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
    await userEvent.click(
      await canvas.findByRole("button", { name: "Connect HubSpot" }),
    );
    // `screen`, not the canvas: Modal portals to document.body, so a
    // canvas-scoped query for anything inside the dialog rejects — and a
    // rejecting play() used to report after the gate had already screenshotted
    // and passed the story.
    await userEvent.type(
      await screen.findByLabelText("Private-app token"),
      "pat-secret",
    );
    await screen.findByText(/switches every seat's reads to HubSpot/);
  },
};

// A seat with no overlay grant. The row keeps its place and the verb is refused
// WITH the reason beside it, rather than vanishing — an absent Connect on a card
// that reports a connection reads as "there is nothing to connect", which is a
// claim about the installation standing in for one about authority. What to check
// is that the refused button reads as unavailable and the sentence beside it as
// prose, not as a second control.
export const ConnectRefused: Story = {
  render: () => {
    installFetchStub({
      "GET /me": () => jsonResponse(meFixture({ allow: {} })),
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

// The shed band in dark — the card at its loudest, so the tones can be read
// against each other rather than one at a time. Three reds and a green share the
// frame: the danger Badge on the recessed plate, the Meter drawn nearly full
// against its track, the sync rows' success chips above it, and the danger
// Disconnect in the action band. --danger lightens in dark the way --accent does
// while --dangerBg only deepens its alpha, which leaves the danger CHIP the
// thinnest of the tones — and the filled danger BUTTON is a different question
// again, because its ink is --textOnStatusControl, not --danger (base.css).
export const BudgetShedDark: Story = {
  globals: { theme: "dark" },
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

// The live card at 390px. This is the width overlay.css was rewritten for: every
// figure row here used to be an inline style with its own private answer to
// whether it wraps, and the row that answered "no" is what pushed the card past a
// phone. What to check is that the status/connected-at/region line, each sync
// row's identifier + chip + timestamp, and the budget's figure row all wrap
// inside the plate — and that Reconcile and Disconnect stay together, rather than
// one of them leaving the band, since a Button's own label never wraps.
//
// Storybook applies the viewport from the MANAGER, by resizing the preview
// iframe — so the fe-uat capture, which loads a bare iframe.html, renders this at
// the harness's own width and its PNG is NOT a picture of a phone. Review it in
// Storybook, or by narrowing the browser.
export const ActiveFreshPhone: Story = {
  globals: { viewport: { value: "phone" } },
  tags: ["uat-phone"],
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
