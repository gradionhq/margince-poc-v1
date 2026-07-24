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
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { components } from "../api/schema";
import { LocaleProvider } from "../i18n";
import { ConnectorsCard } from "./connectors";
import { installFetchStub } from "./story-utils";

// The connected-inboxes card makes the onboarding promise ("disconnect in one
// click", "manage in Settings") real. It renders server facts only, and a
// disconnect is confirmed-first before it stops capture.

type CaptureConnection = components["schemas"]["CaptureConnection"];

const gmailConnected: CaptureConnection = {
  id: "018f3a1b-0000-7000-8000-0000000000c1",
  provider: "gmail",
  status: "connected",
  scopes: ["read"],
  last_synced_at: "2026-07-23T09:30:00Z",
  // A finished backfill: mounting BackfillPanel below the row must not fire
  // an extra request (the panel seeds from this embedded snapshot). "none"
  // would auto-fire the setup screen's scope preview against an unstubbed
  // route — "done" is the honest, inert terminal state for an established
  // connection these fixtures otherwise don't care about.
  backfill: { state: "done" },
};

const gmailStale: CaptureConnection = {
  ...gmailConnected,
  status: "reauth_required",
};

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

type StubOpts = {
  /** Fail the /connectors GET with this status (load-error path). */
  listStatus?: number;
  /** The connect (reconnect) POST response, or an error status. */
  connect?: { authorize_url?: string } | { status: number };
};

function stubApi(connections: CaptureConnection[], opts: StubOpts = {}) {
  const calls: Request[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (request: Request) => {
      calls.push(request);
      const path = new URL(request.url).pathname;
      if (path.endsWith("/connectors") && request.method === "GET") {
        if (opts.listStatus) {
          return jsonResponse({ detail: "boom" }, opts.listStatus);
        }
        return jsonResponse({ data: connections });
      }
      if (path.endsWith("/connect") && request.method === "POST") {
        const c = opts.connect ?? {
          authorize_url: "https://accounts.google/x",
        };
        if ("status" in c) {
          return jsonResponse({ detail: "connect failed" }, c.status);
        }
        return jsonResponse(c);
      }
      if (path.endsWith("/disconnect") && request.method === "POST") {
        return new Response(null, { status: 204 });
      }
      throw new Error(`unstubbed: ${request.method} ${path}`);
    }),
  );
  return calls;
}

function render(ui: ReactNode) {
  return rtlRender(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <LocaleProvider initial="en">{ui}</LocaleProvider>
    </QueryClientProvider>,
  );
}

function requestsTo(calls: Request[], suffix: string, method: string) {
  return calls.filter(
    (r) => new URL(r.url).pathname.endsWith(suffix) && r.method === method,
  );
}

beforeEach(() => {
  vi.stubGlobal("scrollTo", vi.fn());
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  globalThis.location.hash = "";
});

describe("the connected-inboxes card", () => {
  it("lists a live connection with its status and last-synced time", async () => {
    stubApi([gmailConnected]);
    render(<ConnectorsCard />);
    expect(await screen.findByText("Gmail")).toBeTruthy();
    expect(screen.getByText("Capturing")).toBeTruthy();
    expect(screen.getByText(/Last synced/)).toBeTruthy();
  });

  it("shows an empty state with a connect CTA when nothing is connected", async () => {
    stubApi([]);
    render(<ConnectorsCard />);
    expect(await screen.findByText(/No inbox is connected yet/)).toBeTruthy();
    expect(
      screen.getByRole("button", { name: /Connect an inbox/ }),
    ).toBeTruthy();
  });

  it("opens the inline IMAP form from the empty state instead of bouncing to onboarding", async () => {
    stubApi([]);
    render(<ConnectorsCard />);
    await userEvent.click(
      await screen.findByRole("button", { name: /Connect an IMAP mailbox/ }),
    );
    expect(
      screen.getByRole("dialog", { name: "Connect an IMAP mailbox" }),
    ).toBeTruthy();
  });

  it("offers reconnect only for a connection that needs re-auth", async () => {
    stubApi([gmailStale]);
    render(<ConnectorsCard />);
    expect(await screen.findByText("Needs reconnect")).toBeTruthy();
    expect(screen.getByRole("button", { name: /Reconnect/ })).toBeTruthy();
  });

  it("shows an honest waiting line for a connection that has never synced", async () => {
    stubApi([{ ...gmailConnected, last_synced_at: null }]);
    render(<ConnectorsCard />);
    expect(await screen.findByText(/Waiting for the first sync/)).toBeTruthy();
  });

  it("surfaces a load failure without crashing the card", async () => {
    stubApi([], { listStatus: 500 });
    render(<ConnectorsCard />);
    expect(await screen.findByText(/Couldn't load|boom/)).toBeTruthy();
  });

  it("reconnect re-mints the consent URL and redirects", async () => {
    const assign = vi.fn();
    vi.stubGlobal("location", { ...globalThis.location, assign });
    const calls = stubApi([gmailStale], {
      connect: { authorize_url: "https://accounts.google/consent" },
    });
    render(<ConnectorsCard />);
    await userEvent.click(
      await screen.findByRole("button", { name: /Reconnect/ }),
    );
    await waitFor(() =>
      expect(requestsTo(calls, "/connect", "POST").length).toBe(1),
    );
    await waitFor(() =>
      expect(assign).toHaveBeenCalledWith("https://accounts.google/consent"),
    );
  });

  it("sends return_to=settings on reconnect so consent lands back on Settings", async () => {
    vi.stubGlobal("location", { ...globalThis.location, assign: vi.fn() });
    const calls = stubApi([gmailStale], {
      connect: { authorize_url: "https://accounts.google/consent" },
    });
    render(<ConnectorsCard />);
    await userEvent.click(
      await screen.findByRole("button", { name: /Reconnect/ }),
    );
    const connectRequests = await waitFor(() => {
      const requests = requestsTo(calls, "/connect", "POST");
      expect(requests.length).toBe(1);
      return requests;
    });
    const body = await connectRequests[0].clone().json();
    expect(body).toMatchObject({ return_to: "settings" });
  });

  it("offers the inline IMAP form to reconnect an imap connection instead of an OAuth reconnect", async () => {
    stubApi([{ ...gmailStale, provider: "imap" }]);
    render(<ConnectorsCard />);
    await userEvent.click(
      await screen.findByRole("button", { name: /Reconnect/ }),
    );
    expect(await screen.findByText("Connect an IMAP mailbox")).toBeTruthy();
  });

  it("surfaces a failed reconnect instead of redirecting", async () => {
    const calls = stubApi([gmailStale], { connect: { status: 502 } });
    render(<ConnectorsCard />);
    await userEvent.click(
      await screen.findByRole("button", { name: /Reconnect/ }),
    );
    await waitFor(() =>
      expect(requestsTo(calls, "/connect", "POST").length).toBe(1),
    );
    expect(await screen.findByText(/connect failed/)).toBeTruthy();
  });

  it("disconnects only after an explicit confirm", async () => {
    const calls = stubApi([gmailConnected]);
    render(<ConnectorsCard />);
    await screen.findByText("Gmail");

    // Opening the row's disconnect shows a confirm — nothing is called yet.
    await userEvent.click(screen.getByRole("button", { name: /^Disconnect$/ }));
    expect(requestsTo(calls, "/disconnect", "POST").length).toBe(0);

    // The modal's confirm is the one that stops capture.
    const confirms = screen.getAllByRole("button", { name: /^Disconnect$/ });
    await userEvent.click(confirms[confirms.length - 1]);
    await waitFor(() =>
      expect(requestsTo(calls, "/disconnect", "POST").length).toBe(1),
    );
  });
});

// The richer per-row health line (account_label, next_sync_due_at,
// watch_expires_at, the error-class sentence) and the 501 calm state, all
// exercised through the real installFetchStub route-map shape.
describe("the connected-inboxes card's richer health line", () => {
  it("shows the account label beside the provider name", async () => {
    installFetchStub({
      "GET /connectors": () =>
        jsonResponse({
          data: [{ ...gmailConnected, account_label: "lars@example.de" }],
        }),
    });
    render(<ConnectorsCard />);
    expect(await screen.findByText("lars@example.de")).toBeTruthy();
  });

  it("reads a null watch_expires_at as polled, never as expired", async () => {
    installFetchStub({
      "GET /connectors": () =>
        jsonResponse({
          data: [
            { ...gmailConnected, provider: "imap", watch_expires_at: null },
          ],
        }),
    });
    render(<ConnectorsCard />);
    expect(await screen.findByText(/polled/i)).toBeTruthy();
    expect(screen.queryByText(/expired/i)).toBeNull();
  });

  it("renders a push renewal deadline when watch_expires_at is set", async () => {
    installFetchStub({
      "GET /connectors": () =>
        jsonResponse({
          data: [
            { ...gmailConnected, watch_expires_at: "2026-08-01T00:00:00Z" },
          ],
        }),
    });
    render(<ConnectorsCard />);
    expect(await screen.findByText(/push renewal/i)).toBeTruthy();
  });

  it("renders the error-class sentence for a reauth_required connection", async () => {
    installFetchStub({
      "GET /connectors": () =>
        jsonResponse({
          data: [{ ...gmailStale, last_sync_error_class: "auth" }],
        }),
    });
    render(<ConnectorsCard />);
    expect(await screen.findByText(/rejected our credentials/i)).toBeTruthy();
  });

  it("renders the 501 not-configured response as a calm state, not an error", async () => {
    installFetchStub({
      "GET /connectors": () => jsonResponse({ code: "not_implemented" }, 501),
    });
    render(<ConnectorsCard />);
    expect(
      await screen.findByText(/isn't configured in this deployment/i),
    ).toBeTruthy();
    expect(screen.queryByRole("alert")).toBeNull();
    expect(screen.queryByText(/couldn't load/i)).toBeNull();
  });

  it("shows the updated disconnect copy naming credential deletion and Google's own access list", async () => {
    installFetchStub({
      "GET /connectors": () => jsonResponse({ data: [gmailConnected] }),
    });
    render(<ConnectorsCard />);
    await userEvent.click(
      await screen.findByRole("button", { name: /^Disconnect$/ }),
    );
    expect(
      await screen.findByText(/delete the credential we stored/i),
    ).toBeTruthy();
    expect(screen.getByText(/Google may still list Margince/i)).toBeTruthy();
  });

  it("omits the vendor-access note for an IMAP disconnect (no upstream grant)", async () => {
    installFetchStub({
      "GET /connectors": () =>
        jsonResponse({ data: [{ ...gmailConnected, provider: "imap" }] }),
    });
    render(<ConnectorsCard />);
    await userEvent.click(
      await screen.findByRole("button", { name: /^Disconnect$/ }),
    );
    expect(
      await screen.findByText(/delete the credential we stored/i),
    ).toBeTruthy();
    expect(screen.queryByText(/Google may still list Margince/i)).toBeNull();
  });
});

// The OAuth return outcome (Task 2): the backend now lands the callback on
// #/settings/integrations/{outcome} — the route parses to
// {screen:"settings", id:"integrations", id2:<outcome>} and the card renders
// a dismissible inline note from that segment, never a claim the server
// hasn't confirmed.
describe("the OAuth return outcome", () => {
  it("renders an honest denial note when the user declined access", async () => {
    globalThis.location.hash = "#/settings/integrations/denied";
    installFetchStub({
      "GET /connectors": () => jsonResponse({ data: [] }),
    });
    render(<ConnectorsCard />);
    expect(await screen.findByText(/you declined access/i)).toBeTruthy();
    expect(screen.queryByText(/couldn't be completed/i)).toBeNull();
  });

  it("renders an honest failure note when the connection could not complete", async () => {
    globalThis.location.hash = "#/settings/integrations/error";
    installFetchStub({
      "GET /connectors": () => jsonResponse({ data: [] }),
    });
    render(<ConnectorsCard />);
    expect(await screen.findByText(/couldn't be completed/i)).toBeTruthy();
    expect(screen.queryByText(/you declined access/i)).toBeNull();
  });

  it("renders a brief success note on ok — never an error", async () => {
    globalThis.location.hash = "#/settings/integrations/ok";
    installFetchStub({
      "GET /connectors": () => jsonResponse({ data: [gmailConnected] }),
    });
    render(<ConnectorsCard />);
    expect(await screen.findByText(/mailbox is now capturing/i)).toBeTruthy();
    expect(screen.queryByText(/couldn't be completed/i)).toBeNull();
    expect(screen.queryByText(/you declined access/i)).toBeNull();
  });

  it("renders no outcome note when the route carries none", async () => {
    globalThis.location.hash = "#/settings/integrations";
    installFetchStub({
      "GET /connectors": () => jsonResponse({ data: [] }),
    });
    render(<ConnectorsCard />);
    await screen.findByText(/No inbox is connected yet/);
    expect(screen.queryByRole("status")).toBeNull();
  });

  it("dismisses the note and clears it", async () => {
    globalThis.location.hash = "#/settings/integrations/denied";
    installFetchStub({
      "GET /connectors": () => jsonResponse({ data: [] }),
    });
    render(<ConnectorsCard />);
    await screen.findByText(/you declined access/i);
    await userEvent.click(screen.getByRole("button", { name: /dismiss/i }));
    expect(screen.queryByText(/you declined access/i)).toBeNull();
  });
});
