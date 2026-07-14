/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  cleanup,
  render as rtlRender,
  screen,
  waitFor,
} from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { LocaleProvider } from "../i18n";
import { SettingsScreen } from "./settings";

// The settings identity + passport surfaces through the RBAC primitives:
// roles render as localized RoleBadges (a workspace-defined key stays raw),
// and the passport list's token slot reads as WITHHELD (FieldGuard mask) —
// the wire schema carries no token, and the row says so instead of omitting
// the field as if none existed.

beforeEach(() => {
  globalThis.localStorage.setItem("margince.workspaceSlug", "acme");
  vi.stubGlobal("fetch", settingsBackend());
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

// Routed by URL so every card on the screen gets an honest per-endpoint
// answer; the cards not under test render their empty states.
function settingsBackend() {
  return vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input instanceof Request ? input.url : input);
    if (url.endsWith("/v1/me")) {
      return jsonResponse({
        user: { email: "ada@acme.test" },
        roles: ["admin", "field_marketing"],
        teams: [],
      });
    }
    if (url.includes("/passports")) {
      return jsonResponse({
        data: [
          {
            id: "pp-1",
            label: "Scout",
            scopes: ["read"],
            created_at: "2026-07-01T08:00:00Z",
            expires_at: null,
            revoked_at: null,
          },
        ],
        page: { next_cursor: null, has_more: false },
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

describe("SettingsScreen RBAC surfaces", () => {
  it("renders the session roles as localized badges on the default Account tab; a custom key stays its raw self", async () => {
    render(<SettingsScreen />);
    await waitFor(() => expect(screen.getByText("ada@acme.test")).toBeTruthy());
    expect(screen.getByText("Admin")).toBeTruthy();
    expect(screen.getByText("field_marketing")).toBeTruthy();
    // the seeded key never leaks raw once a label exists
    expect(screen.queryByText("admin")).toBeNull();
  });

  it("the passport row's token reads as withheld — masked, never re-disclosed — on the AI tab", async () => {
    render(<SettingsScreen tab="ai" />);
    await waitFor(() => expect(screen.getByText("Scout")).toBeTruthy());
    expect(screen.getByRole("img", { name: "Masked value" })).toBeTruthy();
    expect(screen.queryByText(/mgp_/)).toBeNull();
  });
});

describe("SettingsScreen tab layout", () => {
  it("shows a settings-sections nav with the six tabs, Account current by default", () => {
    render(<SettingsScreen />);
    const nav = screen.getByRole("navigation", { name: /settings sections/i });
    expect(nav).toBeTruthy();
    for (const label of [
      "Account",
      "AI & autonomy",
      "Data model",
      "Catalog",
      "Privacy & consent",
      "Audit log",
    ]) {
      expect(
        screen.getByRole("link", { name: new RegExp(label, "i") }),
      ).toBeTruthy();
    }
    const account = screen.getByRole("link", { name: /Account/i });
    expect(account.getAttribute("aria-current")).toBe("page");
    expect(
      screen.getByRole("link", { name: /Data model/i }).getAttribute("href"),
    ).toBe("#/settings/data");
  });

  it("renders only the active tab's cards — the passport is off the Account tab", async () => {
    render(<SettingsScreen />);
    await waitFor(() => expect(screen.getByText("ada@acme.test")).toBeTruthy());
    // Scout lives on the AI tab; the default Account tab must not render it.
    expect(screen.queryByText("Scout")).toBeNull();
  });

  it("surfaces the custom-fields door on the Data model tab", () => {
    render(<SettingsScreen tab="data" />);
    expect(screen.getByRole("link", { name: /custom fields/i })).toBeTruthy();
  });

  it("surfaces the Products and Offer-templates doors on the Catalog tab", () => {
    render(<SettingsScreen tab="catalog" />);
    expect(
      screen.getByRole("link", { name: /products/i }).getAttribute("href"),
    ).toBe("#/products");
    expect(
      screen
        .getByRole("link", { name: /offer templates/i })
        .getAttribute("href"),
    ).toBe("#/offer-templates");
  });
});
