// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

/** @vitest-environment jsdom */
import "@testing-library/jest-dom/vitest";

import { cleanup, render as rtlRender, screen } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { LocaleProvider, translate } from "../i18n";
import type { CompanyDraft } from "./onboarding";
import { EMPTY_DRAFT } from "./onboarding";
import { CompanyStep } from "./onboarding-company-form";

// Evidence-or-omit on the classic form: a grounded field shows the page's own
// words when the read captured them, and NOTHING when it did not. A chip drawn
// around an empty quote is the one failure this rule exists to prevent — it
// looks exactly like proof, and there is none behind it.

function render(ui: ReactNode) {
  return rtlRender(<LocaleProvider initial="en">{ui}</LocaleProvider>);
}

function groundedDraft(snippet: string | undefined): CompanyDraft {
  return {
    values: { ...EMPTY_DRAFT.values, legal_name: "Gradion Co., Ltd." },
    grounded: {
      legal_name: {
        field: "legal_name",
        value: "Gradion Co., Ltd.",
        evidence_snippet: snippet,
        source_kind: "url",
        source_url: "https://gradion.test/impressum",
      },
    },
    edited: new Set(),
  };
}

function renderForm(snippet: string | undefined) {
  render(
    <CompanyStep
      draft={groundedDraft(snippet)}
      setField={vi.fn()}
      onPickEntity={vi.fn()}
      read={null}
      saved={false}
      saveError={null}
      missingRequired={[]}
      selectedFactKeys={[]}
      setSelectedFactKeys={vi.fn()}
      onFieldBlur={vi.fn()}
    />,
  );
}

// The grounding label is asserted through its catalog key, not the sentence
// the en catalog happens to hold today: the wording belongs to the catalog,
// and what these tests are about is that the label renders at all.
const groundingLabel = translate("en", "ob.readFromSite");

afterEach(cleanup);

describe("a grounded field's evidence chip", () => {
  it("shows the page's own words when the read captured them", () => {
    renderForm("Gradion Co., Ltd. · HRB 12345 B");

    expect(
      screen.getByText(/Gradion Co\., Ltd\. · HRB 12345 B/),
    ).toBeInTheDocument();
    expect(document.querySelectorAll(".evidence-chip")).toHaveLength(1);
  });

  it("draws no chip at all when the read captured no quote", () => {
    renderForm(undefined);

    expect(document.querySelectorAll(".evidence-chip")).toHaveLength(0);
    // The grounding itself is still real and still says so: only the quote it
    // does not have is withheld.
    expect(screen.getByText(groundingLabel)).toBeInTheDocument();
  });

  it("treats a quote of nothing but whitespace as no quote", () => {
    renderForm("   ");

    expect(document.querySelectorAll(".evidence-chip")).toHaveLength(0);
    expect(screen.getByText(groundingLabel)).toBeInTheDocument();
  });
});
