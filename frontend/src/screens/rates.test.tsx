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
import { type GrantSpec, meFixture } from "../app/mefixture";
import { LocaleProvider } from "../i18n";
import { RatesScreen } from "./rates";

// The rates editor renders both price sheets read-only for any role that
// reaches the tab, and shows the write affordances (Set rate / Add model
// rate) only for admin/ops — the server stays the authority regardless.

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

// Both rate cards gate on CREATE, not update: setting a rate is an upsert the
// server admits through the create grant, and no endpoint anywhere checks
// fx_rate:update. A fixture granting update instead would leave both cards
// hidden — which is exactly the mis-binding this names away.
const RATE_SETTER: GrantSpec = {
  fx_rate: ["create"],
  ai_model_rate: ["create"],
};

function ratesBackend(allow: GrantSpec) {
  return vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input instanceof Request ? input.url : input);
    if (url.endsWith("/v1/me")) {
      return jsonResponse(meFixture({ allow }));
    }
    if (url.includes("/v1/fx-rates")) {
      return jsonResponse({
        data: [
          {
            from_currency: "USD",
            to_currency: "EUR",
            rate: "0.9200000000",
            effective_date: "2026-07-23",
          },
        ],
      });
    }
    if (url.includes("/v1/ai-model-rates")) {
      return jsonResponse({
        data: [
          {
            provider: "anthropic",
            model_id: "claude-opus-4-8",
            input_per_mtok: "5",
            output_per_mtok: "25",
            cache_read_per_mtok: "0.5",
            cache_write_per_mtok: "6.25",
            effective_date: "2026-07-23",
          },
        ],
      });
    }
    return jsonResponse({}, 404);
  });
}

function render(ui: ReactNode) {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return rtlRender(
    <QueryClientProvider client={qc}>
      <LocaleProvider>{ui}</LocaleProvider>
    </QueryClientProvider>,
  );
}

describe("RatesScreen", () => {
  beforeEach(() => {
    globalThis.localStorage?.setItem("margince.workspaceSlug", "acme");
  });

  it("renders both price sheets with their current rows", async () => {
    vi.stubGlobal("fetch", ratesBackend(RATE_SETTER));
    render(<RatesScreen />);
    // trimDecimal turns the numeric(20,10) value into a readable 0.92.
    await waitFor(() => expect(screen.getByText("USD")).toBeTruthy());
    expect(screen.getByText("0.92")).toBeTruthy();
    expect(screen.getByText("claude-opus-4-8")).toBeTruthy();
    expect(screen.getByText("6.25")).toBeTruthy();
  });

  it("shows write affordances for an admin", async () => {
    vi.stubGlobal("fetch", ratesBackend(RATE_SETTER));
    render(<RatesScreen />);
    await waitFor(() => expect(screen.getByText("USD")).toBeTruthy());
    expect(screen.getByRole("button", { name: "Set rate" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Add model rate" })).toBeTruthy();
    // Both cards expose the async "Refresh from sources" control to an admin.
    expect(
      screen.getAllByRole("button", { name: "Refresh from sources" }),
    ).toHaveLength(2);
  });

  it("hides write affordances for a non-admin role", async () => {
    vi.stubGlobal("fetch", ratesBackend({}));
    render(<RatesScreen />);
    await waitFor(() => expect(screen.getByText("USD")).toBeTruthy());
    expect(screen.queryByRole("button", { name: "Set rate" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Add model rate" })).toBeNull();
  });
});
