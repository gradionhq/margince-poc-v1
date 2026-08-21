/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  cleanup,
  render as rtlRender,
  screen,
  within,
} from "@testing-library/react";
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

    expect(new URL(urls[0] ?? "").searchParams.get("partner_org_id")).toBe(
      "o-1",
    );
  });

  it("shows what was earned, on what, and at which rate", async () => {
    stubCommissions([accrued]);

    render(<PartnerCommissions organizationId="o-1" />);
    const ledger = await screen.findByTestId("commission-ledger");
    const cells = [...ledger.querySelectorAll("tbody tr td")].map(
      (c) => c.textContent,
    );

    // 20% of a €1,000 deal, read per cell: asserting the row as one string
    // would pass just as happily with the earned and basis figures swapped.
    // The deal leads, because an entry's first question is "on what?".
    expect(cells[1]).toBe("€200.00");
    // The rate is the tier a human agreed to, not the basis points stored.
    expect(cells[2]).toBe("20%");
    expect(cells[3]).toBe("€1,000.00");
    expect(cells[4]).toContain("Accrued");
  });

  // A ledger of bare figures cannot be reconciled against anything: the entry
  // has to say which deal produced it, and let a reader open that deal.
  it("names the deal an entry was earned on, and links to it", async () => {
    // EntityRef resolves the deal's own name, so the stub has to answer that
    // read too — a reference it cannot name is deliberately not a link.
    vi.stubGlobal(
      "fetch",
      vi.fn(async (request: Request) => {
        const body = request.url.includes("/deals/")
          ? { id: "d-1", name: "Northgate rollout" }
          : { data: [accrued], page: { has_more: false } };
        return new Response(JSON.stringify(body), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        });
      }),
    );

    render(<PartnerCommissions organizationId="o-1" />);
    const ledger = await screen.findByTestId("commission-ledger");

    // The control the design system routes with is a button, not an anchor.
    const link = await within(ledger).findByRole("button", {
      name: "Northgate rollout",
    });
    expect(link).toBeTruthy();
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
    const ledger = await screen.findByTestId("commission-ledger");
    const rows = [...ledger.querySelectorAll("tbody tr")];

    expect(rows).toHaveLength(2);
    expect(rows[1]?.textContent).toContain("Reversed");
  });

  // Reading page one and stopping would under-report what a partner earned,
  // silently, which is the worst way for a money figure to be wrong.
  it("follows the cursor rather than showing the first page as the whole ledger", async () => {
    const pages = [
      { data: [accrued], page: { has_more: true, next_cursor: "page-2" } },
      { data: [{ ...accrued, id: "c-2" }], page: { has_more: false } },
    ];
    let call = 0;
    const urls: string[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn(async (request: Request) => {
        urls.push(request.url);
        const body = pages[Math.min(call, pages.length - 1)];
        call += 1;
        return new Response(JSON.stringify(body), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        });
      }),
    );

    render(<PartnerCommissions organizationId="o-1" />);
    const ledger = await screen.findByTestId("commission-ledger");
    const rows = [...ledger.querySelectorAll("tbody tr")];

    expect(rows).toHaveLength(2);
    expect(new URL(urls[1] ?? "").searchParams.get("cursor")).toBe("page-2");
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
    expect(formatRate(1500, "en")).toBe("15%");
    expect(formatRate(2000, "en")).toBe("20%");
    expect(formatRate(2500, "en")).toBe("25%");
  });

  it("keeps a fraction that a tier genuinely carries", () => {
    expect(formatRate(1250, "en")).toBe("12.5%");
  });

  // A German reader writes "12,5 %". A hand-built string would hand them a
  // decimal point, which is the kind of wrong that reads as a different number.
  it("writes the separator the reader's locale uses", () => {
    expect(formatRate(1250, "de")).toContain(",");
  });
});
