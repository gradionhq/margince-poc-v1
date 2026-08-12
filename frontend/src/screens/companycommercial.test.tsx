/** @vitest-environment jsdom */
import { describe, expect, it } from "vitest";
import { leadingDeal, offerAmount } from "./companycommercial";

// leadingDeal picks the ONE deal the "last offer" reading names as this
// account's leading opportunity. A wrong pick here is a wrong number
// presented as a fact about the account, so each case below pins a refusal
// or a pick on its own terms rather than through the rendered component —
// the selection rule is the thing under test, not the query/RBAC wiring
// around it.

const deal = (
  id: string,
  amountMinor: number | null,
  currency: string | null = "EUR",
) => ({
  deal_id: id,
  name: `Deal ${id}`,
  status: "open" as const,
  stalled: false,
  amount:
    amountMinor == null
      ? undefined
      : { amount_minor: amountMinor, currency: currency ?? undefined },
});

describe("leadingDeal — the account's own leading opportunity", () => {
  it("names the biggest deal when every deal shares one currency and none is truncated", () => {
    const picked = leadingDeal(
      [deal("d-1", 10_000), deal("d-2", 25_000), deal("d-3", 5_000)],
      false,
    );
    expect(picked?.deal_id).toBe("d-2");
  });

  it("a mixed-currency set names no leading deal", () => {
    // 100 JPY and 100 EUR are not comparable amounts; picking either implies
    // they are the same kind of number.
    const picked = leadingDeal(
      [deal("d-1", 100, "JPY"), deal("d-2", 100, "EUR")],
      false,
    );
    expect(picked).toBeUndefined();
  });

  it("an unpriced deal never leads", () => {
    // Absence of an amount is not an amount of zero: an unpriced deal must
    // never outrank a deal that actually carries a figure, and must never be
    // picked over one either.
    const picked = leadingDeal([deal("d-1", null), deal("d-2", 5_000)], false);
    expect(picked?.deal_id).toBe("d-2");
  });

  it("breaks a tie on id when every deal is equally unpriced", () => {
    // Nothing here claims an AMOUNT — LastOffer names the deal, not its
    // price — so a set with no priced deal to prefer still names one,
    // deterministically, the same way the amount tiebreak does.
    const picked = leadingDeal([deal("d-2", null), deal("d-1", null)], false);
    expect(picked?.deal_id).toBe("d-1");
  });

  it("a truncated page names no leading deal", () => {
    // The largest of what the 360 happened to fetch is not the largest deal
    // on the account — the page is capped, and the real largest may be off
    // the end of it.
    const picked = leadingDeal(
      [deal("d-1", 10_000), deal("d-2", 25_000)],
      true,
    );
    expect(picked).toBeUndefined();
  });

  it("an empty deals list names no leading deal", () => {
    expect(leadingDeal([], false)).toBeUndefined();
  });

  it("breaks a tie on id, so equal deals do not swap between renders", () => {
    const picked = leadingDeal(
      [deal("d-2", 10_000), deal("d-1", 10_000)],
      false,
    );
    expect(picked?.deal_id).toBe("d-1");
  });
});

describe("offerAmount — the last offer's own figure", () => {
  it("says nothing rather than a guessed symbol when the gross has no currency", () => {
    expect(
      offerAmount({ gross_minor: 50_000, currency: undefined } as never, "en"),
    ).toBe("—");
  });

  it("says nothing rather than a zero when the offer carries no gross amount", () => {
    expect(
      offerAmount({ gross_minor: undefined, currency: "EUR" } as never, "en"),
    ).toBe("—");
  });

  it("names the gross, not the net, so the figure matches what the buyer saw", () => {
    expect(
      offerAmount({ gross_minor: 119_000, currency: "EUR" } as never, "en"),
    ).toBe("€1,190.00");
  });
});
