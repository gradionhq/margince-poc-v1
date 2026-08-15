import { describe, expect, it } from "vitest";
import { contractBody, draftProblem } from "./contractform";

const DRAFT = {
  title: "MSA 2026",
  contractNumber: "",
  valueMinor: 0,
  currency: "EUR",
  valueBasis: "total" as const,
  startsOn: "",
  endsOn: "",
  renewalOn: "",
  noticePeriodDays: "",
  signedOn: "",
};

describe("contractBody", () => {
  it("omits an unanswered field rather than sending an empty one", () => {
    // "Not recorded" and "recorded as nothing" are different facts about an
    // agreement, and an empty string would be the second wearing the first's
    // clothes.
    const body = contractBody("org-1", DRAFT);

    expect(body).not.toHaveProperty("starts_on");
    expect(body).not.toHaveProperty("signed_on");
    expect(body).not.toHaveProperty("contract_number");
    expect(body.title).toBe("MSA 2026");
  });

  it("sends value and currency together or not at all", () => {
    // Half a money pair cannot be converted, and the server refuses it. The
    // form must not manufacture the refusal by sending one side.
    const unpriced = contractBody("org-1", DRAFT);
    expect(unpriced).not.toHaveProperty("value_minor");
    expect(unpriced).not.toHaveProperty("currency");

    const priced = contractBody("org-1", { ...DRAFT, valueMinor: 12_000_000 });
    expect(priced.value_minor).toBe(12_000_000);
    expect(priced.currency).toBe("EUR");
  });

  it("never invents a signed date", () => {
    // The whole point of the field: a date the form supplied would be
    // indistinguishable from one a human asserted, the moment it was saved.
    expect(contractBody("org-1", DRAFT)).not.toHaveProperty("signed_on");
  });

  it("carries the value basis, because it changes what the amount means", () => {
    const annual = contractBody("org-1", {
      ...DRAFT,
      valueMinor: 12_000_000,
      valueBasis: "annualized_12m",
    });
    expect(annual.value_basis).toBe("annualized_12m");
  });
});

describe("draftProblem", () => {
  it("refuses an agreement with no title", () => {
    expect(draftProblem({ ...DRAFT, title: "   " })).toBe(
      "contracts.form.errNoName",
    );
  });

  it("refuses a term that ends before it starts", () => {
    expect(
      draftProblem({ ...DRAFT, startsOn: "2026-06-30", endsOn: "2026-01-01" }),
    ).toBe("contracts.form.errTermOrder");
  });

  it("accepts an open-ended term, which is a real shape and not a gap", () => {
    expect(draftProblem({ ...DRAFT, startsOn: "2026-01-01" })).toBeNull();
  });
});
