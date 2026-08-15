import { describe, expect, it } from "vitest";
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
    );

    expect(values).toHaveLength(1);
    expect(values[0]).toMatch(/\/ a$/);
  });

  it("draws nothing when no agreement carries a convertible figure", () => {
    // Null, not zero: an account whose agreements have no priced value is not
    // an account under contract for nothing.
    expect(
      contractValues({ active_count: 3, cancellation_pending: false }, "en"),
    ).toHaveLength(0);
  });
});
