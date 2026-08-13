/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render as rtlRender, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { LocaleProvider } from "../i18n";
import { ProductsAdmin } from "./products";

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

describe("ProductsAdmin", () => {
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
    render(<ProductsAdmin />);
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
    render(<ProductsAdmin />);
    await userEvent.click(await screen.findByTestId("new-record"));
    // Each column header is a sort button carrying its own column's name, so
    // every form field is asked for by role rather than by a loose text match.
    const nameField = await screen.findByRole("textbox", { name: /Name/ });
    await userEvent.type(nameField, "Consulting Day");
    await userEvent.type(
      screen.getByRole("spinbutton", { name: /Unit price/ }),
      "1500",
    );
    await userEvent.click(screen.getByText("Create"));
    expect(await screen.findByText("sku already in use")).toBeTruthy();
  });
});
