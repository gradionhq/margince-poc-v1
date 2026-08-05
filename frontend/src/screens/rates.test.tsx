/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  cleanup,
  render as rtlRender,
  screen,
  waitFor,
  within,
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

// Setting a rate is an UPSERT: it inserts on a (pair, day) the sheet has never
// carried and replaces the row when it has. The server admits the call on
// either write grant and then demands the specific one inside the transaction,
// so each card asks the same union — hence a `create`-only fixture and an
// `update`-only one both have to open the card they name.
const RATE_SETTER: GrantSpec = {
  fx_rate: ["create"],
  ai_model_rate: ["create"],
};

// The card a heading names, so an affordance can be attributed to ONE sheet.
// Both cards spell "Refresh from sources" identically, and a screen-wide query
// for it cannot tell which card offered it — which is precisely the confusion
// a transposed `useCanUpsert` object would hide in.
function rateCard(title: string): HTMLElement {
  const card = screen.getByRole("heading", { name: title }).closest("section");
  if (!(card instanceof HTMLElement)) {
    throw new Error(`no rate card is headed "${title}"`);
  }
  return card;
}

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

  // One object at a time. A fixture that grants both sheets at once cannot tell
  // a correct binding from a transposed one — each card would find its grant
  // either way — so each case below grants exactly one and requires the OTHER
  // card to stay read-only.
  it("opens the FX sheet alone on an fx_rate create grant", async () => {
    vi.stubGlobal("fetch", ratesBackend({ fx_rate: ["create"] }));
    render(<RatesScreen />);
    await waitFor(() => expect(screen.getByText("USD")).toBeTruthy());

    const fx = rateCard("Currency rates");
    expect(within(fx).getByRole("button", { name: "Set rate" })).toBeTruthy();
    expect(
      within(fx).getByRole("button", { name: "Refresh from sources" }),
    ).toBeTruthy();

    // The model sheet still renders its rows — it is the WRITING that is
    // withheld, not the reading.
    const model = rateCard("AI model costs");
    expect(within(model).getByText("claude-opus-4-8")).toBeTruthy();
    expect(
      within(model).queryByRole("button", { name: "Add model rate" }),
    ).toBeNull();
    expect(
      within(model).queryByRole("button", { name: "Refresh from sources" }),
    ).toBeNull();
  });

  it("opens the model sheet alone on an ai_model_rate update grant", async () => {
    // `update` and not `create`: the upsert admits either, so the mirror case
    // also proves the card asks the union rather than one hard-coded verb.
    vi.stubGlobal("fetch", ratesBackend({ ai_model_rate: ["update"] }));
    render(<RatesScreen />);
    await waitFor(() => expect(screen.getByText("USD")).toBeTruthy());

    const model = rateCard("AI model costs");
    expect(
      within(model).getByRole("button", { name: "Add model rate" }),
    ).toBeTruthy();
    expect(
      within(model).getByRole("button", { name: "Refresh from sources" }),
    ).toBeTruthy();

    const fx = rateCard("Currency rates");
    expect(within(fx).getByText("USD")).toBeTruthy();
    expect(within(fx).queryByRole("button", { name: "Set rate" })).toBeNull();
    expect(
      within(fx).queryByRole("button", { name: "Refresh from sources" }),
    ).toBeNull();
  });

  it("offers no write affordance on either sheet for a read-only grant on both", async () => {
    // A read grant is not an absent one: the object IS in the snapshot, with
    // every write verb false. A card that checked only for the object's
    // presence would open here.
    vi.stubGlobal(
      "fetch",
      ratesBackend({ fx_rate: ["read"], ai_model_rate: ["read"] }),
    );
    render(<RatesScreen />);
    await waitFor(() => expect(screen.getByText("USD")).toBeTruthy());
    expect(screen.getByText("claude-opus-4-8")).toBeTruthy();

    expect(screen.queryByRole("button", { name: "Set rate" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Add model rate" })).toBeNull();
    expect(
      screen.queryAllByRole("button", { name: "Refresh from sources" }),
    ).toEqual([]);
  });
});
