/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render as rtlRender, screen } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it } from "vitest";
import type { components } from "../api/schema";
import { MONEY_ABSENT } from "../format/format";
import { LocaleProvider } from "../i18n";
import { CommercialPanel, DealsCard } from "./company360";

// Money on this page is an integer minor amount PLUS its ISO currency, and the
// wire may carry either half alone. Neither absence has a safe default, and the
// two failures are not symmetric:
//
//   - a missing amount rendered as 0 states a figure the server never sent, and
//     "€0 won" reads as a settled account rather than as an unknown one;
//   - a missing currency has no rendering at all — Intl.NumberFormat throws a
//     RangeError on an empty currency code, so a page that reaches for one takes
//     the whole record down, navigation rail included.
//
// Both readings therefore go through formatMoneyOrAbsent, and both suites below
// mount the panels directly: the account page renders them from a composite read
// whose deals section a stub would have to fake anyway.

type Organization360 = components["schemas"]["Organization360"];
type Organization360Deal = components["schemas"]["Organization360Deal"];
type Organization360Deals = components["schemas"]["Organization360Deals"];
type Money = components["schemas"]["Money"];

const PAGE = { has_more: false, next_cursor: null };

const ORG: components["schemas"]["Organization"] = {
  id: "o-1",
  display_name: "Brandt Automotive GmbH",
  source: "manual",
  captured_by: "human:u-1",
  created_at: "2026-06-01T08:00:00Z",
  updated_at: "2026-06-01T08:00:00Z",
};

// A deal the server priced but never labelled: the amount is real, the currency
// is not there. The database pairs the two columns today, which is exactly why
// the contract's nullable currency is worth pinning — the crash arrives with the
// first source that does not.
const UNLABELLED_DEAL: Organization360Deal = {
  deal_id: "d-1",
  name: "Retrofit rollout",
  status: "open",
  stalled: false,
  amount: { amount_minor: 1_250_000, currency: null },
};

function dealsSection(
  wonLifetime: Money,
  data: Organization360Deal[] = [],
): Organization360Deals {
  return { data, page: PAGE, won_lifetime: wonLifetime, lost_count: 0 };
}

function view(deals: Organization360Deals): Organization360 {
  return {
    as_of: "2026-06-01T09:00:00Z",
    organization: ORG,
    sections_omitted: [],
    deals,
  };
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

afterEach(cleanup);

describe("DealsCard — a money reading with one half missing", () => {
  it("renders an unlabelled lifetime total as an absence instead of dying", () => {
    render(
      <DealsCard
        view={view(dealsSection({ amount_minor: 4_200_000, currency: null }))}
      />,
    );

    expect(screen.getByText(`Won to date ${MONEY_ABSENT}`)).toBeTruthy();
  });

  it("never states a zero for a lifetime total the server did not compute", () => {
    render(
      <DealsCard
        view={view(dealsSection({ amount_minor: null, currency: "EUR" }))}
      />,
    );

    expect(screen.getByText(`Won to date ${MONEY_ABSENT}`)).toBeTruthy();
    expect(screen.queryByText(/€\s?0/)).toBeNull();
  });

  it("keeps a priced deal's row when its amount carries no currency", () => {
    render(
      <DealsCard
        view={view(
          dealsSection({ amount_minor: 1, currency: "EUR" }, [UNLABELLED_DEAL]),
        )}
      />,
    );

    expect(screen.getByText("Retrofit rollout")).toBeTruthy();
    expect(screen.getByText(MONEY_ABSENT)).toBeTruthy();
  });
});

describe("CommercialPanel — the same two readings, one card up", () => {
  it("holds the lifetime slot with an absence rather than a fabricated total", () => {
    render(
      <CommercialPanel
        view={view(
          dealsSection({ amount_minor: 4_200_000, currency: null }, [
            UNLABELLED_DEAL,
          ]),
        )}
      />,
    );

    // The label keeps its slot, so the reader sees WHICH figure is missing —
    // and the deal row beside it is the second reading of the same field.
    expect(screen.getByText("Won to date")).toBeTruthy();
    expect(screen.getAllByText(MONEY_ABSENT).length).toBe(2);
  });
});
