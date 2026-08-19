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
type FinanceInvoice = components["schemas"]["FinanceInvoice"];

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

describe("how late an invoice was, counted in whole days", () => {
  const invoice = (
    id: string,
    status: FinanceInvoice["status"],
    daysLate: number,
  ): FinanceInvoice => ({
    id,
    number: id,
    issued_at: "2026-07-01",
    due_at: "2026-07-31",
    status,
    currency: "EUR",
    gross_minor: 100_000,
    open_minor: status === "paid" ? 0 : 100_000,
    days_late: daysLate,
  });

  function withInvoices(invoices: FinanceInvoice[]) {
    stub({ ...CONNECTED, recent_invoices: invoices });
    render(<CompanyFinanceCard orgId="o-1" lifecycle="customer" />);
  }

  it("says one day late in the singular, and the rest in the plural", async () => {
    withInvoices([invoice("INV-1", "paid", 1), invoice("INV-2", "paid", 8)]);
    expect(await screen.findByText("Paid 1 day late")).toBeTruthy();
    expect(screen.getByText("Paid 8 days late")).toBeTruthy();
  });

  it("counts an unpaid invoice's lateness the same way", async () => {
    withInvoices([invoice("INV-3", "open", 1), invoice("INV-4", "open", 3)]);
    expect(await screen.findByText("1 day overdue")).toBeTruthy();
    expect(screen.getByText("3 days overdue")).toBeTruthy();
  });
});

describe("the two readings the card draws as shapes", () => {
  function withSummary(patch: Partial<FinanceSummary>) {
    stub({ ...CONNECTED, ...patch });
    render(<CompanyFinanceCard orgId="o-1" lifecycle="customer" />);
  }

  it("draws the payment habit's shape, and states the median beside the money", async () => {
    withSummary({ median_days_after_due: 4, payment_behaviour: [1, 3, 9] });
    expect(await screen.findByText("Typically 4 days after due.")).toBeTruthy();
    expect(
      screen.getByLabelText("Days late per settled invoice, oldest first"),
    ).toBeTruthy();
  });

  it("draws no line from a single settled invoice, and still states the median", async () => {
    withSummary({ median_days_after_due: 4, payment_behaviour: [1] });
    expect(await screen.findByText("Typically 4 days after due.")).toBeTruthy();
    expect(
      screen.queryByLabelText("Days late per settled invoice, oldest first"),
    ).toBeNull();
  });

  it("draws overdue against open, and names both halves of the bar", async () => {
    withSummary({
      open_balance: { amount_minor: 400_000, currency: "EUR" },
      overdue: { amount_minor: 100_000, currency: "EUR" },
    });
    expect(await screen.findByText("25% of everything open.")).toBeTruthy();
    expect(screen.getByText("Overdue €1,000.00")).toBeTruthy();
    expect(screen.getByText("Open €4,000.00")).toBeTruthy();
    const meter = screen.getByLabelText("Overdue share of the open balance");
    expect(meter.getAttribute("aria-valuenow")).toBe("100000");
    expect(meter.getAttribute("aria-valuemax")).toBe("400000");
  });

  it("states both clauses as one lede when the panel has both", async () => {
    withSummary({
      median_days_after_due: 26,
      open_balance: { amount_minor: 400_000, currency: "EUR" },
      overdue: { amount_minor: 100_000, currency: "EUR" },
    });
    expect(
      await screen.findByText(
        "25% of everything open. Typically 26 days after due.",
      ),
    ).toBeTruthy();
  });

  it("draws no bar when the two halves are in different currencies", async () => {
    withSummary({
      open_balance: { amount_minor: 400_000, currency: "EUR" },
      overdue: { amount_minor: 100_000, currency: "USD" },
    });
    expect(await screen.findByText("€186,420.00")).toBeTruthy();
    expect(
      screen.queryByLabelText("Overdue share of the open balance"),
    ).toBeNull();
  });

  it("draws no bar when nothing is open to be a share of", async () => {
    withSummary({
      open_balance: { amount_minor: 0, currency: "EUR" },
      overdue: { amount_minor: 0, currency: "EUR" },
    });
    expect(await screen.findByText("€186,420.00")).toBeTruthy();
    expect(
      screen.queryByLabelText("Overdue share of the open balance"),
    ).toBeNull();
  });
});
