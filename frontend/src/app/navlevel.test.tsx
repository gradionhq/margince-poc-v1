/** @vitest-environment jsdom */
import { render, screen } from "@testing-library/react";
import { Blocks } from "lucide-react";
import { describe, expect, it } from "vitest";
import { LocaleProvider } from "../i18n";
import { NavLevelView } from "./navlevel";

// A composed unit's row is named by the INSTALLATION, so it carries a `label`
// rather than a message key. This is the render that matters: the data-level
// test in nav.test.ts asserts the entry HAS the label, and it passed for the
// whole time this renderer ignored it and showed every unit as the literal
// word "Unit" — in the row, in its accessible name, and in the collapsed
// rail's tooltip, which all read the same string.
describe("a nav entry named by the installation", () => {
  it("shows its own label, not the fallback key's text", () => {
    render(
      <LocaleProvider>
        <NavLevelView
          level={{
            groups: [
              {
                headingKey: "nav.group.units",
                items: [
                  {
                    id: "ext/notes",
                    labelKey: "nav.units.entry",
                    label: "notes",
                    icon: Blocks,
                  },
                ],
              },
            ],
            path: [],
            activeId: "ext/notes",
          }}
          state={{ collapsed: false, tip: null, onTip: () => {} }}
          onSelect={() => {}}
          onWalkUp={() => {}}
        />
      </LocaleProvider>,
    );
    const row = screen.getByRole("link", { name: "notes" });
    expect(row.textContent).toContain("notes");
    expect(row.textContent).not.toContain("Unit");
    // And it is the row the route made current.
    expect(row.getAttribute("aria-current")).toBe("page");
  });
});
