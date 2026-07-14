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
import { LocaleProvider } from "../i18n";
import { ProductsScreen } from "./products";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  window.location.hash = "";
});

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}
function render(ui: ReactNode) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return rtlRender(
    <QueryClientProvider client={client}>
      <LocaleProvider initial="en">{ui}</LocaleProvider>
    </QueryClientProvider>,
  );
}
const product = {
  id: "p-1",
  workspace_id: "w",
  name: "Consulting Day",
  sku: "CONS-DAY",
  unit: "day",
  unit_price_minor: 150000,
  currency: "EUR",
  default_tax_rate: 19,
  active: true,
  source: "manual",
  captured_by: "human:u1",
  version: 1,
  created_at: "2026-06-01T08:00:00Z",
  updated_at: "2026-06-01T08:00:00Z",
};

describe("ProductsScreen", () => {
  it("renders products with money formatted from minor units", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        jsonResponse({
          data: [product],
          page: { next_cursor: null, has_more: false },
        }),
      ),
    );
    render(<ProductsScreen />);
    expect(await screen.findByText("Consulting Day")).toBeTruthy();
    expect(screen.getByText("CONS-DAY")).toBeTruthy();
    // 150000 minor EUR -> "€1,500.00" (en locale)
    expect(screen.getByText(/1,500\.00/)).toBeTruthy();
  });

  it("surfaces a 409 SKU-duplicate detail verbatim on create", async () => {
    const fetchMock = vi.fn(
      async (input: RequestInfo | URL, init?: RequestInit) => {
        const method =
          (input instanceof Request ? input.method : init?.method) ?? "GET";
        if (method === "POST") {
          return jsonResponse(
            {
              title: "conflict",
              detail: "sku already in use",
              code: "duplicate_sku",
            },
            409,
          );
        }
        return jsonResponse({
          data: [],
          page: { next_cursor: null, has_more: false },
        });
      },
    );
    vi.stubGlobal("fetch", fetchMock);
    render(<ProductsScreen />);
    await userEvent.click(await screen.findByTestId("new-record"));
    await waitFor(() => screen.getByLabelText(/Name/));
    await userEvent.type(screen.getByLabelText(/Name/), "Consulting Day");
    await userEvent.type(screen.getByLabelText(/Unit price/), "1500");
    await userEvent.click(screen.getByText("Create"));
    expect(await screen.findByText("sku already in use")).toBeTruthy();
  });
});
