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
import { type GrantSpec, meFixture } from "../app/mefixture";
import { LocaleProvider } from "../i18n";
import { FxRatesCard, ModelCostsCard } from "./rates";

// Each price sheet has three honest states, and a grant picks which one:
// WITHHELD without the object's read grant (fx_rate and ai_model_rate are
// admin/ops-only, so most roles land here), READ-ONLY with the read but no
// upsert — the table plus one caption, write affordances simply absent — and
// EDITABLE with a write verb on a full seat. The server stays the authority
// regardless; these cases pin what the card SAYS.

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
//
// The read rides along in every write fixture because the two are separate
// grants and the card asks for each: a rate-setter who could not read the sheet
// would get the withheld card, and there would be no table to assert against.
const RATE_SETTER: GrantSpec = {
  fx_rate: ["read", "create"],
  ai_model_rate: ["read", "create"],
};

// Read on both objects, no write verb anywhere: the read-only state.
const RATE_READER: GrantSpec = {
  fx_rate: ["read"],
  ai_model_rate: ["read"],
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

// Every URL the cards asked for. A withheld card's defining property is that it
// requests NOTHING — a settled denial needs no round trip to be rendered — and
// no assertion about the DOM can show that; only the call log can.
function ratesBackend(allow: GrantSpec, seat: "full" | "read", urls: string[]) {
  return vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input instanceof Request ? input.url : input);
    urls.push(url);
    if (url.endsWith("/v1/me")) {
      return jsonResponse(meFixture({ allow, seat }));
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

// The two sheets are on different settings pages now (FX under Organization,
// model prices under AI). They are rendered as a pair here because one fixture
// backend answers both, and every case below asserts on one of them alone.
function RateSheets() {
  return (
    <>
      <FxRatesCard />
      <ModelCostsCard />
    </>
  );
}

// One mount, and the request log it produced.
function mount(
  allow: GrantSpec,
  seat: "full" | "read" = "full",
): { urls: string[] } {
  const urls: string[] = [];
  vi.stubGlobal("fetch", ratesBackend(allow, seat, urls));
  render(<RateSheets />);
  return { urls };
}

describe("the rate sheets", () => {
  beforeEach(() => {
    globalThis.localStorage?.setItem("margince.workspaceSlug", "acme");
  });

  it("renders both price sheets with their current rows", async () => {
    mount(RATE_SETTER);
    // trimDecimal turns the numeric(20,10) value into a readable 0.92.
    await waitFor(() => expect(screen.getByText("USD")).toBeTruthy());
    expect(screen.getByText("0.92")).toBeTruthy();
    expect(screen.getByText("claude-opus-4-8")).toBeTruthy();
    expect(screen.getByText("6.25")).toBeTruthy();

    // Each sheet is a stacked settings row, and the row is what says WHICH
    // rates these are — the table's own headers name columns, not the sheet.
    expect(
      within(rateCard("Currency rates")).getByText("Rates in force"),
    ).toBeTruthy();
    expect(
      within(rateCard("AI model costs")).getByText("Prices in force"),
    ).toBeTruthy();
  });

  // The dialogs are where a rate is authored, and every box in them is named by
  // a `Field` that owns the input's id. `getByLabelText` is the assertion that
  // the two are wired: a label pointing at nothing cannot be found this way,
  // which is exactly what a hand-rolled `htmlFor` gets wrong silently.
  it("names every box in the currency-rate dialog", async () => {
    const user = userEvent.setup();
    mount(RATE_SETTER);
    await waitFor(() => expect(screen.getByText("USD")).toBeTruthy());

    await user.click(
      within(rateCard("Currency rates")).getByRole("button", {
        name: "Set rate",
      }),
    );
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByLabelText("From")).toBeTruthy();
    expect(
      within(dialog).getByLabelText("Rate (to base currency)"),
    ).toBeTruthy();
    expect(within(dialog).getByLabelText("Effective")).toBeTruthy();
    // The currency and the rate are still required: an empty dialog cannot be
    // saved, which is the predicate the conversion had to leave alone.
    expect(within(dialog).getByRole("button", { name: "Save" })).toHaveProperty(
      "disabled",
      true,
    );
  });

  it("names every box in the model-price dialog", async () => {
    const user = userEvent.setup();
    mount(RATE_SETTER);
    await waitFor(() =>
      expect(screen.getByText("claude-opus-4-8")).toBeTruthy(),
    );

    await user.click(
      within(rateCard("AI model costs")).getByRole("button", {
        name: "Add model rate",
      }),
    );
    const dialog = await screen.findByRole("dialog");
    for (const label of [
      "Provider",
      "Model",
      "Input $/M",
      "Output $/M",
      "Cache read $/M",
      "Cache write $/M",
      "Effective",
    ]) {
      expect(within(dialog).getByLabelText(label)).toBeTruthy();
    }
    // Provider, model and the two prices are required; the cache columns
    // default to 0 and do not hold the button.
    expect(within(dialog).getByRole("button", { name: "Save" })).toHaveProperty(
      "disabled",
      true,
    );
  });

  it("shows write affordances for an admin", async () => {
    mount(RATE_SETTER);
    await waitFor(() => expect(screen.getByText("USD")).toBeTruthy());
    expect(screen.getByRole("button", { name: "Set rate" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Add model rate" })).toBeTruthy();
    // Both cards expose the async "Refresh from sources" control to an admin.
    expect(
      screen.getAllByRole("button", { name: "Refresh from sources" }),
    ).toHaveLength(2);
    // And nothing about reading is withheld or read-only for them.
    expect(screen.queryByText(/read-only view/i)).toBeNull();
  });

  it("hides write affordances for a role granted the read alone", async () => {
    mount(RATE_READER);
    await waitFor(() => expect(screen.getByText("USD")).toBeTruthy());
    expect(screen.queryByRole("button", { name: "Set rate" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Add model rate" })).toBeNull();
  });

  // One object at a time. A fixture that grants both sheets at once cannot tell
  // a correct binding from a transposed one — each card would find its grant
  // either way — so each case below grants exactly one and requires the OTHER
  // card to stay read-only.
  it("opens the FX sheet alone on an fx_rate create grant", async () => {
    mount({ fx_rate: ["read", "create"], ai_model_rate: ["read"] });
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
    mount({ fx_rate: ["read"], ai_model_rate: ["read", "update"] });
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
    mount(RATE_READER);
    await waitFor(() => expect(screen.getByText("USD")).toBeTruthy());
    expect(screen.getByText("claude-opus-4-8")).toBeTruthy();

    expect(screen.queryByRole("button", { name: "Set rate" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Add model rate" })).toBeNull();
    expect(
      screen.queryAllByRole("button", { name: "Refresh from sources" }),
    ).toEqual([]);

    // The read-only posture is stated once per readable sheet, and the withheld
    // reason is NOT: stacking both would explain one denial twice, in two
    // mutually contradictory ways.
    for (const title of ["Currency rates", "AI model costs"]) {
      const card = rateCard(title);
      expect(within(card).getByText(/read-only view/i)).toBeTruthy();
    }
    expect(screen.queryByText(/only an admin or ops can see/i)).toBeNull();
  });

  // Withheld, not absent. Currency rates sits on the Organization page a
  // read_only seat now opens for its other cards, so a sheet that vanished
  // would read as "this installation converts nothing" — and a sheet that
  // fetched its list in order to render the 403 would read as a broken page
  // with a Retry that can only be refused again.
  it("withholds the FX sheet without fx_rate:read and requests no rates", async () => {
    const { urls } = mount({ ai_model_rate: ["read"] });

    // Awaited on the sibling's rows, which land after /me and therefore after
    // the withheld notice: an assertion that stopped at the notice would judge
    // the FX request log while the model list was still in flight.
    expect(await screen.findByText("claude-opus-4-8")).toBeTruthy();
    expect(
      screen.getByText(/only an admin or ops can see the currency rates/i),
    ).toBeTruthy();
    const fx = rateCard("Currency rates");
    expect(within(fx).queryByText("USD")).toBeNull();
    // Not the table with an empty body either: the withheld card draws no
    // sheet at all, so the row that names one is absent with it.
    expect(within(fx).queryByText("Rates in force")).toBeNull();
    // One explanation, not two: the read-only caption belongs to a sheet the
    // reader may actually read.
    expect(within(fx).queryByText(/read-only view/i)).toBeNull();
    expect(urls.some((url) => url.includes("/v1/fx-rates"))).toBe(false);

    // The model sheet is unaffected — the grants are per object, and a card that
    // read the wrong one would fail here rather than pass by symmetry.
    const model = rateCard("AI model costs");
    expect(within(model).getByText("claude-opus-4-8")).toBeTruthy();
    expect(urls.some((url) => url.includes("/v1/ai-model-rates"))).toBe(true);
  });

  it("withholds the model sheet without ai_model_rate:read and requests no rates", async () => {
    const { urls } = mount({ fx_rate: ["read"] });

    // Awaited on the readable sibling, for the same reason as above.
    expect(await screen.findByText("USD")).toBeTruthy();
    expect(
      screen.getByText(/only an admin or ops can see what each model costs/i),
    ).toBeTruthy();
    const model = rateCard("AI model costs");
    expect(within(model).queryByText("claude-opus-4-8")).toBeNull();
    expect(within(model).queryByText("Prices in force")).toBeNull();
    expect(within(model).queryByText(/read-only view/i)).toBeNull();
    expect(urls.some((url) => url.includes("/v1/ai-model-rates"))).toBe(false);

    const fx = rateCard("Currency rates");
    expect(within(fx).getByText("USD")).toBeTruthy();
    expect(urls.some((url) => url.includes("/v1/fx-rates"))).toBe(true);
  });

  // The read SEAT is a write ceiling, not a read one (A62/ADR-0047): the server
  // clamps it on the HTTP method, so these grants still read both sheets. The
  // card must therefore reach for the read grant alone when it decides whether
  // to withhold — telling an admin looking at the table that only an admin may
  // see it would be a lie about which permission ran out.
  it("keeps both sheets readable for a rate-setter on a read seat", async () => {
    mount(RATE_SETTER, "read");
    await waitFor(() => expect(screen.getByText("USD")).toBeTruthy());
    expect(screen.getByText("claude-opus-4-8")).toBeTruthy();

    expect(screen.queryByText(/only an admin or ops can see/i)).toBeNull();
    for (const title of ["Currency rates", "AI model costs"]) {
      const card = rateCard(title);
      expect(within(card).getByText(/read-only view/i)).toBeTruthy();
    }
    expect(screen.queryByRole("button", { name: "Set rate" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Add model rate" })).toBeNull();
  });
});
