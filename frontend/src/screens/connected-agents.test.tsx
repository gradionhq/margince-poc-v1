/** @vitest-environment jsdom */
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
import { LocaleProvider } from "../i18n";
import { ConnectedAgentsCard } from "./connected-agents";
import { SettingsScreen, settingsAddress } from "./settings";

// The split GET /passports feeds: a passport the human minted belongs to the
// passports card, a connection's credential to this one. `connection` decides,
// which is what the label fixtures below exist to prove — a minted passport
// NAMED like a connection stays a passport, and a real connection is shown
// under its client's registered name rather than the oauth: label it carries.

beforeEach(() => {
  globalThis.localStorage.setItem("margince.workspaceSlug", "acme");
  // The clock is pinned for the WHOLE file, because these fixtures carry
  // absolute expiry dates and the card's state is derived by comparing them to
  // now. Left on the real clock, every row here reads as live until the day
  // CONNECTION.expires_at arrives and lapsed from then on — so the suite passed
  // for a month and then began failing by the calendar, naming code nobody had
  // touched. Two cases already stubbed their own time; pinning it here is the
  // invariant rather than three more copies of the same guard.
  vi.setSystemTime(new Date("2026-08-03T09:00:00Z"));
});

afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  globalThis.localStorage.clear();
});

// The zone the viewer's browser reports, as `viewerZone()` asks for it.
// Restored by the afterEach above, so a case pretending to sit elsewhere never
// decides what the next one renders.
function pretendViewerZone(timeZone: string): void {
  const real = Intl.DateTimeFormat().resolvedOptions();
  vi.spyOn(Intl.DateTimeFormat.prototype, "resolvedOptions").mockReturnValue({
    ...real,
    timeZone,
  });
}

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

// A DCR client id is opaque, high-entropy and machine-issued — which is the
// whole reason it must not be what a human sees. Spelled once, obviously
// synthetic, and NOT copied from a real installation: a plausible-looking one
// reads as a credential to every secret scanner that meets it, and it would be
// someone's actual identifier sitting in a fixture.
const DCR_CLIENT_ID = "dcr-client-id-0000000000000000000000000000";

const MINTED = {
  id: "pp-minted",
  // Deliberately spelled like a connection's stored label: the card must split
  // on the `connection` field, never on this prefix.
  label: "oauth:not-a-connection",
  scopes: ["read", "draft"],
  created_at: "2026-07-01T08:00:00Z",
  expires_at: "2026-08-01T08:00:00Z",
  revoked_at: null,
  connection: null,
};

const CONNECTED = {
  id: "pp-connection",
  label: `oauth:${DCR_CLIENT_ID}`,
  scopes: ["read", "draft", "write"],
  created_at: "2026-07-20T08:00:00Z",
  // Far future on purpose: LIVE is a comparison against now, and the tests
  // that render this fixture do not pin the clock — an expiry near the
  // authoring date turns into a time bomb the morning it passes (it did).
  expires_at: "2036-08-20T08:00:00Z",
  revoked_at: null,
  connection: {
    client_id: DCR_CLIENT_ID,
    client_name: "Claude Code",
    connected_at: "2026-07-02T09:00:00Z",
    renewable: false,
    lent_passport_id: "pp-minted",
    lent_passport_label: "full test",
  },
};

// The same connection after its credential simply ran out. Its grant carries no
// offline_access (renewable: false), so it cannot mint a replacement — this is
// how a connection ends WITHOUT anything writing revoked_at, the state that
// used to render as live.
const LAPSED = {
  ...CONNECTED,
  id: "pp-lapsed",
  expires_at: "2026-07-30T08:00:00Z",
};

// Past its expiry too, but its grant CAN renew: the client repairs this on its
// next call. Reading the expiry alone would bury a perfectly live connector.
const RENEWING = {
  ...CONNECTED,
  id: "pp-renewing",
  expires_at: "2026-07-30T08:00:00Z",
  connection: { ...CONNECTED.connection, renewable: true },
};

// One backend for both cards, since they share the ["passports"] read.
// `connectorEnabled: false` answers discovery with the 404 an installation
// serving no /mcp routes produces.
function backend(opts: {
  passports?: unknown[];
  connectorEnabled?: boolean;
  onDelete?: (id: string) => void;
}) {
  const passports = [...(opts.passports ?? [MINTED, CONNECTED])];
  return vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input instanceof Request ? input.url : input);
    // openapi-fetch hands the whole call over as a Request; the plain fetch
    // the connect guide makes passes a string and an init instead.
    const method =
      input instanceof Request ? input.method : (init?.method ?? "GET");
    if (url.includes("/.well-known/oauth-protected-resource")) {
      return opts.connectorEnabled === false
        ? jsonResponse({ type: "about:blank" }, 404)
        : jsonResponse({
            resource: "https://crm.acme.test/mcp",
            authorization_servers: ["https://crm.acme.test"],
          });
    }
    if (/\/passports\/[^/]+$/.test(url) && method === "DELETE") {
      const id = url.split("/passports/")[1];
      opts.onDelete?.(id);
      // The server really does stop returning the row (the grant cascade
      // revokes it and the list collapses to the newest per grant), so the
      // fixture has to as well. A mock that kept serving the deleted row would
      // let a broken refetch — or a row that never leaves the list — pass.
      const at = passports.findIndex((p) => (p as { id: string }).id === id);
      if (at !== -1) {
        passports.splice(at, 1);
      }
      return new Response(null, { status: 204 });
    }
    if (url.includes("/passports")) {
      return jsonResponse({
        data: passports,
        page: { next_cursor: null, has_more: false },
      });
    }
    if (url.endsWith("/v1/me")) {
      return jsonResponse({
        user: { email: "ada@acme.test" },
        roles: ["admin"],
        teams: [],
      });
    }
    return jsonResponse({
      data: [],
      page: { next_cursor: null, has_more: false },
    });
  });
}

const render = (ui: ReactNode) => {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return rtlRender(
    <QueryClientProvider client={client}>
      <LocaleProvider initial="en">{ui}</LocaleProvider>
    </QueryClientProvider>,
  );
};

describe("ConnectedAgentsCard", () => {
  it("names a connection by its client, never the raw client id its label carries", async () => {
    vi.stubGlobal("fetch", backend({}));
    render(<ConnectedAgentsCard />);
    await waitFor(() => expect(screen.getByText("Claude Code")).toBeTruthy());
    expect(screen.queryByText(CONNECTED.label)).toBeNull();
    // The provenance answers "which of my passports did this come from?" —
    // the question a human revoking things actually asks.
    expect(screen.getByText("lent from “full test”")).toBeTruthy();
    // The grant's age, not the current credential's: the passport was minted
    // on the 20th, the connection made on the 2nd.
    expect(screen.getByText(/connected 02\/07\/2026/)).toBeTruthy();
  });

  it("dates the grant on the record's calendar and the deadline on the viewer's", async () => {
    // One row carries both purposes, so one render proves both halves of the
    // zone-by-purpose rule — and both instants below fall on a DIFFERENT
    // calendar day in Berlin than on the US west coast, so picking the wrong
    // zone for either cannot pass by coincidence:
    //   connected_at 00:30 UTC  → 2 July in RECORD_ZONE, 1 July in Los Angeles
    //   expires_at   00:00 UTC  → 31 Dec in RECORD_ZONE, 30 Dec in Los Angeles
    pretendViewerZone("America/Los_Angeles");
    vi.stubGlobal(
      "fetch",
      backend({
        passports: [
          {
            ...CONNECTED,
            expires_at: "2026-12-31T00:00:00Z",
            connection: {
              ...CONNECTED.connection,
              connected_at: "2026-07-02T00:30:00Z",
            },
          },
        ],
      }),
    );
    render(<ConnectedAgentsCard />);
    // When the grant was made is a record fact — every colleague reading this
    // installation must be able to quote the same day for it.
    expect(await screen.findByText(/connected 02\/07\/2026/)).toBeTruthy();
    expect(screen.queryByText(/connected 01\/07\/2026/)).toBeNull();
    // When the credential runs out is this human's deadline, on this human's
    // calendar: a fixed zone would promise them a day that, where they are,
    // has not arrived.
    expect(screen.getByText(/credential renews by 30\/12\/2026/)).toBeTruthy();
    expect(screen.queryByText(/credential renews by 31\/12\/2026/)).toBeNull();
  });

  it("leaves a minted passport out, however its label is spelled", async () => {
    vi.stubGlobal("fetch", backend({}));
    render(<ConnectedAgentsCard />);
    await waitFor(() => expect(screen.getByText("Claude Code")).toBeTruthy());
    expect(screen.queryByText(MINTED.label)).toBeNull();
  });

  it("says no agent is connected rather than showing a bare empty state", async () => {
    vi.stubGlobal("fetch", backend({ passports: [MINTED] }));
    render(<ConnectedAgentsCard />);
    await waitFor(() =>
      expect(screen.getByText("No agent is connected yet.")).toBeTruthy(),
    );
  });

  // The guide is reference rather than a decision, so it reads last and closed
  // — EXCEPT in the one state where it is the point of the card. Nobody has
  // connected, so nothing else on the card can be acted on, and a reader who
  // has to open a disclosure to find the only thing to do has been given a
  // puzzle instead of an instruction.
  it("opens the connect guide by itself while no agent is connected", async () => {
    vi.stubGlobal("fetch", backend({ passports: [] }));
    render(<ConnectedAgentsCard />);
    await waitFor(() =>
      expect(screen.getByText("No agent is connected yet.")).toBeTruthy(),
    );
    const guide = screen.getByText("Connect an agent").closest("details");
    if (!(guide instanceof HTMLDetailsElement)) {
      throw new Error("the connect guide is not a disclosure");
    }
    expect(guide.open).toBe(true);
  });

  // And leaves it closed once there is something else to read: four commands
  // nobody runs twice are a footnote to a card whose subject is the clients
  // already connected.
  it("leaves the connect guide closed once an agent is connected", async () => {
    vi.stubGlobal("fetch", backend({}));
    render(<ConnectedAgentsCard />);
    await waitFor(() => expect(screen.getByText("Claude Code")).toBeTruthy());
    const guide = screen.getByText("Connect an agent").closest("details");
    if (!(guide instanceof HTMLDetailsElement)) {
      throw new Error("the connect guide is not a disclosure");
    }
    expect(guide.open).toBe(false);
  });

  it("offers a connect command per client, built from the URL the server advertises", async () => {
    vi.stubGlobal("fetch", backend({ passports: [] }));
    render(<ConnectedAgentsCard />);
    await waitFor(() =>
      expect(
        screen.getByText(
          "claude mcp add --transport http margince https://crm.acme.test/mcp",
        ),
      ).toBeTruthy(),
    );
    expect(
      screen.getByText(
        /codex mcp add margince --url https:\/\/crm\.acme\.test/,
      ),
    ).toBeTruthy();
    expect(
      screen.getByText(
        "gemini mcp add --transport http margince https://crm.acme.test/mcp",
      ),
    ).toBeTruthy();
    // Antigravity rejects the `url`/`httpUrl` spellings, so the guide must
    // carry `serverUrl` — a wrong key here is a config that silently no-ops.
    expect(
      screen.getByText(/"serverUrl": "https:\/\/crm\.acme\.test\/mcp"/),
    ).toBeTruthy();
  });

  it("says the connector is off instead of printing commands that cannot work", async () => {
    vi.stubGlobal("fetch", backend({ passports: [], connectorEnabled: false }));
    render(<ConnectedAgentsCard />);
    await waitFor(() =>
      expect(
        screen.getByText("The MCP connector is off for this installation."),
      ).toBeTruthy(),
    );
    expect(screen.queryByText(/claude mcp add/)).toBeNull();
  });

  // A credential that ran out ends the connection just as surely as a revoke,
  // and only one of the two writes a column. Reading revoked_at alone left an
  // expired connection reading as live, with a Disconnect button aimed at a
  // credential that had already stopped working.
  it("reports a connection whose credential expired as ended, not as live", async () => {
    // The clock is pinned rather than read: "expired" is a comparison against
    // now, and a test that let the real clock decide it would pass today and
    // fail on the fixture's own expiry date.
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-08-03T09:00:00Z"));
    try {
      vi.stubGlobal("fetch", backend({ passports: [LAPSED] }));
      render(<ConnectedAgentsCard />);
      // Scoped to the row: "Claude Code" also labels the connect guide's own
      // command, and a bare text query would match either.
      await vi.waitFor(() =>
        expect(
          document.querySelector('[data-testid="connection-pp-lapsed"]'),
        ).toBeTruthy(),
      );
      expect(screen.getByText("credential expired")).toBeTruthy();
      expect(screen.getByText(/credential expired 30\/07\/2026/)).toBeTruthy();
      // No Disconnect: it would aim at a credential that is already gone. The
      // grant beneath it is still live, so the way to end that for good stays.
      expect(screen.queryByRole("button", { name: /^Disconnect/ })).toBeNull();
      expect(
        screen.getByRole("button", {
          name: "End the connection to Claude Code",
        }),
      ).toBeTruthy();
    } finally {
      vi.useRealTimers();
    }
  });

  // The distinction the expiry alone cannot make. A grant with offline_access
  // mints its own replacement, so its credential turning over is a renewal, not
  // an ending — treating every expiry as terminal reports live connectors as
  // dead and takes away the control that ends them.
  it("reports an expired but renewable connection as renewing, and keeps it actionable", async () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-08-03T09:00:00Z"));
    try {
      vi.stubGlobal("fetch", backend({ passports: [RENEWING] }));
      render(<ConnectedAgentsCard />);
      await vi.waitFor(() =>
        expect(
          document.querySelector('[data-testid="connection-pp-renewing"]'),
        ).toBeTruthy(),
      );
      expect(screen.getByText("renewing")).toBeTruthy();
      // Regex, not an exact string: the phrase carries a formatted date, so an
      // exact-match query would miss "credential expired 30/07/2026" and pass
      // against the very contradiction this asserts is gone. A renewing row
      // says nothing about the expiry at all — the badge is the whole state.
      expect(screen.queryByText(/credential expired/)).toBeNull();
      expect(screen.queryByText(/credential renews by/)).toBeNull();
      // Still the human's to end, and still by the primary control.
      expect(
        screen.getByRole("button", { name: "Disconnect Claude Code" }),
      ).toBeTruthy();
    } finally {
      vi.useRealTimers();
    }
  });

  it("names the client in each row's accessible action, so two connections are told apart", async () => {
    vi.stubGlobal("fetch", backend({}));
    render(<ConnectedAgentsCard />);
    await waitFor(() => expect(screen.getByText("Claude Code")).toBeTruthy());
    expect(
      screen.getByRole("button", { name: "Disconnect Claude Code" }),
    ).toBeTruthy();
  });

  it("disconnects through the connection's own credential, and warns that the whole connection ends", async () => {
    const deleted: string[] = [];
    vi.stubGlobal("fetch", backend({ onDelete: (id) => deleted.push(id) }));
    render(<ConnectedAgentsCard />);
    await waitFor(() => expect(screen.getByText("Claude Code")).toBeTruthy());

    // The row's verb names the client it would disconnect; the confirm inside
    // the dialog names the act alone. Two buttons reading "Disconnect" one
    // dialog apart are ambiguous for a reader and for a name-based query, so
    // the query below is the one that proves they are still separable.
    const opener = screen.getByRole("button", {
      name: "Disconnect Claude Code",
    });
    expect(opener.textContent).toBe("Disconnect");
    await userEvent.click(opener);
    expect(screen.getByText(/ends the whole connection/)).toBeTruthy();
    const dialog = screen.getByRole("dialog");
    await userEvent.click(
      within(dialog).getByRole("button", { name: "Disconnect" }),
    );

    await waitFor(() => expect(deleted).toEqual([CONNECTED.id]));
    // The DELETE firing is not the claim — the row leaving the list is. Without
    // this the test passes on a refetch that never happens.
    await waitFor(() =>
      expect(screen.getByText("No agent is connected yet.")).toBeTruthy(),
    );
    expect(document.querySelector('[data-testid^="connection-"]')).toBeNull();
  });

  // Ending a connection removes the row the confirm was opened from, so there is
  // no opener left to hand focus back to: without a named target focus falls to
  // <body> and the next Tab restarts at the top of the page.
  it("leaves focus in the connections list after a disconnect, not on the document", async () => {
    vi.stubGlobal("fetch", backend({}));
    render(<ConnectedAgentsCard />);
    await waitFor(() => expect(screen.getByText("Claude Code")).toBeTruthy());

    const opener = screen.getByRole("button", {
      name: "Disconnect Claude Code",
    });
    await userEvent.click(opener);
    await userEvent.click(
      within(screen.getByRole("dialog")).getByRole("button", {
        name: "Disconnect",
      }),
    );

    await waitFor(() =>
      expect(screen.getByText("No agent is connected yet.")).toBeTruthy(),
    );
    expect(opener.isConnected).toBe(false);
    // The region that held the row, which now reads back what is left — the
    // question somebody who just disconnected a client has next.
    const landed = document.activeElement;
    if (!(landed instanceof HTMLElement)) {
      throw new Error("focus left the document entirely after the disconnect");
    }
    expect(landed).not.toBe(document.body);
    expect(landed.textContent).toContain("No agent is connected yet.");
  });
});

describe("the two passport cards on Your agents", () => {
  it("keeps a connection out of the passports a human may lend", async () => {
    vi.stubGlobal("fetch", backend({}));
    render(<SettingsScreen route={settingsAddress("agents")} />);
    await waitFor(() => expect(screen.getByText("Claude Code")).toBeTruthy());
    // The minted passport is listed as lendable...
    const passports = document.querySelector('[data-passport="pp-minted"]');
    expect(passports).toBeTruthy();
    // ...and the connection is not, because it is not the human's to lend.
    expect(
      document.querySelector('[data-passport="pp-connection"]'),
    ).toBeNull();
    expect(
      document.querySelector('[data-testid="connection-pp-connection"]'),
    ).toBeTruthy();
  });
});
