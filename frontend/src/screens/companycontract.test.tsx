import { describe, expect, it } from "vitest";
import type { components } from "../api/schema";

type Contract = components["schemas"]["Contract"];

import { contractValues } from "./companycommercial";

// The rule the server and this card both hold: a three-year total and a
// per-year figure describe different spans, so they are never added into one
// number. A card that summed them would hand the reader a figure they would
// misuse, and nothing on the page would reveal the error.
describe("contractValues", () => {
  it("keeps the two bases apart rather than summing them", () => {
    const values = contractValues(
      {
        active_count: 2,
        cancellation_pending: false,
        base_currency: "EUR",
        total_basis_value_minor_base: 30_000_000,
        annualized_value_minor_base: 12_000_000,
      },
      "en",
      (amount) => `${amount} / year`,
    );

    expect(values).toHaveLength(2);
    // 300k total and 120k a year — never one 420k figure.
    expect(values.join(" ")).toContain("300,000");
    expect(values.join(" ")).toContain("120,000");
    expect(values.join(" ")).not.toContain("420,000");
  });

  it("marks the annualized figure as per-year so it cannot read as a total", () => {
    const values = contractValues(
      {
        active_count: 1,
        cancellation_pending: false,
        base_currency: "EUR",
        annualized_value_minor_base: 12_000_000,
      },
      "en",
      (amount) => `${amount} / year`,
    );

    expect(values).toHaveLength(1);
    expect(values[0]).toMatch(/\/ year$/);
  });

  it("draws nothing when no agreement carries a convertible figure", () => {
    // Null, not zero: an account whose agreements have no priced value is not
    // an account under contract for nothing.
    expect(
      contractValues(
        { active_count: 3, cancellation_pending: false },
        "en",
        (amount) => `${amount} / year`,
      ),
    ).toHaveLength(0);
  });
});

import { basisLabel, contractAmount } from "./companycontracts";

// A contract as the wire actually carries it: every server-owned field present,
// so a fixture cannot pass a check the real payload would fail.
function contractRow(
  over: Partial<Contract> & Pick<Contract, "value_basis">,
): Contract {
  return {
    id: "c-1",
    organization_id: "o-1",
    title: "Agreement",
    source: "manual",
    captured_by: "human:00000000-0000-0000-0000-000000000001",
    status: "active",
    under_contract: true,
    auto_renew: false,
    version: 1,
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
    ...over,
  };
}

// A row's value must say which KIND of figure it is. A reader who cannot tell a
// three-year total from a per-year figure has been handed a number they will
// misuse, and the row is the last place that distinction can be drawn.
describe("a contract's value", () => {
  it("names the basis an annualized figure is stated on", () => {
    const annual = basisLabel(
      contractRow({
        value_basis: "annualized_12m",
        value_minor: 12_000_000,
        currency: "EUR",
      }),
    );
    const total = basisLabel(
      contractRow({
        value_basis: "total",
        value_minor: 30_000_000,
        currency: "EUR",
      }),
    );

    // Two figures of the same size mean different money, and the row is the
    // last place that can be said.
    expect(annual).toBe("contracts.value.perYear");
    expect(total).toBe("contracts.value.total");
  });

  it("draws nothing rather than a bare number when the currency is missing", () => {
    // Half a money pair cannot be rendered: an amount with no currency is a
    // figure the reader would supply their own units for — and a basis with no
    // figure to qualify is a caption under an empty cell.
    const half = contractRow({ value_basis: "total", value_minor: 5_000 });
    expect(contractAmount(half, "en")).toBe("");
    expect(basisLabel(half)).toBe("");
  });
});
