/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  cleanup,
  render as rtlRender,
  screen,
  waitFor,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { components } from "../api/schema";
import { LocaleProvider } from "../i18n";
import { OverlayCard } from "./overlay";

// The overlay card renders the incumbent connection lifecycle and its two
// health reads off server facts only: a 404 reads as "never connected", a
// 501 reads as "this deployment never wired overlay mode", and a revoked or
// errored connection still shows what the server actually says rather than
// collapsing into a blank screen.

type Connection = components["schemas"]["OverlayConnection"];
type SyncStatus = components["schemas"]["OverlaySyncStatus"];
type Budget = components["schemas"]["OverlayBudget"];

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

const syncStatusFixture: SyncStatus = {
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

const budgetFixture: Budget = {
  window: "2026-07-25T08:00:00Z/PT1H",
  consumed: 120,
  limit: 1000,
  band: "warn",
  sources: { force_fresh: 10, poller: 100, capture: 10 },
  headroom: "~unknown",
  search: {
    window: "2026-07-25T08:00:00Z/PT1S",
    consumed: 2,
    limit: 20,
    band: "ok",
  },
};

function jsonResponse(body: unknown, status = 200) {
  return new Response(body === undefined ? null : JSON.stringify(body), {
    status,
    headers:
      body === undefined ? undefined : { "Content-Type": "application/json" },
  });
}

type RouteHandler = (request: Request) => Response | Promise<Response>;

// A minimal method+path router over the real fetch surface, mirroring the
// installFetchStub convention (story-utils.tsx) but local to this test file
// since it also needs to record every call for the invalidate/queued
// assertions below.
function stubApi(routes: Record<string, RouteHandler>): Request[] {
  const calls: Request[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (request: Request) => {
      calls.push(request);
      const path = new URL(request.url).pathname.replace(/^\/v1/, "");
      const key = `${request.method} ${path}`;
      const handler = routes[key];
      if (!handler) {
        throw new Error(`unstubbed: ${key}`);
      }
      return handler(request);
    }),
  );
  return calls;
}

function meRoute(roles: string[]): RouteHandler {
  return () =>
    jsonResponse({ user: { email: "ada@acme.test" }, roles, teams: [] });
}

function render(ui: ReactNode) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  const result = rtlRender(
    <QueryClientProvider client={client}>
      <LocaleProvider initial="en">{ui}</LocaleProvider>
    </QueryClientProvider>,
  );
  return { ...result, client };
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("the overlay card", () => {
  it("renders the not-connected empty state when the server has no connection", async () => {
    stubApi({
      "GET /me": meRoute(["admin"]),
      "GET /overlay/connection": () =>
        jsonResponse({ detail: "not found" }, 404),
    });
    render(<OverlayCard />);
    expect(await screen.findByText(/No incumbent is connected/)).toBeTruthy();
    expect(screen.getByLabelText("Private-app token")).toBeTruthy();
  });

  it("says overlay is unconfigured when the server answers 501", async () => {
    stubApi({
      "GET /me": meRoute(["admin"]),
      "GET /overlay/connection": () =>
        jsonResponse(
          { code: "not_implemented", detail: "overlay not wired" },
          501,
        ),
    });
    render(<OverlayCard />);
    expect(
      await screen.findByText(
        /Overlay mode isn't configured in this deployment/,
      ),
    ).toBeTruthy();
    expect(screen.queryByLabelText("Private-app token")).toBeNull();
  });

  it("does not offer actions to a non-admin seat", async () => {
    stubApi({
      "GET /me": meRoute(["rep"]),
      "GET /overlay/connection": () =>
        jsonResponse({ detail: "not found" }, 404),
    });
    render(<OverlayCard />);
    await screen.findByText(/No incumbent is connected/);
    expect(screen.queryByLabelText("Private-app token")).toBeNull();
    expect(
      await screen.findByText(
        /Ask an admin or ops teammate to connect or disconnect HubSpot/,
      ),
    ).toBeTruthy();
  });

  it("shows per-object sync rows and the budget band", async () => {
    stubApi({
      "GET /me": meRoute(["admin"]),
      "GET /overlay/connection": () => jsonResponse(activeConnection),
      "GET /overlay/sync-status": () => jsonResponse(syncStatusFixture),
      "GET /overlay/budget": () => jsonResponse(budgetFixture),
    });
    render(<OverlayCard />);
    expect(await screen.findByText("person")).toBeTruthy();
    expect(screen.getByText("Fresh")).toBeTruthy();
    expect(screen.getByText("deal")).toBeTruthy();
    expect(screen.getByText("Pending sync")).toBeTruthy();
    expect(screen.getByText("Approaching limit")).toBeTruthy();
    // The server's own `~unknown` sentinel prints verbatim — never a
    // computed substitute.
    expect(screen.getByText(/~unknown/)).toBeTruthy();
  });

  it("keeps showing sync and budget when the connection is in error", async () => {
    stubApi({
      "GET /me": meRoute(["admin"]),
      "GET /overlay/connection": () => jsonResponse(errorConnection),
      "GET /overlay/sync-status": () => jsonResponse(syncStatusFixture),
      "GET /overlay/budget": () => jsonResponse(budgetFixture),
    });
    render(<OverlayCard />);
    expect(await screen.findByText("Sync error")).toBeTruthy();
    expect(await screen.findByText("person")).toBeTruthy();
    expect(screen.getByText("Approaching limit")).toBeTruthy();
  });

  it("does not connect until the confirmation is accepted", async () => {
    const calls = stubApi({
      "GET /me": meRoute(["admin"]),
      "GET /overlay/connection": () =>
        jsonResponse({ detail: "not found" }, 404),
      "POST /overlay/connection": () => jsonResponse(activeConnection, 201),
    });
    render(<OverlayCard />);
    await userEvent.type(
      await screen.findByLabelText("Private-app token"),
      "pat-secret",
    );
    // Submitting the form only opens the confirmation — it must not POST yet.
    await userEvent.click(
      screen.getByRole("button", { name: "Connect HubSpot" }),
    );
    expect(
      calls.filter(
        (r) => r.url.endsWith("/overlay/connection") && r.method === "POST",
      ),
    ).toHaveLength(0);
    expect(
      await screen.findByText(/switches every seat's reads to HubSpot/),
    ).toBeTruthy();
    // Two buttons now share the label (the form's trigger, already submitted,
    // and the modal's own confirm) — the modal's is the last one in the DOM,
    // the same convention connectors.test.tsx's disconnect-confirm test uses.
    const confirms = screen.getAllByRole("button", { name: "Connect HubSpot" });
    await userEvent.click(confirms[confirms.length - 1]);
    await waitFor(() =>
      expect(
        calls.filter(
          (r) => r.url.endsWith("/overlay/connection") && r.method === "POST",
        ),
      ).toHaveLength(1),
    );
  });

  it("offers Reconnect for a revoked connection, gated by the same confirm step", async () => {
    const calls = stubApi({
      "GET /me": meRoute(["admin"]),
      "GET /overlay/connection": () => jsonResponse(revokedConnection),
      "POST /overlay/connection": () => jsonResponse(activeConnection, 201),
    });
    render(<OverlayCard />);
    expect(await screen.findByText("Revoked")).toBeTruthy();
    await userEvent.type(
      screen.getByLabelText("Private-app token"),
      "pat-secret",
    );
    await userEvent.click(screen.getByRole("button", { name: "Reconnect" }));
    expect(
      calls.filter(
        (r) => r.url.endsWith("/overlay/connection") && r.method === "POST",
      ),
    ).toHaveLength(0);
    const confirms = screen.getAllByRole("button", { name: "Reconnect" });
    await userEvent.click(confirms[confirms.length - 1]);
    await waitFor(() =>
      expect(
        calls.filter(
          (r) => r.url.endsWith("/overlay/connection") && r.method === "POST",
        ),
      ).toHaveLength(1),
    );
  });

  it("invalidates every query after a successful connect", async () => {
    stubApi({
      "GET /me": meRoute(["admin"]),
      "GET /overlay/connection": () =>
        jsonResponse({ detail: "not found" }, 404),
      "POST /overlay/connection": () => jsonResponse(activeConnection, 201),
    });
    const { client } = render(<OverlayCard />);
    await screen.findByLabelText("Private-app token");
    await userEvent.type(
      screen.getByLabelText("Private-app token"),
      "pat-secret",
    );
    await userEvent.click(
      screen.getByRole("button", { name: "Connect HubSpot" }),
    );
    const invalidateSpy = vi.spyOn(client, "invalidateQueries");
    const confirms = await screen.findAllByRole("button", {
      name: "Connect HubSpot",
    });
    await userEvent.click(confirms[confirms.length - 1]);
    await waitFor(() => expect(invalidateSpy).toHaveBeenCalled());
    // Called with no arguments — the whole cache, not one targeted key —
    // because the workspace's data source itself just changed (/me included).
    expect(invalidateSpy).toHaveBeenCalledWith();
  });

  it("surfaces a concurrent already-connected conflict instead of guessing", async () => {
    stubApi({
      "GET /me": meRoute(["admin"]),
      "GET /overlay/connection": () =>
        jsonResponse({ detail: "not found" }, 404),
      "POST /overlay/connection": () =>
        jsonResponse(
          {
            code: "incumbent_already_connected",
            detail: "an active incumbent connection already exists",
          },
          409,
        ),
    });
    render(<OverlayCard />);
    await userEvent.type(
      await screen.findByLabelText("Private-app token"),
      "pat-secret",
    );
    await userEvent.click(
      screen.getByRole("button", { name: "Connect HubSpot" }),
    );
    const confirms = await screen.findAllByRole("button", {
      name: "Connect HubSpot",
    });
    await userEvent.click(confirms[confirms.length - 1]);
    expect(
      await screen.findByText(/an active incumbent connection already exists/),
    ).toBeTruthy();
  });

  it("does not offer reconcile/disconnect to a non-admin seat on a live connection", async () => {
    stubApi({
      "GET /me": meRoute(["rep"]),
      "GET /overlay/connection": () => jsonResponse(activeConnection),
      "GET /overlay/sync-status": () => jsonResponse(syncStatusFixture),
      "GET /overlay/budget": () => jsonResponse(budgetFixture),
    });
    render(<OverlayCard />);
    // The health rows still render (read is granted to every role) — only
    // the mutating actions are withheld.
    expect(await screen.findByText("person")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Sync now" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Disconnect" })).toBeNull();
  });

  it("reports a queued sweep rather than a finished one", async () => {
    stubApi({
      "GET /me": meRoute(["admin"]),
      "GET /overlay/connection": () => jsonResponse(activeConnection),
      "GET /overlay/sync-status": () => jsonResponse(syncStatusFixture),
      "GET /overlay/budget": () => jsonResponse(budgetFixture),
      "POST /overlay/reconcile": () => jsonResponse(undefined, 202),
    });
    render(<OverlayCard />);
    await userEvent.click(
      await screen.findByRole("button", { name: "Sync now" }),
    );
    expect(await screen.findByText(/Sweep queued/)).toBeTruthy();
    expect(screen.queryByText(/finished/i)).toBeNull();
  });

  it("names the purge in the disconnect confirm", async () => {
    stubApi({
      "GET /me": meRoute(["admin"]),
      "GET /overlay/connection": () => jsonResponse(activeConnection),
      "GET /overlay/sync-status": () => jsonResponse(syncStatusFixture),
      "GET /overlay/budget": () => jsonResponse(budgetFixture),
    });
    render(<OverlayCard />);
    await userEvent.click(
      await screen.findByRole("button", { name: "Disconnect" }),
    );
    expect(await screen.findByText(/purges the mirrored data/)).toBeTruthy();
  });
});
