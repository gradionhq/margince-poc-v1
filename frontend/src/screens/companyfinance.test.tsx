/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render as rtlRender, screen } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { components } from "../api/schema";
import { LocaleProvider } from "../i18n";
import { CompanyFinanceCard } from "./companyfinance";

// FIN-AC-3 decides WHETHER this card exists, and it is the one rule on the
// card that fails silently: a lifecycle wrongly treated as never-invoiced
// renders nothing at all, so a reader is never told the money is missing.

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

beforeEach(() => {
  globalThis.localStorage.setItem("margince.workspaceSlug", "acme");
});

type FinanceSummary = components["schemas"]["OrganizationFinanceSummary"];

const CONNECTED: FinanceSummary = {
  organization_id: "o-1",
  state: "connected",
  provider: "offline_demo",
  net_invoiced: { amount_minor: 18642000, currency: "EUR" },
};

function stub(summary: FinanceSummary = CONNECTED) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (request: Request) => {
      const body = new URL(request.url).pathname.endsWith("/finance-summary")
        ? summary
        : { data: [], page: { has_more: false, next_cursor: null } };
      return new Response(JSON.stringify(body), {
        status: 200,
        headers: { "content-type": "application/json" },
      });
    }),
  );
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

describe("the finance card is absent only where FIN-AC-3 says so", () => {
  // The criterion names exactly three, and the card must not add to them.
  it.each(["target", "prospect", "opportunity"])(
    "draws nothing for a %s, which has never been invoiced",
    (lifecycle) => {
      stub();
      const { container } = render(
        <CompanyFinanceCard orgId="o-1" lifecycle={lifecycle} />,
      );
      expect(container.textContent).toBe("");
    },
  );

  // The overreach this suite exists for. Every IMPORTED company carries
  // `unknown`, so treating it as never-invoiced hid finance from the majority
  // of the book — and hid it silently, because the card simply did not render.
  it.each(["unknown", "disqualified", "customer", "former_customer"])(
    "renders for a %s, which may have been invoiced",
    async (lifecycle) => {
      stub();
      render(<CompanyFinanceCard orgId="o-1" lifecycle={lifecycle} />);
      // A figure only the LOADED body draws. The card's title renders on the
      // loading skeleton too, so asserting on it would pass before the read
      // lands — and would then prove only that the lifecycle set said no,
      // which is the set's own truth table restated.
      expect(await screen.findByText("€186,420.00")).toBeTruthy();
    },
  );

  // An account with no lifecycle recorded yet is not an account we know we
  // never billed.
  it("renders when the lifecycle is not known", async () => {
    stub();
    render(<CompanyFinanceCard orgId="o-1" />);
    expect(await screen.findByText("€186,420.00")).toBeTruthy();
  });

  // FIN-AC-3's second half: a former customer's figures are history, and the
  // card says so rather than letting money from a finished relationship read
  // as current.
  it("labels a former customer's money historical", async () => {
    stub();
    render(<CompanyFinanceCard orgId="o-1" lifecycle="former_customer" />);
    expect(await screen.findByText("Finance · historical")).toBeTruthy();
  });

  // The label belongs to every state the card can be in, not only the loaded
  // one. `error` matters most: it keeps showing the last good figures, so a
  // title reading "Finance" there puts money from a finished relationship
  // under a heading that claims it is current.
  it("keeps the historical label while the read is still in flight", () => {
    stub();
    render(<CompanyFinanceCard orgId="o-1" lifecycle="former_customer" />);
    expect(screen.getByText("Finance · historical")).toBeTruthy();
  });

  it("leaves a current customer's card unqualified", async () => {
    stub();
    render(<CompanyFinanceCard orgId="o-1" lifecycle="customer" />);
    expect(await screen.findByText("€186,420.00")).toBeTruthy();
    expect(screen.getByText("Finance")).toBeTruthy();
    expect(screen.queryByText("Finance · historical")).toBeNull();
  });
});
