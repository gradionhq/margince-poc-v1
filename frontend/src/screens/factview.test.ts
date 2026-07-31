import { describe, expect, it } from "vitest";
import type { components } from "../api/schema";
import { canonical, factFieldLabelKey, groupFacts } from "./factview";

type OrganizationFact = components["schemas"]["OrganizationFact"];

function fact(over: Partial<OrganizationFact> = {}): OrganizationFact {
  return {
    category: "market",
    field: "served_industry",
    value: "E-Commerce",
    value_key: "e-commerce",
    source: "site_read",
    captured_by: "agent:site-read",
    updated_at: "2026-07-29T07:37:00Z",
    ...over,
  };
}

const values = (groups: ReturnType<typeof groupFacts>, category: string) =>
  groups.find((g) => g.category === category)?.facts.map((f) => f.value) ?? [];

describe("canonical", () => {
  it("collapses the spellings a scrape produces of one value", () => {
    expect(canonical("Shop-Devs")).toBe(canonical("Shop Devs"));
    expect(canonical("shop devs")).toBe(canonical("Shop-Devs"));
  });

  it("keeps diacritics, which carry meaning in German", () => {
    expect(canonical("Prüfung")).not.toBe(canonical("Prufung"));
  });
});

describe("groupFacts", () => {
  it("shows one row for two spellings of the same fact", () => {
    const groups = groupFacts([
      fact({ value: "Shop Devs" }),
      fact({ value: "Shop-Devs" }),
    ]);
    expect(values(groups, "market")).toHaveLength(1);
  });

  it("keeps the same value under two different fields apart", () => {
    // A company can genuinely be both a partner and a named customer, and the
    // field is what distinguishes the two statements.
    const groups = groupFacts([
      fact({ category: "signal", field: "partner", value: "bitExpert" }),
      fact({ category: "signal", field: "named_customer", value: "bitExpert" }),
    ]);
    expect(values(groups, "signal")).toHaveLength(2);
  });

  it("collapses one offering listed as product, service and capability", () => {
    const groups = groupFacts([
      fact({ category: "offering", field: "capability", value: "PaaS" }),
      fact({ category: "offering", field: "service", value: "PaaS" }),
      fact({ category: "offering", field: "product", value: "PaaS" }),
    ]);
    const offering = groups.find((g) => g.category === "offering");
    expect(offering?.facts).toHaveLength(1);
    // Product is the most concrete of the three, so it is the one kept.
    expect(offering?.facts[0].field).toBe("product");
  });

  it("keeps a human-held value over a more confident machine one", () => {
    const groups = groupFacts([
      fact({ value: "E-Commerce", source: "site_read", confidence: 0.9 }),
      fact({ value: "e commerce", source: "human", confidence: 0.1 }),
    ]);
    expect(groups[0].facts[0].source).toBe("human");
  });

  it("orders the most confident fact first", () => {
    const groups = groupFacts([
      fact({ value: "Agenturen", confidence: 0.2 }),
      fact({ value: "Shopbetreiber", confidence: 0.9 }),
    ]);
    expect(values(groups, "market")[0]).toBe("Shopbetreiber");
  });

  it("orders categories the same way every time", () => {
    const groups = groupFacts([
      fact({ category: "signal", field: "technology", value: "Redis" }),
      fact({ category: "company", field: "phone", value: "+49 30 1" }),
      fact({ category: "offering", field: "product", value: "Frontic" }),
    ]);
    expect(groups.map((g) => g.category)).toEqual([
      "company",
      "offering",
      "signal",
    ]);
  });

  it("omits a category with no facts rather than drawing an empty heading", () => {
    const groups = groupFacts([fact({ category: "market" })]);
    expect(groups.map((g) => g.category)).toEqual(["market"]);
  });

  it("names every field the schema allows", () => {
    // Derived from the schema's own vocabulary, so a new fact field fails here
    // rather than reaching a German reader as English snake_case.
    const fields: OrganizationFact["field"][] = [
      "founded_year",
      "employee_range",
      "phone",
      "contact_email",
      "location",
      "service",
      "product",
      "capability",
      "served_industry",
      "company_size",
      "geography",
      "language",
      "certification",
      "partner",
      "named_customer",
      "technology",
      "quantified_outcome",
    ];
    for (const field of fields) {
      expect(factFieldLabelKey(field)).toBe(`co.factField.${field}`);
    }
  });
});
