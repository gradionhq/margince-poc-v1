/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it } from "vitest";
import type { components } from "../api/schema";
import { LocaleProvider } from "../i18n";
import { PersonResearchTab } from "./personresearch";

type Person360 = components["schemas"]["Person360"];
type ProfileField = components["schemas"]["PersonProfileField"];

// The 360's own person, captured once and reused across every fixture below —
// the tab never reads anything off it besides the id PersonProviderSection
// wants, so one shape here is enough.
const person: Person360["person"] = {
  id: "p-1",
  full_name: "Dana Buyer",
  first_name: "Dana",
  last_name: "Buyer",
  owner_id: "u-1",
  source: "ui",
  captured_by: "human:u-1",
  created_at: "2026-08-01T09:00:00Z",
  updated_at: "2026-08-12T09:00:00Z",
};

function profileField(
  row: Pick<ProfileField, "field" | "value" | "captured_at"> &
    Partial<ProfileField>,
): ProfileField {
  return {
    evidence_snippet: "Dana Buyer, Head of Fleet at Brandt Automotive GmbH",
    source: "site_read",
    captured_by: "agent:enrich",
    ...row,
  };
}

// This suite mounts several trees into one document. Without cleanup the
// second assertion reads the first render's DOM.
afterEach(() => {
  cleanup();
});

function withProviders(node: ReactNode) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  render(
    <QueryClientProvider client={client}>
      <LocaleProvider initial="en">{node}</LocaleProvider>
    </QueryClientProvider>,
  );
}

function view(partial: Partial<Person360>): Person360 {
  return {
    as_of: "2026-08-13T09:00:00Z",
    person,
    sections_omitted: [],
    ...partial,
  };
}

describe("the research tab's enrichment evidence", () => {
  it("draws a row for every enriched field the 360 carried", () => {
    withProviders(
      <PersonResearchTab
        view={view({
          profile_fields: [
            profileField({
              field: "title",
              value: "Head of Fleet",
              captured_at: "2026-08-10T09:00:00Z",
              claim_key: "profile_field:title",
            }),
            profileField({
              field: "phone",
              value: "+493012345678",
              captured_at: "2026-08-11T09:00:00Z",
              claim_key: "profile_field:phone",
            }),
          ],
        })}
      />,
    );
    expect(screen.getByText("Title")).toBeTruthy();
    expect(screen.getByText("Head of Fleet")).toBeTruthy();
    expect(screen.getByText("Phone")).toBeTruthy();
    expect(screen.getByText("+493012345678")).toBeTruthy();
  });

  it("carries each row's provenance: who captured it and a receipt to check it against", () => {
    withProviders(
      <PersonResearchTab
        view={view({
          profile_fields: [
            profileField({
              field: "title",
              value: "Head of Fleet",
              captured_at: "2026-08-10T09:00:00Z",
              captured_by: "agent:enrich",
              claim_key: "profile_field:title",
            }),
          ],
        })}
      />,
    );
    expect(screen.getByText("Captured by:")).toBeTruthy();
    expect(screen.getByText("agent: enrich")).toBeTruthy();
    // The value itself is the evidence affordance's trigger: opening it is
    // what surfaces the snippet, so the row's receipt is reachable through
    // the value rather than a separate widget.
    const trigger = screen.getByRole("button", { name: /Head of Fleet/ });
    expect(trigger.getAttribute("aria-expanded")).toBe("false");
  });

  it("keeps two readings of the same field as separate rows rather than one colliding on the key", () => {
    withProviders(
      <PersonResearchTab
        view={view({
          profile_fields: [
            profileField({
              field: "phone",
              value: "+493012345678",
              captured_at: "2026-08-10T09:00:00Z",
              // No claim_key: the row falls back to `field:captured_at`,
              // which is exactly what keeps this pair from colliding.
            }),
            profileField({
              field: "phone",
              value: "+491701234567",
              captured_at: "2026-08-11T09:00:00Z",
            }),
          ],
        })}
      />,
    );
    expect(screen.getByText("+493012345678")).toBeTruthy();
    expect(screen.getByText("+491701234567")).toBeTruthy();
  });

  it("says the provider snapshot is withheld rather than drawing it as an empty section", () => {
    withProviders(
      <PersonResearchTab
        view={view({
          sections_omitted: ["provider_profile"],
          profile_fields: [
            profileField({
              field: "title",
              value: "Head of Fleet",
              captured_at: "2026-08-10T09:00:00Z",
            }),
          ],
        })}
      />,
    );
    expect(
      screen.getByText("Hidden — your role cannot read this"),
    ).toBeTruthy();
    // A withheld section is not merely "nothing here" — the tab-wide empty
    // sentence must not also fire while the fields panel still has rows.
    expect(
      screen.queryByText("Nothing has been researched about them yet."),
    ).toBeNull();
  });

  it("says the enrichment evidence is withheld rather than claiming no field carries evidence", () => {
    withProviders(
      <PersonResearchTab
        view={view({
          sections_omitted: ["profile_fields"],
        })}
      />,
    );
    expect(
      screen.getByText("Hidden — your role cannot read this"),
    ).toBeTruthy();
    expect(
      screen.queryByText("No enriched field carries evidence yet."),
    ).toBeNull();
  });

  it("says the tab has nothing once, not twice, when neither half has anything to show", () => {
    withProviders(<PersonResearchTab view={view({ profile_fields: [] })} />);
    expect(
      screen.getByText("Nothing has been researched about them yet."),
    ).toBeTruthy();
    // The fields panel's own empty sentence must not ALSO render beside the
    // tab-wide one — that would be the same fact stated twice.
    expect(
      screen.queryByText("No enriched field carries evidence yet."),
    ).toBeNull();
  });
});
