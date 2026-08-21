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
import { PartnerDeals } from "./partnerdeals";

// The sourced-deals panel on a partner's company page. These deals belong to
// the CUSTOMERS, so the account's own Deals tab is silent about them and this
// is the only surface a reader can see a partner's pipeline on.

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

function stubDeals(deals: unknown[]) {
  const urls: string[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (request: Request) => {
      urls.push(request.url);
      const body = request.url.includes("/organizations/")
        ? { id: "cust-1", display_name: "Northgate GmbH" }
        : { data: deals, page: { has_more: false } };
      return new Response(JSON.stringify(body), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      });
    }),
  );
  return urls;
}

const sourced = {
  id: "d-1",
  name: "Northgate rollout",
  organization_id: "cust-1",
  partner_org_id: "o-1",
  partner_attribution: "sourced",
  amount_minor: 4800000,
  currency: "EUR",
  status: "open",
  pipeline_id: "p-1",
  stage_id: "s-1",
  version: 1,
  created_at: "2026-08-01T00:00:00Z",
  updated_at: "2026-08-01T00:00:00Z",
};

describe("the deals a partner brought", () => {
  it("reads only the deals attributed to this partner", async () => {
    const urls = stubDeals([sourced]);

    render(<PartnerDeals organizationId="o-1" />);
    await screen.findByTestId("partner-deals");

    const listed = urls.find((url) => url.includes("/deals?"));
    expect(new URL(listed ?? "").searchParams.get("partner_org_id")).toBe(
      "o-1",
    );
  });

  // The point of the whole panel: a partner brings a deal FOR another company,
  // so a row that does not name the customer is the confusing half of the
  // arrangement — the reading that made a partner look like its own client.
  it("names the customer the deal was brought for, and links to it", async () => {
    stubDeals([sourced]);

    render(<PartnerDeals organizationId="o-1" />);
    const panel = await screen.findByTestId("partner-deals");

    // The design system routes with a button, not an anchor.
    expect(
      await within(panel).findByRole("button", { name: "Northgate GmbH" }),
    ).toBeTruthy();
    expect(
      within(panel).getByRole("button", { name: "Northgate rollout" }),
    ).toBeTruthy();
  });

  it("shows the deal value and what the partner's part was", async () => {
    stubDeals([sourced]);

    render(<PartnerDeals organizationId="o-1" />);
    const panel = await screen.findByTestId("partner-deals");
    const cells = [...panel.querySelectorAll("tbody tr td")].map(
      (cell) => cell.textContent,
    );

    expect(cells[2]).toBe("Brought us this deal (earns commission)");
    expect(cells[3]).toBe("€48,000.00");
    expect(cells[4]).toContain("open");
  });

  // A partner's page should show the deals they helped with too, not only the
  // ones that earn them money — the commission ledger below already answers
  // the money question, and it answers it for sourced deals alone.
  it("lists an influenced deal beside a sourced one", async () => {
    stubDeals([
      sourced,
      {
        ...sourced,
        id: "d-2",
        name: "Hanoi expansion",
        partner_attribution: "influenced",
      },
    ]);

    render(<PartnerDeals organizationId="o-1" />);
    const panel = await screen.findByTestId("partner-deals");
    const rows = [...panel.querySelectorAll("tbody tr")];

    expect(rows).toHaveLength(2);
    expect(rows[1]?.textContent).toContain(
      "Helped on a deal we already had (no commission)",
    );
  });

  // Stopping at page one under-reports a productive partner silently.
  it("follows the cursor rather than showing the first page as all of them", async () => {
    const pages = [
      { data: [sourced], page: { has_more: true, next_cursor: "page-2" } },
      { data: [{ ...sourced, id: "d-2" }], page: { has_more: false } },
    ];
    let call = 0;
    const urls: string[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn(async (request: Request) => {
        if (request.url.includes("/organizations/")) {
          return new Response(
            JSON.stringify({ id: "cust-1", display_name: "Northgate GmbH" }),
            { status: 200, headers: { "Content-Type": "application/json" } },
          );
        }
        urls.push(request.url);
        const body = pages[Math.min(call, pages.length - 1)];
        call += 1;
        return new Response(JSON.stringify(body), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        });
      }),
    );

    render(<PartnerDeals organizationId="o-1" />);
    const panel = await screen.findByTestId("partner-deals");

    expect([...panel.querySelectorAll("tbody tr")]).toHaveLength(2);
    expect(new URL(urls[1] ?? "").searchParams.get("cursor")).toBe("page-2");
  });

  it("says nothing was brought in rather than showing an empty table", async () => {
    stubDeals([]);

    render(<PartnerDeals organizationId="o-1" />);

    expect(await screen.findByText("No deals brought in yet")).toBeTruthy();
    expect(screen.queryByTestId("partner-deals")).toBeNull();
  });
});
