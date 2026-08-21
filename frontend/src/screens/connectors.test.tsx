/** @vitest-environment jsdom */
import "@testing-library/jest-dom/vitest";

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  cleanup,
  render as rtlRender,
  screen,
  waitFor,
  within,
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
  scopes: [
    "https://www.googleapis.com/auth/gmail.readonly",
    "https://www.googleapis.com/auth/gmail.send",
  ],
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

// A mailbox connected before Margince asked for the send scope: healthy,
// capturing, and permanently unable to send until it is reconnected — Google
// will not widen an existing refresh token.
const gmailNoSendGrant: CaptureConnection = {
  ...gmailConnected,
  scopes: ["https://www.googleapis.com/auth/gmail.readonly"],
};

const imapConnected: CaptureConnection = {
  ...gmailConnected,
  id: "018f3a1b-0000-7000-8000-0000000000c9",
  provider: "imap",
  scopes: [],
};

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

type ChannelConnection = components["schemas"]["ChannelConnection"];

type StubOpts = {
  /** Fail the /connectors GET with this status (load-error path). */
  listStatus?: number;
  /** The connect (reconnect) POST response, or an error status. */
  connect?: { authorize_url?: string } | { status: number };
  /** The messaging-channel roster the Telegram panel reads. */
  channels?: ChannelConnection[];
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
      // Every ConnectorsCard mount now also reads the Telegram channel
      // panel's own list — these tests exercise the mail-connector rows
      // only, so an empty roster is the honest default rather than a
      // fixture every one of them would otherwise have to repeat.
      if (path.endsWith("/channel-connections") && request.method === "GET") {
        return jsonResponse({ data: opts.channels ?? [] });
      }
      if (path.endsWith("/connect") && request.method === "POST") {
        const c = opts.connect ?? {
          authorize_url: "https://accounts.google/x",
        };
        if ("status" in c) {
          const body =
            c.status === 501
              ? { code: "not_implemented", detail: "capture not wired" }
              : { detail: "connect failed" };
          return jsonResponse(body, c.status);
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

// Adding a connection is the card's create verb, so it lives in the panel
// header and its provider picks live in the dialog it opens — a strip of four
// buttons in a row's right column was the shape it replaced. Every claim about
// a pick therefore opens the dialog first and scopes its queries to it.
async function openAddDialog() {
  await userEvent.click(
    await screen.findByRole("button", { name: "Connect an account" }),
  );
  return screen.getByRole("dialog", { name: "Add a connection" });
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

  // Mail capture and the workspace's messaging bot are two subjects, so they
  // are two panels. The Telegram half used to be a level-3 heading buried under
  // the mail roster, which put a workspace-wide bot inside a per-user card.
  it("draws mail capture and the Telegram bot as two separate panels", async () => {
    stubApi([gmailConnected]);
    render(<ConnectorsCard />);
    const inboxes = await screen.findByRole("heading", {
      name: "Connected inboxes",
    });
    const telegram = screen.getByRole("heading", { name: "Telegram bot" });
    // Two panel headings means two panels: neither heading may sit inside the
    // other's section, which is what a nested SectionHeader did.
    expect(inboxes.closest(".panel")).not.toBe(telegram.closest(".panel"));
  });

  // The history import belongs to the mailbox it imports for. Mounted as a
  // sibling of that row rather than inside a Disclosure: <details> renders its
  // children while closed, and BackfillPanel fires a scope-preview POST from an
  // effect, so a collapsed one would spend requests nobody asked for.
  it("attaches the history import to the connected mailbox's own row", async () => {
    stubApi([gmailConnected]);
    render(<ConnectorsCard />);
    const row = await screen.findByTestId("connector-gmail");
    const backfill = document.querySelector(".connector-backfill");
    expect(backfill).not.toBeNull();
    expect(row.nextElementSibling).toBe(backfill);
  });

  // An empty roster is the ANSWER to the question this card asks, so it is a
  // row of the card's own list rather than a bare paragraph floating between
  // the description and whatever came next.
  it("states an empty roster as a row of the list, with the connect verb in the header", async () => {
    stubApi([]);
    render(<ConnectorsCard />);
    const row = await screen.findByTestId("connector-roster-empty");
    expect(within(row).getByText(/No inbox is connected yet/)).toBeTruthy();
    expect(row.closest(".settinglist")).not.toBeNull();
    expect(
      screen.getByRole("button", { name: "Connect an account" }),
    ).toBeTruthy();
  });

  it("offers every provider from the dialog when nothing is connected", async () => {
    stubApi([]);
    render(<ConnectorsCard />);
    const dialog = await openAddDialog();
    for (const provider of [
      "Gmail",
      "Google Calendar",
      "Microsoft",
      "IMAP mailbox",
    ]) {
      expect(
        within(dialog).getByRole("button", { name: `Connect ${provider}` }),
      ).toBeTruthy();
    }
  });

  it("opens the inline IMAP form from the dialog instead of bouncing to onboarding", async () => {
    stubApi([]);
    render(<ConnectorsCard />);
    const dialog = await openAddDialog();
    await userEvent.click(
      within(dialog).getByRole("button", { name: "Connect IMAP mailbox" }),
    );
    // The chooser gives way rather than stacking behind the form: two overlays
    // deep, Escape and the focus restore both answer to the wrong layer.
    expect(
      await screen.findByRole("dialog", { name: "Connect an IMAP mailbox" }),
    ).toBeTruthy();
    expect(
      screen.queryByRole("dialog", { name: "Add a connection" }),
    ).toBeNull();
  });

  it("offers reconnect only for a connection that needs re-auth", async () => {
    stubApi([gmailStale]);
    render(<ConnectorsCard />);
    expect(await screen.findByText("Needs reconnect")).toBeTruthy();
    expect(screen.getByRole("button", { name: /Reconnect/ })).toBeTruthy();
  });

  it("says a Gmail mailbox cannot send before the rep discovers it at send time", async () => {
    stubApi([gmailNoSendGrant]);
    render(<ConnectorsCard />);
    expect(
      await screen.findByText("Capturing only — cannot send"),
    ).toBeTruthy();
    expect(
      screen.getByText(/Reconnect this mailbox to send from it/),
    ).toBeTruthy();
    // The prompt is only actionable if reconnecting is one click from here.
    expect(screen.getByRole("button", { name: /Reconnect/ })).toBeTruthy();
  });

  it("stays quiet about sending once the send scope is granted", async () => {
    stubApi([gmailConnected]);
    render(<ConnectorsCard />);
    expect(await screen.findByText("Capturing")).toBeTruthy();
    expect(screen.queryByText("Capturing only — cannot send")).toBeNull();
    expect(screen.queryByRole("button", { name: /Reconnect/ })).toBeNull();
  });

  it("never claims a non-Gmail mailbox cannot send — its scopes are not Google's", async () => {
    stubApi([imapConnected]);
    render(<ConnectorsCard />);
    expect(await screen.findByText("IMAP mailbox")).toBeTruthy();
    expect(screen.queryByText("Capturing only — cannot send")).toBeNull();
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
    expect(body).toEqual({ return_to: "settings" });
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
    // Announced, and attached to the button that produced it: the reason
    // renders inside the same roster row as the Reconnect that was pressed,
    // never in a band of its own under an unrelated heading.
    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toMatch(/connect failed/);
    const row = screen
      .getByRole("button", { name: /Reconnect/ })
      .closest(".settingrow");
    expect(row).not.toBeNull();
    expect(row?.contains(alert)).toBe(true);
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
// #/settings/connections/{outcome} — the route parses to
// {screen:"settings", id:"connections", id2:<outcome>} and the card renders
// a dismissible inline note from that segment, never a claim the server
// hasn't confirmed.
describe("the OAuth return outcome", () => {
  it("renders an honest denial note when the user declined access", async () => {
    globalThis.location.hash = "#/settings/connections/denied";
    installFetchStub({
      "GET /connectors": () => jsonResponse({ data: [] }),
    });
    render(<ConnectorsCard />);
    expect(await screen.findByText(/you declined access/i)).toBeTruthy();
    expect(screen.queryByText(/couldn't be completed/i)).toBeNull();
  });

  it("renders an honest failure note when the connection could not complete", async () => {
    globalThis.location.hash = "#/settings/connections/error";
    installFetchStub({
      "GET /connectors": () => jsonResponse({ data: [] }),
    });
    render(<ConnectorsCard />);
    expect(await screen.findByText(/couldn't be completed/i)).toBeTruthy();
    expect(screen.queryByText(/you declined access/i)).toBeNull();
  });

  // A permanent failure must not tell the reader to try again: the provider's
  // API is not enabled for this deployment, and only an administrator can
  // change that.
  it("names the remedy when the provider's API is not enabled here", async () => {
    globalThis.location.hash = "#/settings/connections/misconfigured";
    installFetchStub({
      "GET /connectors": () => jsonResponse({ data: [] }),
    });
    render(<ConnectorsCard />);
    expect(
      await screen.findByText(/administrator needs to enable it/i),
    ).toBeTruthy();
    expect(screen.queryByText(/couldn't be completed/i)).toBeNull();
  });

  it("tells the reader to accept every permission when the provider declined", async () => {
    globalThis.location.hash = "#/settings/connections/rejected";
    installFetchStub({
      "GET /connectors": () => jsonResponse({ data: [] }),
    });
    render(<ConnectorsCard />);
    expect(await screen.findByText(/accept every permission/i)).toBeTruthy();
    // The generic "couldn't be completed — please try again" must not also show.
    expect(screen.queryByText(/couldn't be completed/i)).toBeNull();
  });

  it("renders a brief success note on ok — never an error", async () => {
    globalThis.location.hash = "#/settings/connections/ok";
    installFetchStub({
      "GET /connectors": () => jsonResponse({ data: [gmailConnected] }),
    });
    render(<ConnectorsCard />);
    expect(await screen.findByText(/mailbox is now capturing/i)).toBeTruthy();
    expect(screen.queryByText(/couldn't be completed/i)).toBeNull();
    expect(screen.queryByText(/you declined access/i)).toBeNull();
  });

  it("renders no outcome note when the route carries none", async () => {
    globalThis.location.hash = "#/settings/connections";
    installFetchStub({
      "GET /connectors": () => jsonResponse({ data: [] }),
    });
    render(<ConnectorsCard />);
    await screen.findByTestId("connector-roster-empty");
    expect(screen.queryByRole("status")).toBeNull();
  });

  it("dismisses the note and clears it", async () => {
    globalThis.location.hash = "#/settings/connections/denied";
    installFetchStub({
      "GET /connectors": () => jsonResponse({ data: [] }),
    });
    render(<ConnectorsCard />);
    await screen.findByText(/you declined access/i);
    await userEvent.click(screen.getByRole("button", { name: /dismiss/i }));
    expect(screen.queryByText(/you declined access/i)).toBeNull();
  });
});

// The "Add a connection" affordance (Task 1): one verb in the card's header
// opens a dialog listing the providers still addable, each with the sentence
// its choice needs. An OAuth pick connects+redirects, IMAP hands over to the
// inline form, and a 501 from a specific provider's connect renders an honest
// named note — in the dialog the press happened in.
describe("add a connection", () => {
  it("offers only not-yet-connected providers when one is connected", async () => {
    stubApi([gmailConnected]);
    render(<ConnectorsCard />);
    const dialog = await openAddDialog();
    expect(
      within(dialog).queryByRole("button", { name: "Connect Gmail" }),
    ).toBeNull();
    expect(
      within(dialog).getByRole("button", { name: "Connect Google Calendar" }),
    ).toBeTruthy();
    expect(
      within(dialog).getByRole("button", { name: "Connect Microsoft" }),
    ).toBeTruthy();
    expect(
      within(dialog).getByRole("button", { name: "Connect IMAP mailbox" }),
    ).toBeTruthy();
  });

  it("redirects the browser when an OAuth provider is chosen", async () => {
    const assign = vi.fn();
    vi.stubGlobal("location", { ...globalThis.location, assign });
    stubApi([gmailConnected], {
      connect: { authorize_url: "https://accounts.google/cal" },
    });
    render(<ConnectorsCard />);
    const dialog = await openAddDialog();
    await userEvent.click(
      within(dialog).getByRole("button", { name: "Connect Google Calendar" }),
    );
    await waitFor(() =>
      expect(assign).toHaveBeenCalledWith("https://accounts.google/cal"),
    );
  });

  it("shows an honest note when a provider is not configured (501)", async () => {
    stubApi([gmailConnected], { connect: { status: 501 } });
    render(<ConnectorsCard />);
    const dialog = await openAddDialog();
    await userEvent.click(
      within(dialog).getByRole("button", { name: "Connect Microsoft" }),
    );
    expect(
      await within(dialog).findByText(
        "Microsoft isn't configured in this deployment.",
      ),
    ).toBeTruthy();
  });

  it("reports a refused connect inside the dialog whose button produced it", async () => {
    stubApi([gmailConnected], { connect: { status: 502 } });
    render(<ConnectorsCard />);
    const dialog = await openAddDialog();
    await userEvent.click(
      within(dialog).getByRole("button", { name: "Connect Microsoft" }),
    );

    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toMatch(/connect failed/);
    // In the dialog the press happened in — not in a band of its own on the
    // card behind it, where nothing says which press it answers.
    expect(dialog.contains(alert)).toBe(true);
  });

  // Four provider names decide nothing: Gmail and Google Calendar are two
  // halves of one account, only Gmail can ever send, and IMAP is the answer for
  // every host with no OAuth. Each pick carries the sentence that says so, and
  // it lands in that button's own aria-describedby.
  it("gives every pick the sentence its choice needs", async () => {
    stubApi([]);
    render(<ConnectorsCard />);
    await openAddDialog();
    const gcal = await screen.findByTestId("connector-add-gcal");
    expect(within(gcal).getByText(/separately from Gmail/i)).toBeTruthy();
    const imap = screen.getByTestId("connector-add-imap");
    expect(within(imap).getByText(/app password/i)).toBeTruthy();
    const describedBy = within(imap)
      .getByRole("button", { name: "Connect IMAP mailbox" })
      .getAttribute("aria-describedby");
    expect(describedBy).not.toBeNull();
  });

  it("withdraws the header verb when all four providers are connected", async () => {
    stubApi([
      gmailConnected,
      { ...gmailConnected, id: "c2", provider: "gcal" },
      { ...gmailConnected, id: "c3", provider: "graph" },
      { ...gmailConnected, id: "c4", provider: "imap" },
    ]);
    render(<ConnectorsCard />);
    await screen.findByText("Google Calendar"); // a roster row label
    expect(
      screen.queryByRole("button", { name: "Connect an account" }),
    ).toBeNull();
  });
});

// A workspace may hold more than one live bot — nothing in the API forbids
// connecting a second one — and a send refuses outright while it does. This
// panel is the only surface that can take one of them away again, so it has
// to show every one it is handed.
describe("the Telegram connector panel", () => {
  const salesBot: ChannelConnection = {
    id: "018f3a1b-0000-7000-8000-0000000000d1",
    provider: "telegram",
    channelId: "555000111",
    channelLabel: "acme_sales_bot",
    status: "connected",
    version: 1,
  };
  const supportBot: ChannelConnection = {
    ...salesBot,
    id: "018f3a1b-0000-7000-8000-0000000000d2",
    channelId: "555000222",
    channelLabel: "acme_support_bot",
    status: "pending",
  };

  it("lists every connected bot, each with its own Disconnect", async () => {
    stubApi([], { channels: [salesBot, supportBot] });
    render(<ConnectorsCard />);

    // One SettingRow per bot: the roster is a list of decisions now, so a bot
    // is identified by its own row rather than by an <li> the list wrapper
    // used to supply.
    const rows = await screen.findAllByTestId("telegram-connection");
    expect(rows.length).toBe(2);
    expect(within(rows[0]).getByText("@acme_sales_bot")).toBeTruthy();
    expect(within(rows[1]).getByText("@acme_support_bot")).toBeTruthy();
    // One shared Disconnect for two bots could only ever remove one of them,
    // which is exactly the state an admin is here to escape.
    for (const row of rows) {
      expect(
        within(row).getByRole("button", { name: "Disconnect" }),
      ).toBeTruthy();
    }
  });

  it("opens the replace-token form on the bot whose row was clicked", async () => {
    stubApi([], { channels: [salesBot, supportBot] });
    render(<ConnectorsCard />);

    const rows = await screen.findAllByTestId("telegram-connection");
    await userEvent.click(
      within(rows[1]).getByRole("button", { name: "Replace token" }),
    );
    // The second bot is the pending one: the form showing its status is the
    // observable proof it bound to that row and not to the first.
    const dialog = screen.getByRole("dialog");
    expect(within(dialog).getByText(/^Pending/)).toBeTruthy();
  });
});
