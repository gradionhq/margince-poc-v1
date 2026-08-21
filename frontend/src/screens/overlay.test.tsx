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
import { type GrantSpec, meFixture } from "../app/mefixture";
import { LocaleProvider } from "../i18n";
import { OverlayCard } from "./overlay";

// The overlay card renders the incumbent connection lifecycle and its two
// health reads off server facts only: a 404 reads as "never connected", a
// 501 reads as "this deployment never wired overlay mode", and a revoked or
// errored connection still shows what the server actually says rather than
// collapsing into a blank screen.
//
// The lifecycle is ONE settings row now: the connection's facts as its answer,
// and the verb that changes them in its right column. Region + token are two
// inputs submitted together, so they live inside the confirm dialog that verb
// opens — which is why every connect assertion below presses the row's verb
// first and then works inside the dialog.

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

// Connect, reconcile and disconnect are create, update and delete on the same
// object — three different amounts of damage, so three grants.
const OVERLAY_OPERATOR: GrantSpec = {
  overlay_connection: ["create", "update", "delete"],
};

function meRoute(allow: GrantSpec): RouteHandler {
  return () => jsonResponse(meFixture({ allow }));
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

// Press the row's verb and hand back the token field inside the dialog it
// opens. The verb and the dialog's own confirm share a label, so a caller that
// needs the confirm takes the LAST match — the dialog is portalled to the end
// of the document, the same convention connectors.test.tsx's disconnect test
// uses.
async function openConnectDialog(
  user: ReturnType<typeof userEvent.setup>,
  verb: string,
) {
  await user.click(await screen.findByRole("button", { name: verb }));
  return await screen.findByLabelText("Private-app token");
}

function lastButton(name: string): HTMLElement {
  const matches = screen.getAllByRole("button", { name });
  return matches[matches.length - 1];
}

describe("the overlay card", () => {
  it("renders the not-connected empty state when the server has no connection", async () => {
    const user = userEvent.setup();
    stubApi({
      "GET /me": meRoute(OVERLAY_OPERATOR),
      "GET /overlay/connection": () =>
        jsonResponse({ detail: "not found" }, 404),
    });
    render(<OverlayCard />);
    expect(await screen.findByText(/No incumbent is connected/)).toBeTruthy();
    // The row states what is set now, and the token field is behind its verb
    // rather than standing open on the card.
    expect(screen.getByText("Not connected")).toBeTruthy();
    expect(screen.queryByLabelText("Private-app token")).toBeNull();
    expect(await openConnectDialog(user, "Connect HubSpot")).toBeTruthy();
  });

  it("says overlay is unconfigured when the server answers 501", async () => {
    stubApi({
      "GET /me": meRoute(OVERLAY_OPERATOR),
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
    // No row at all: a deployment without an overlay adapter has no connection
    // to report, so there is nothing for a verb to change.
    expect(
      screen.queryByRole("button", { name: "Connect HubSpot" }),
    ).toBeNull();
  });

  // The row keeps its place on a denial and SAYS it is not the reader's to
  // change — an absent verb on a card that reports a connection would read as
  // "there is nothing to connect", a claim about the installation standing in
  // for one about authority.
  it("refuses the connect verb, with the reason, without any overlay grant", async () => {
    const user = userEvent.setup();
    stubApi({
      "GET /me": meRoute({}),
      "GET /overlay/connection": () =>
        jsonResponse({ detail: "not found" }, 404),
    });
    render(<OverlayCard />);
    await screen.findByText(/No incumbent is connected/);
    expect(
      await screen.findByText(
        /You do not have permission to change the HubSpot connection/,
      ),
    ).toBeTruthy();
    const verb = screen.getByRole("button", { name: "Connect HubSpot" });
    expect(verb.hasAttribute("disabled")).toBe(true);
    // And the refusal holds: a refused verb opens nothing.
    await user.click(verb);
    expect(screen.queryByLabelText("Private-app token")).toBeNull();
  });

  // One grant at a time. connect/reconcile/disconnect are create/update/delete
  // on the same object; a fixture holding all three cannot catch a swap.
  it("offers the connect dialog on the create grant alone", async () => {
    const user = userEvent.setup();
    stubApi({
      "GET /me": meRoute({ overlay_connection: ["read", "create"] }),
      "GET /overlay/connection": () =>
        jsonResponse({ detail: "not found" }, 404),
    });
    render(<OverlayCard />);
    expect(await openConnectDialog(user, "Connect HubSpot")).toBeTruthy();
  });

  it("refuses the connect verb when only update and delete are granted", async () => {
    stubApi({
      "GET /me": meRoute({ overlay_connection: ["read", "update", "delete"] }),
      "GET /overlay/connection": () =>
        jsonResponse({ detail: "not found" }, 404),
    });
    render(<OverlayCard />);
    await screen.findByText(/No incumbent is connected/);
    expect(
      (
        await screen.findByRole("button", { name: "Connect HubSpot" })
      ).hasAttribute("disabled"),
    ).toBe(true);
    expect(screen.queryByLabelText("Private-app token")).toBeNull();
  });

  it("shows per-object sync rows and the budget band", async () => {
    stubApi({
      "GET /me": meRoute(OVERLAY_OPERATOR),
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

  it("renders a sync state or budget band this build doesn't recognize as the server's own raw value, not blank or literal 'undefined'", async () => {
    // A state/band the running server added after this frontend's schema
    // was generated — the honest fallback is the server's raw string, the
    // same rule `~unknown` headroom above already follows for a value the
    // server explicitly declines to compute for us.
    const unknownSyncStatus = {
      objects: [
        {
          object: "organization",
          lastSyncedAt: "2026-07-25T08:00:00Z",
          state: "syncing",
          backfillComplete: false,
        },
      ],
    };
    const unknownBandBudget = {
      window: "2026-07-25T08:00:00Z/PT1H",
      consumed: 5,
      limit: 1000,
      band: "critical",
      headroom: 995,
    };
    stubApi({
      "GET /me": meRoute(OVERLAY_OPERATOR),
      "GET /overlay/connection": () => jsonResponse(activeConnection),
      "GET /overlay/sync-status": () => jsonResponse(unknownSyncStatus),
      "GET /overlay/budget": () => jsonResponse(unknownBandBudget),
    });
    render(<OverlayCard />);
    expect(await screen.findByText("organization")).toBeTruthy();
    expect(screen.getByText("syncing")).toBeTruthy();
    expect(screen.getByText("critical")).toBeTruthy();
    expect(screen.queryByText("undefined")).toBeNull();
  });

  it("keeps showing sync and budget when the connection is in error", async () => {
    stubApi({
      "GET /me": meRoute(OVERLAY_OPERATOR),
      "GET /overlay/connection": () => jsonResponse(errorConnection),
      "GET /overlay/sync-status": () => jsonResponse(syncStatusFixture),
      "GET /overlay/budget": () => jsonResponse(budgetFixture),
    });
    render(<OverlayCard />);
    expect(await screen.findByText("Sync error")).toBeTruthy();
    expect(await screen.findByText("person")).toBeTruthy();
    expect(screen.getByText("Approaching limit")).toBeTruthy();
  });

  // The blast radius is stated BEFORE the only press that binds the
  // installation, and the token is typed while that sentence is on screen —
  // which is the whole of the confirm-first posture. Opening the dialog and
  // filling it in must send nothing.
  it("does not connect until the confirmation is accepted", async () => {
    const user = userEvent.setup();
    const calls = stubApi({
      "GET /me": meRoute(OVERLAY_OPERATOR),
      "GET /overlay/connection": () =>
        jsonResponse({ detail: "not found" }, 404),
      "POST /overlay/connection": () => jsonResponse(activeConnection, 201),
    });
    render(<OverlayCard />);
    const token = await openConnectDialog(user, "Connect HubSpot");
    expect(
      screen.getByText(/switches every seat's reads to HubSpot/),
    ).toBeTruthy();
    await user.type(token, "pat-secret");
    expect(
      calls.filter(
        (r) => r.url.endsWith("/overlay/connection") && r.method === "POST",
      ),
    ).toHaveLength(0);
    await user.click(lastButton("Connect HubSpot"));
    await waitFor(() =>
      expect(
        calls.filter(
          (r) => r.url.endsWith("/overlay/connection") && r.method === "POST",
        ),
      ).toHaveLength(1),
    );
  });

  // An empty token cannot be submitted: the confirm is refused until the field
  // the write needs is filled, so a press on an empty dialog is not a POST of
  // an empty secret.
  it("refuses the confirm until a token has been typed", async () => {
    const user = userEvent.setup();
    stubApi({
      "GET /me": meRoute(OVERLAY_OPERATOR),
      "GET /overlay/connection": () =>
        jsonResponse({ detail: "not found" }, 404),
    });
    render(<OverlayCard />);
    const token = await openConnectDialog(user, "Connect HubSpot");
    expect(lastButton("Connect HubSpot").hasAttribute("disabled")).toBe(true);
    await user.type(token, "pat-secret");
    expect(lastButton("Connect HubSpot").hasAttribute("disabled")).toBe(false);
  });

  it("offers Reconnect for a revoked connection, gated by the same confirm step", async () => {
    const user = userEvent.setup();
    const calls = stubApi({
      "GET /me": meRoute(OVERLAY_OPERATOR),
      "GET /overlay/connection": () => jsonResponse(revokedConnection),
      "POST /overlay/connection": () => jsonResponse(activeConnection, 201),
    });
    render(<OverlayCard />);
    expect(await screen.findByText("Revoked")).toBeTruthy();
    const token = await openConnectDialog(user, "Reconnect");
    await user.type(token, "pat-secret");
    expect(
      calls.filter(
        (r) => r.url.endsWith("/overlay/connection") && r.method === "POST",
      ),
    ).toHaveLength(0);
    await user.click(lastButton("Reconnect"));
    await waitFor(() =>
      expect(
        calls.filter(
          (r) => r.url.endsWith("/overlay/connection") && r.method === "POST",
        ),
      ).toHaveLength(1),
    );
  });

  it("invalidates every query after a successful connect", async () => {
    const user = userEvent.setup();
    stubApi({
      "GET /me": meRoute(OVERLAY_OPERATOR),
      "GET /overlay/connection": () =>
        jsonResponse({ detail: "not found" }, 404),
      "POST /overlay/connection": () => jsonResponse(activeConnection, 201),
    });
    const { client } = render(<OverlayCard />);
    const token = await openConnectDialog(user, "Connect HubSpot");
    await user.type(token, "pat-secret");
    const invalidateSpy = vi.spyOn(client, "invalidateQueries");
    await user.click(lastButton("Connect HubSpot"));
    await waitFor(() => expect(invalidateSpy).toHaveBeenCalled());
    // Called with no arguments — the whole cache, not one targeted key —
    // because the workspace's data source itself just changed (/me included).
    expect(invalidateSpy).toHaveBeenCalledWith();
  });

  it("surfaces a concurrent already-connected conflict instead of guessing", async () => {
    const user = userEvent.setup();
    stubApi({
      "GET /me": meRoute(OVERLAY_OPERATOR),
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
    const token = await openConnectDialog(user, "Connect HubSpot");
    await user.type(token, "pat-secret");
    await user.click(lastButton("Connect HubSpot"));
    expect(
      await screen.findByText(/an active incumbent connection already exists/),
    ).toBeTruthy();
  });

  // Reconcile and disconnect are update and delete — independent grants over
  // very different amounts of damage. Without one-at-a-time cases a swap ships
  // unnoticed, because the all-or-none fixtures pass either way.
  it("offers reconcile but not disconnect on the update grant alone", async () => {
    stubApi({
      "GET /me": meRoute({ overlay_connection: ["read", "update"] }),
      "GET /overlay/connection": () => jsonResponse(activeConnection),
      "GET /overlay/sync-status": () => jsonResponse(syncStatusFixture),
      "GET /overlay/budget": () => jsonResponse(budgetFixture),
    });
    render(<OverlayCard />);
    expect(
      await screen.findByRole("button", { name: "Sync now" }),
    ).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Disconnect" })).toBeNull();
  });

  it("offers disconnect but not reconcile on the delete grant alone", async () => {
    stubApi({
      "GET /me": meRoute({ overlay_connection: ["read", "delete"] }),
      "GET /overlay/connection": () => jsonResponse(activeConnection),
      "GET /overlay/sync-status": () => jsonResponse(syncStatusFixture),
      "GET /overlay/budget": () => jsonResponse(budgetFixture),
    });
    render(<OverlayCard />);
    expect(
      await screen.findByRole("button", { name: "Disconnect" }),
    ).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Sync now" })).toBeNull();
  });

  it("does not offer reconcile/disconnect without either grant on a live connection", async () => {
    stubApi({
      "GET /me": meRoute({}),
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
      "GET /me": meRoute(OVERLAY_OPERATOR),
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
      "GET /me": meRoute(OVERLAY_OPERATOR),
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
