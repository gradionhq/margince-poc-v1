/** @vitest-environment jsdom */
import { cleanup, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { type GrantSpec, meFixture } from "../app/mefixture";
import { jsonResponse, renderSettings } from "./settings.testkit";

// The two entries one Connections tab became: the PERSON's mailbox and network
// on Connections, the INSTALLATION's outside wiring on Integrations. Each page
// has to carry its own half and none of the other's — a relabelling would pass
// any claim that only ever looked at one of them.

// No shared fetch stub: the backend a claim needs is installed beside the claim,
// so what answered it is readable where it is asserted.
beforeEach(() => {
  globalThis.localStorage.setItem("margince.workspaceSlug", "acme");
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  globalThis.localStorage.clear();
});

// The whole of Overlay — connect, sync/budget health, user mapping — sits on
// Integrations beside the provider credential and the webhooks, because all of
// them are the INSTALLATION's outside wiring: one shared key, one set of
// subscriptions, one system-of-record flip that re-points every read. The
// personal mailbox and LinkedIn network that used to share the entry are on
// Connections, and neither page carries the other's cards.
//
// `system_of_record` is stubbed explicitly per test: the entry must stay reachable
// in native mode (a workspace is native until an overlay is connected, so gating
// it on overlay mode would hide the only place to connect one), and a retired
// route id must carry none of it.
function overlaySettingsBackend(opts: {
  roles: string[];
  allow?: GrantSpec;
  sorMode: "native" | "overlay";
}) {
  return vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input instanceof Request ? input.url : input);
    // The two overlay reads below answer GET only. A mock that answers any verb
    // hands a successful payload to a request the real endpoint would reject,
    // so a client sending the wrong one would still pass here.
    const method = (
      input instanceof Request ? input.method : (init?.method ?? "GET")
    ).toUpperCase();
    if (url.endsWith("/v1/me")) {
      const me = meFixture({ roles: opts.roles, allow: opts.allow ?? {} });
      return jsonResponse({
        ...me,
        user: { ...me.user, email: "ada@acme.test" },
        system_of_record: { mode: opts.sorMode },
      });
    }
    if (url.includes("/overlay/connection")) {
      return jsonResponse({ detail: "not found" }, 404);
    }
    if (url.includes("/overlay/user-map") && method === "GET") {
      return jsonResponse({
        incumbent: "hubspot",
        entries: [],
        next_cursor: null,
      });
    }
    if (url.includes("/overlay/owners") && method === "GET") {
      return jsonResponse({
        incumbent: "hubspot",
        owners: [],
        truncated: false,
      });
    }
    return jsonResponse({
      data: [],
      page: { next_cursor: null, has_more: false },
    });
  });
}

// The reads the seeded matrix gives every role on the installation's wiring, and
// the two terms of the Integrations predicate — granted wherever a case needs the
// entry to be OPEN, so an absent card on it can only mean the card is elsewhere.
const WIRING_READS: GrantSpec = {
  overlay_connection: ["read"],
  webhook_subscription: ["read"],
};

describe("SettingsScreen connections and integrations tabs", () => {
  it("carries the overlay on Integrations, reachable before any overlay is connected", async () => {
    vi.stubGlobal(
      "fetch",
      overlaySettingsBackend({
        roles: ["admin"],
        allow: WIRING_READS,
        sorMode: "native",
      }),
    );
    renderSettings("integrations");
    await waitFor(() =>
      expect(
        screen
          .getByRole("link", { name: "Integrations" })
          .getAttribute("aria-current"),
      ).toBe("page"),
    );
    // OverlayCard's own heading — proof its connect flow rendered on this
    // entry even though the workspace has never connected an overlay.
    expect(
      await screen.findByRole("heading", { name: "HubSpot mirror" }),
    ).toBeTruthy();
  });

  // A retired id is what a bookmark still carries: the audit trail was an entry
  // of its own before it moved onto Privacy & audit, so `#/settings/audit` names
  // nothing. It has to land on the first entry this principal can see rather than
  // on a blank screen. The wiring reads are granted so Integrations is genuinely
  // open — a fallback that happens because an entry is hidden proves nothing
  // about a route id that no longer exists.
  it("falls back to Account when the route names a retired entry", async () => {
    vi.stubGlobal(
      "fetch",
      overlaySettingsBackend({
        roles: ["admin"],
        allow: WIRING_READS,
        sorMode: "overlay",
      }),
    );
    renderSettings("audit");
    await waitFor(() =>
      expect(
        screen
          .getByRole("link", { name: "Account" })
          .getAttribute("aria-current"),
      ).toBe("page"),
    );
    // The Account tab's own content, not merely its nav entry: the fallback has
    // to render a page, and the sidebar carries the viewer's email either way.
    expect(
      await screen.findByRole("heading", { name: "Your account" }),
    ).toBeTruthy();
    expect(
      screen.queryByRole("heading", { name: "HubSpot mirror" }),
    ).toBeNull();
  });

  // The overlay is the installation's, so its page sits in the admin group and
  // asks for the wiring read on top of the operator seat. An OPS principal is the
  // case worth proving: they reach the entry, and reaching it costs no
  // confidentiality — both cards' write and management reads are admin-only on
  // the server, and each keeps them unsent for anyone else, so ops sees the
  // connection card's read-only state and the mapping card's admin-only notice,
  // never the directory.
  it("shows Integrations to a non-admin ops with both overlay cards in their read-only state", async () => {
    const fetchMock = overlaySettingsBackend({
      roles: ["ops"],
      allow: WIRING_READS,
      sorMode: "native",
    });
    vi.stubGlobal("fetch", fetchMock);
    renderSettings("integrations");
    await waitFor(() =>
      expect(
        screen
          .getByRole("link", { name: "Integrations" })
          .getAttribute("aria-current"),
      ).toBe("page"),
    );
    expect(
      await screen.findByRole("heading", { name: "HubSpot mirror" }),
    ).toBeTruthy();
    expect(
      await screen.findByText(
        "You do not have permission to change the HubSpot connection.",
      ),
    ).toBeTruthy();
    expect(
      await screen.findByText(
        "You do not have permission to review who is mapped.",
      ),
    ).toBeTruthy();
    // No mapping table, no grouping toggle, and — the point of the card's
    // own gate — no request that could only have come back 403.
    expect(screen.queryByRole("group", { name: "Grouping" })).toBeNull();
    expect(screen.queryByRole("button", { name: "By user" })).toBeNull();
    const requested = fetchMock.mock.calls.map(([input]) => String(input));
    expect(
      requested.some((url) => url.includes("/overlay/user-map")),
    ).toBeFalsy();
    expect(
      requested.some((url) => url.includes("/overlay/owners")),
    ).toBeFalsy();
  });

  // The split, from both sides. One entry used to hold a rep's own mailbox and
  // the installation's webhooks together, which is why it could carry no honest
  // predicate: any gate on it took a personal task away from whoever it hid it
  // from. The two cases below are what makes the split real rather than a
  // relabelling — each page has to carry its own half and NOT the other's, and
  // the wiring reads are granted in both so an absent card can only mean the card
  // lives on the other entry.
  it("renders the personal connections on Connections and none of the installation's wiring", async () => {
    vi.stubGlobal(
      "fetch",
      overlaySettingsBackend({
        roles: ["admin"],
        allow: WIRING_READS,
        sorMode: "native",
      }),
    );
    renderSettings("connections");
    // Every surface here reads a per-user seam: the connector list is scoped to
    // the calling human server-side, and both LinkedIn cards read /me.
    expect(
      await screen.findByRole("heading", { name: "Connected inboxes" }),
    ).toBeTruthy();
    expect(
      screen.getByRole("heading", { name: "LinkedIn connections" }),
    ).toBeTruthy();
    expect(
      screen.getByRole("heading", { name: "Where your network reaches" }),
    ).toBeTruthy();
    // And nothing workspace-wide: a key everybody spends from, subscriptions
    // everybody's writes fire, the mirror that re-points every read.
    for (const heading of [
      "Contact data",
      "Webhooks",
      "HubSpot mirror",
      "Mirror user mapping",
    ]) {
      expect(screen.queryByRole("heading", { name: heading })).toBeNull();
    }
  });

  it("renders the installation's wiring on Integrations and none of the personal connections", async () => {
    vi.stubGlobal(
      "fetch",
      overlaySettingsBackend({
        roles: ["admin"],
        allow: WIRING_READS,
        sorMode: "native",
      }),
    );
    renderSettings("integrations");
    expect(
      await screen.findByRole("heading", { name: "Contact data" }),
    ).toBeTruthy();
    for (const heading of [
      "Webhooks",
      "HubSpot mirror",
      "Mirror user mapping",
    ]) {
      expect(screen.getByRole("heading", { name: heading })).toBeTruthy();
    }
    for (const heading of [
      "Connected inboxes",
      "LinkedIn connections",
      "Where your network reaches",
    ]) {
      expect(screen.queryByRole("heading", { name: heading })).toBeNull();
    }
  });
});
