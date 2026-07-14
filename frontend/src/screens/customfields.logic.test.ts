import { describe, expect, it } from "vitest";
import {
  apiKey,
  columnName,
  ddlPreview,
  looksStructural,
  slug,
} from "./customfields.logic";

describe("custom-fields logic", () => {
  it("slugs a label to a snake_case identifier", () => {
    expect(slug("Contract end date")).toBe("contract_end_date");
    expect(slug("  Budget  ceiling! ")).toBe("budget_ceiling");
    expect(slug("")).toBe("");
  });

  it("derives the immutable cf_-prefixed column and api key", () => {
    expect(columnName("Contract end date")).toBe("cf_contract_end_date");
    expect(columnName("")).toBe("cf_…");
    expect(apiKey("organization", "Contract end date")).toBe(
      "organization.cf_contract_end_date",
    );
  });

  it("renders the pending DDL per object and type", () => {
    expect(ddlPreview("organization", "Contract end date", "date", "EUR")).toBe(
      "ALTER organization ADD COLUMN cf_contract_end_date (date) · backfilled NULL · reversible",
    );
    expect(ddlPreview("deal", "Budget ceiling", "currency", "EUR")).toBe(
      "ALTER deal ADD COLUMN cf_budget_ceiling (numeric · cents · EUR) · backfilled NULL · reversible",
    );
  });

  it("flags structural-word labels, passes ordinary ones", () => {
    expect(looksStructural("Link to parent account")).toBe(true);
    expect(looksStructural("New relationship")).toBe(true);
    expect(looksStructural("Lookup to company")).toBe(true);
    expect(looksStructural("Renewal date")).toBe(false);
  });
});
