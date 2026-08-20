/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render as rtlRender, screen } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { LocaleProvider } from "../i18n";
import { formatRate, PartnerCommissions } from "./partnercommissions";

// The commission panel on a partner's company page: what the margin tier one
// card up has actually produced. A tier shown without the money it earned is a
// number nobody can check, which is the whole reason this panel exists.

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

function stubCommissions(entries: unknown[]) {
  const urls: string[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (request: Request) => {
      urls.push(request.url);
      return new Response(JSON.stringify({ data: entries, page: {} }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      });
    }),
  );
  return urls;
}

const accrued = {
  id: "c-1",
  deal_id: "d-1",
  partner_org_id: "o-1",
  status: "accrued",
  attribution_at_accrual: "sourced",
  margin_tier_at_accrual: "tier2_20",
  rate_bps: 2000,
  basis_amount_minor: 100000,
  currency: "EUR",
  amount_minor: 20000,
  captured_by: "human:x",
  version: 1,
  created_at: "2026-08-01T00:00:00Z",
  updated_at: "2026-08-01T00:00:00Z",
};

describe("the commission panel", () => {
  it("reads only this partner's entries", async () => {
    const urls = stubCommissions([accrued]);

    render(<PartnerCommissions organizationId="o-1" />);
    await screen.findByTestId("commission-ledger");

    expect(urls[0]).toContain("partner_org_id=o-1");
  });

  it("shows what was earned, on what, and at which rate", async () => {
    stubCommissions([accrued]);

    render(<PartnerCommissions organizationId="o-1" />);
    const row = await screen.findByTestId("commission-row");

    // 20% of a €1,000 deal. The rate is the tier a human agreed to, not the
    // basis points the row stores it as.
    expect(row.textContent).toContain("€200.00");
    expect(row.textContent).toContain("20%");
    expect(row.textContent).toContain("€1,000.00");
    expect(row.textContent).toContain("Accrued");
  });

  // A reversal keeps its own row rather than being folded into the entry it
  // cancels: a partner asking "what happened to that one" needs both halves.
  it("keeps a reversed entry visible beside the one it cancels", async () => {
    stubCommissions([
      accrued,
      {
        ...accrued,
        id: "c-2",
        status: "void",
        reversal_of: "c-1",
        void_reason: "the deal was reopened",
      },
    ]);

    render(<PartnerCommissions organizationId="o-1" />);
    const rows = await screen.findAllByTestId("commission-row");

    expect(rows).toHaveLength(2);
    expect(rows[1]?.textContent).toContain("Reversed");
  });

  it("says nothing is earned rather than showing an empty table", async () => {
    stubCommissions([]);

    render(<PartnerCommissions organizationId="o-1" />);

    expect(await screen.findByText("Nothing earned yet")).toBeTruthy();
    expect(screen.queryByTestId("commission-ledger")).toBeNull();
  });
});

describe("formatRate", () => {
  it("renders whole-percent tiers without trailing zeros", () => {
    expect(formatRate(1500)).toBe("15%");
    expect(formatRate(2000)).toBe("20%");
    expect(formatRate(2500)).toBe("25%");
  });

  it("keeps a fraction that a tier genuinely carries", () => {
    expect(formatRate(1250)).toBe("12.50%");
  });
});
