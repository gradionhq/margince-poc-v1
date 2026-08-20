/** @vitest-environment jsdom */
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { cleanup, render } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { PanelRow } from "./panel";

afterEach(cleanup);

const here = dirname(fileURLToPath(import.meta.url));

function panelCss(): string {
  return readFileSync(join(here, "panel.css"), "utf8");
}

// The rule and the hover are two shapes, and PanelRow used to hold them
// together: every row lit up under the pointer, so a panel of ruled blocks a
// reader is meant to READ told them all five were pressable. The default is
// therefore inert, and a caller opts in only when the whole row is one press
// target.
describe("PanelRow separates the hairline from the press", () => {
  it("draws an inert row by default", () => {
    const { container } = render(<PanelRow>Attainment to date</PanelRow>);
    const row = container.querySelector(".panel-row");
    expect(row).not.toBeNull();
    expect(row?.classList.contains("panel-row-interactive")).toBe(false);
  });

  it("marks the row a press target when the caller says it is one", () => {
    const { container } = render(
      <PanelRow interactive>
        <button type="button">Q4 — renewals</button>
      </PanelRow>,
    );
    expect(
      container
        .querySelector(".panel-row")
        ?.classList.contains("panel-row-interactive"),
    ).toBe(true);
  });

  // The caller's own class survives the variant: a screen names its row for
  // layout, and the two spellings have to coexist on one element.
  it("keeps the caller's class beside the variant", () => {
    const { container } = render(
      <PanelRow interactive className="quota-row-on">
        Q3
      </PanelRow>,
    );
    const row = container.querySelector(".panel-row");
    expect(row?.classList.contains("panel-row-interactive")).toBe(true);
    expect(row?.classList.contains("quota-row-on")).toBe(true);
  });
});

// A deleted rule body still parses and still paints, so the stylesheet is
// asserted on directly rather than trusting the class list above: the hover
// fill has to exist, and it has to hang on the interactive class alone.
describe("panel.css keeps the row's hover on the interactive variant", () => {
  it("declares the hover fill only for an interactive row", () => {
    const css = panelCss();
    const hovers = [...css.matchAll(/([^{}]*:hover)\s*\{([^}]*)\}/g)].filter(
      ([, selector]) => selector.includes(".panel-row"),
    );
    expect(hovers.length).toBe(1);
    const [selector, body] = [hovers[0][1].trim(), hovers[0][2]];
    expect(selector).toBe(".panel-row-interactive:hover");
    // The body is the point of the variant. An emptied rule reads as a live
    // one to every gate that only counts selectors.
    expect(body).toMatch(/background:\s*var\(--bgHover\)/);
  });

  it("leaves the bare row its hairline and nothing that suggests a press", () => {
    const bare = /(?:^|\n)\.panel-row\s*\{([^}]*)\}/.exec(panelCss());
    expect(bare).not.toBeNull();
    const body = bare?.[1] ?? "";
    expect(body).toMatch(/border-top:\s*1px solid var\(--borderSubtle\)/);
    expect(body).not.toMatch(/background/);
    // A transition on a row with no state to move between is the leftover of
    // the hover it used to carry.
    expect(body).not.toMatch(/transition/);
  });
});
