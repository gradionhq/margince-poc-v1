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
import { SettingsScreen } from "./settings";

// The split GET /passports feeds: a passport the human minted belongs to the
// passports card, a connection's credential to this one. `connection` decides,
// which is what the label fixtures below exist to prove — a minted passport
// NAMED like a connection stays a passport, and a real connection is shown
// under its client's registered name rather than the oauth: label it carries.

beforeEach(() => {
  globalThis.localStorage.setItem("margince.workspaceSlug", "acme");
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  globalThis.localStorage.clear();
});

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
  expires_at: "2026-08-20T08:00:00Z",
  revoked_at: null,
  connection: {
    client_id: DCR_CLIENT_ID,
    client_name: "Claude Code",
    connected_at: "2026-07-02T09:00:00Z",
    lent_passport_id: "pp-minted",
    lent_passport_label: "full test",
  },
};

// The same connection after its credential simply ran out. A grant without
// offline_access cannot renew, so this is how a connection ends WITHOUT
// anything writing revoked_at — the state that used to render as live.
const LAPSED = {
  ...CONNECTED,
  id: "pp-lapsed",
  expires_at: "2026-07-30T08:00:00Z",
};

// One backend for both cards, since they share the ["passports"] read.
// `connectorEnabled: false` answers discovery with the 404 an installation
// serving no /mcp routes produces.
function backend(opts: {
  passports?: unknown[];
  connectorEnabled?: boolean;
  onDelete?: (id: string) => void;
}) {
  const passports = opts.passports ?? [MINTED, CONNECTED];
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
      opts.onDelete?.(url.split("/passports/")[1]);
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
          document.querySelector('[data-connection="pp-lapsed"]'),
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

    await userEvent.click(
      screen.getByRole("button", { name: "Disconnect Claude Code" }),
    );
    expect(screen.getByText(/ends the whole connection/)).toBeTruthy();
    const dialog = screen.getByRole("dialog");
    await userEvent.click(
      within(dialog).getByRole("button", { name: "Disconnect" }),
    );

    await waitFor(() => expect(deleted).toEqual([CONNECTED.id]));
  });
});

describe("the AI tab's two passport cards", () => {
  it("keeps a connection out of the passports a human may lend", async () => {
    vi.stubGlobal("fetch", backend({}));
    render(<SettingsScreen tab="ai" />);
    await waitFor(() => expect(screen.getByText("Claude Code")).toBeTruthy());
    // The minted passport is listed as lendable...
    const passports = document.querySelector('[data-passport="pp-minted"]');
    expect(passports).toBeTruthy();
    // ...and the connection is not, because it is not the human's to lend.
    expect(
      document.querySelector('[data-passport="pp-connection"]'),
    ).toBeNull();
    expect(
      document.querySelector('[data-connection="pp-connection"]'),
    ).toBeTruthy();
  });
});
