/** @vitest-environment jsdom */
// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { cleanup, render, screen, within } from "@testing-library/react";
import { Building2 } from "lucide-react";
import { afterEach, describe, expect, it } from "vitest";
import { Breadcrumb } from "./breadcrumb";

// The specs for the trail. Three of its promises are the ones a caller cannot
// see by looking at it and would otherwise get wrong on every screen: the last
// stop is the page you are on and therefore not a link, the slashes between the
// stops are decoration and must not join the list a screen reader reads, and a
// trail with nothing on it draws nothing at all.

afterEach(cleanup);

const LANDMARK = "Breadcrumb";
const RECORD = "Brandt Logistik GmbH";

function trail() {
  return screen.getByRole("navigation", { name: LANDMARK });
}

describe("Breadcrumb", () => {
  it("names its landmark with the name the caller translated", () => {
    render(
      <Breadcrumb
        label={LANDMARK}
        items={[{ label: "Companies", href: "#/companies" }]}
      />,
    );

    expect(trail().tagName).toBe("NAV");
  });

  it("draws the last stop as the current page rather than a link", () => {
    render(
      <Breadcrumb
        label={LANDMARK}
        items={[
          { label: "Companies", href: "#/companies" },
          // An href on the last stop is deliberately ignored: a link to the page
          // you are already on is a control that does nothing.
          { label: RECORD, href: "#/companies/c-1" },
        ]}
      />,
    );

    expect(screen.queryByRole("link", { name: RECORD })).toBeNull();
    const current = trail().querySelectorAll('[aria-current="page"]');
    expect(current).toHaveLength(1);
    expect(current[0]?.textContent).toBe(RECORD);
  });

  it("links every earlier stop that carries an href", () => {
    render(
      <Breadcrumb
        label={LANDMARK}
        items={[
          { label: "Deals", href: "#/deals" },
          { label: "Companies", href: "#/companies", icon: Building2 },
          { label: RECORD },
        ]}
      />,
    );

    expect(
      screen.getByRole("link", { name: "Deals" }).getAttribute("href"),
    ).toBe("#/deals");
    // The glyph is decorative, so it does not join the link's accessible name.
    expect(
      screen.getByRole("link", { name: "Companies" }).getAttribute("href"),
    ).toBe("#/companies");
    expect(screen.getAllByRole("link")).toHaveLength(2);
  });

  it("leaves an earlier stop with no href as plain text", () => {
    render(
      <Breadcrumb
        label={LANDMARK}
        items={[
          { label: "Reports" },
          { label: "Pipeline coverage", href: "#/reports/coverage" },
          { label: RECORD },
        ]}
      />,
    );

    expect(screen.queryByRole("link", { name: "Reports" })).toBeNull();
    expect(screen.getByText("Reports").tagName).toBe("SPAN");
  });

  it("hides the separators from the list a screen reader reads", () => {
    render(
      <Breadcrumb
        label={LANDMARK}
        items={[
          { label: "Deals", href: "#/deals" },
          { label: "Companies", href: "#/companies" },
          { label: RECORD },
        ]}
      />,
    );

    const nav = trail();
    // One list item per stop, and not one more: a separator rendered as an <li>
    // would make a three-stop trail announce as five things.
    expect(within(nav).getAllByRole("listitem")).toHaveLength(3);

    const separators = [...nav.querySelectorAll("span")].filter(
      (span) => span.textContent === "/",
    );
    expect(separators).toHaveLength(2);
    for (const separator of separators) {
      expect(separator.getAttribute("aria-hidden")).toBe("true");
      expect(separator.closest("li")).not.toBeNull();
    }
  });

  it("carries a stop's language tag onto the label that needs it", () => {
    render(
      <Breadcrumb
        label={LANDMARK}
        items={[
          { label: "Companies", href: "#/companies" },
          { label: "Rahmenvertrag Nordwest", lang: "de" },
        ]}
      />,
    );

    expect(
      screen.getByText("Rahmenvertrag Nordwest").getAttribute("lang"),
    ).toBe("de");
  });

  it("draws a single stop as the page it is, with no trail around it", () => {
    render(<Breadcrumb label={LANDMARK} items={[{ label: "Companies" }]} />);

    const nav = trail();
    expect(within(nav).getAllByRole("listitem")).toHaveLength(1);
    expect(nav.querySelector('[aria-current="page"]')?.textContent).toBe(
      "Companies",
    );
    expect(screen.queryAllByRole("link")).toHaveLength(0);
  });

  it("renders nothing at all when there is no trail to draw", () => {
    const { container } = render(<Breadcrumb label={LANDMARK} items={[]} />);

    expect(container.innerHTML).toBe("");
    expect(screen.queryByRole("navigation")).toBeNull();
  });
});
