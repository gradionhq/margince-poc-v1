/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render as rtlRender, screen } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { LocaleProvider } from "../i18n";
import { OfferScreen } from "./offers";

// Task 2.3: the offer 360 skeleton — header (offer_number/revision/status/
// back-to-deal), read-only server-truth totals, and a draft-only header edit
// affordance (absent from the DOM entirely once the offer leaves draft, not
// merely disabled — AC honesty rule).

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

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

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

const baseOffer = {
  id: "o-1",
  workspace_id: "w",
  deal_id: "d-1",
  offer_number: "ANG-2026-0007",
  revision: 2,
  status: "draft" as const,
  currency: "EUR",
  buyer_org_id: null,
  valid_until: "2026-08-01",
  intro_text: null,
  terms_text: null,
  net_minor: 100000,
  tax_minor: 19000,
  gross_minor: 119000,
  template_id: null,
  line_items: [],
  source: "manual",
  captured_by: "human:u1",
  version: 3,
  created_at: "2026-06-01T08:00:00Z",
  updated_at: "2026-06-01T08:00:00Z",
};

function stubOffer(offer: Record<string, unknown>) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input instanceof Request ? input.url : input);
      if (url.includes("/offers/")) {
        return jsonResponse(offer);
      }
      return jsonResponse({ data: [], page: { has_more: false } });
    }),
  );
}

describe("OfferScreen", () => {
  it("renders the header, status badge, and read-only totals", async () => {
    stubOffer(baseOffer);
    render(<OfferScreen id="o-1" />);
    expect(await screen.findByText("ANG-2026-0007")).toBeTruthy();
    expect(screen.getByText("Revision 2")).toBeTruthy();
    expect(screen.getByText("draft")).toBeTruthy();
    // 100000 minor EUR net, 19000 tax, 119000 gross (en-GB Intl formatting).
    expect(screen.getByText("€1,000.00")).toBeTruthy();
    expect(screen.getByText("€190.00")).toBeTruthy();
    expect(screen.getByText("€1,190.00")).toBeTruthy();
  });

  it("shows the draft-only edit affordance when the offer is a draft", async () => {
    stubOffer(baseOffer);
    render(<OfferScreen id="o-1" />);
    await screen.findByText("ANG-2026-0007");
    expect(screen.getByTestId("edit-offer-header")).toBeTruthy();
  });

  it("omits the edit affordance entirely once the offer is sent", async () => {
    stubOffer({ ...baseOffer, status: "sent" });
    render(<OfferScreen id="o-1" />);
    await screen.findByText("ANG-2026-0007");
    expect(screen.queryByTestId("edit-offer-header")).toBeNull();
  });
});
