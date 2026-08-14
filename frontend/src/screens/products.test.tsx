/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render as rtlRender, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { type GrantSpec, meFixture } from "../app/mefixture";
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

// One grant at a time: create, update and delete govern three different
// controls, and a fixture holding all of them cannot tell a correct binding
// from a transposed one.
const PRODUCT_MANAGER: GrantSpec = {
  product: ["read", "create", "update", "delete"],
};
const PRODUCT_READER: GrantSpec = { product: ["read"] };

// The list read, the /me snapshot the affordance gates ask, and (when a test
// supplies one) whatever the create POST should answer.
function productsStub({
  allow,
  seat = "full",
  onPost,
}: {
  allow: GrantSpec;
  seat?: "full" | "read";
  onPost?: () => Response;
}) {
  return vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input instanceof Request ? input.url : input);
    const method =
      (input instanceof Request ? input.method : init?.method) ?? "GET";
    if (url.endsWith("/v1/me")) {
      return jsonResponse(meFixture({ allow, seat }));
    }
    if (method === "POST" && onPost) {
      return onPost();
    }
    return jsonResponse({
      data: [product],
      page: { next_cursor: null, has_more: false },
    });
  });
}

function newButton() {
  return screen.queryByRole("button", { name: "New product" });
}
function editButton() {
  return screen.queryByRole("button", { name: "Edit product" });
}
function archiveButton() {
  return screen.queryByRole("button", { name: "Archive product" });
}
function postureLine() {
  return screen.queryByText("Read-only view — you may not change products.");
}

describe("ProductsAdmin", () => {
  it("renders products with money formatted from minor units", async () => {
    vi.stubGlobal("fetch", productsStub({ allow: PRODUCT_MANAGER }));
    render(<ProductsAdmin />);
    expect(await screen.findByText("Consulting Day")).toBeTruthy();
    expect(screen.getByText("CONS-DAY")).toBeTruthy();
    // 150000 minor EUR -> "€1,500.00" (en locale)
    expect(screen.getByText(/1,500\.00/)).toBeTruthy();
  });

  it("surfaces a 409 SKU-duplicate detail verbatim on create", async () => {
    vi.stubGlobal(
      "fetch",
      productsStub({
        allow: PRODUCT_MANAGER,
        onPost: () =>
          jsonResponse(
            {
              title: "conflict",
              detail: "sku already in use",
              code: "duplicate_sku",
            },
            409,
          ),
      }),
    );
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

  it("gives a read-only seat the rows, the posture line and no write affordance", async () => {
    vi.stubGlobal("fetch", productsStub({ allow: PRODUCT_READER }));
    render(<ProductsAdmin />);
    // The list itself is never withheld — every seat reads it.
    expect(await screen.findByText("Consulting Day")).toBeTruthy();
    expect(postureLine()).toBeTruthy();
    expect(newButton()).toBeNull();
    expect(editButton()).toBeNull();
    expect(archiveButton()).toBeNull();
    // With both row verbs withheld the column goes too, rather than standing
    // there headed and empty.
    expect(screen.queryByRole("columnheader", { name: "Actions" })).toBeNull();
  });

  it("offers New on product:create alone", async () => {
    vi.stubGlobal("fetch", productsStub({ allow: { product: ["create"] } }));
    render(<ProductsAdmin />);
    expect(
      await screen.findByRole("button", { name: "New product" }),
    ).toBeTruthy();
    expect(editButton()).toBeNull();
    expect(archiveButton()).toBeNull();
    expect(postureLine()).toBeNull();
  });

  it("offers Edit on product:update alone", async () => {
    vi.stubGlobal("fetch", productsStub({ allow: { product: ["update"] } }));
    render(<ProductsAdmin />);
    expect(
      await screen.findByRole("button", { name: "Edit product" }),
    ).toBeTruthy();
    expect(screen.getByRole("columnheader", { name: "Actions" })).toBeTruthy();
    expect(newButton()).toBeNull();
    expect(archiveButton()).toBeNull();
    expect(postureLine()).toBeNull();
  });

  it("offers Archive on product:delete alone", async () => {
    vi.stubGlobal("fetch", productsStub({ allow: { product: ["delete"] } }));
    render(<ProductsAdmin />);
    expect(
      await screen.findByRole("button", { name: "Archive product" }),
    ).toBeTruthy();
    expect(newButton()).toBeNull();
    expect(editButton()).toBeNull();
    expect(postureLine()).toBeNull();
  });

  it("withholds every affordance from a read seat holding all three grants", async () => {
    // The seat ceiling is clamped on the HTTP method before RBAC is consulted,
    // so the grants alone do not make a control honourable.
    vi.stubGlobal(
      "fetch",
      productsStub({ allow: PRODUCT_MANAGER, seat: "read" }),
    );
    render(<ProductsAdmin />);
    expect(await screen.findByText("Consulting Day")).toBeTruthy();
    expect(postureLine()).toBeTruthy();
    expect(newButton()).toBeNull();
    expect(editButton()).toBeNull();
    expect(archiveButton()).toBeNull();
  });
});
