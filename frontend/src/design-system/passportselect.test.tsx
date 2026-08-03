/** @vitest-environment jsdom */
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it } from "vitest";
import { PassportSelect, ScopeChips } from "./passportselect";

// PassportSelect and ScopeChips are the extracted shapes the tool console's
// passport filter and the OAuth consent screen (Task 7) both render — these
// specs pin the behaviour either caller relies on: every option listed, the
// empty choice gated by `allowEmpty`, the chosen id reported back, and every
// scope the passport carries actually reaching the DOM.

afterEach(cleanup);

const OPTIONS = [
  { id: "p1", label: "night agent", scopes: ["read", "write"] },
  { id: "p2", label: "reporter", scopes: ["read"] },
];

describe("PassportSelect", () => {
  it("lists every option and reports the chosen id", async () => {
    const chosen: string[] = [];
    render(
      <PassportSelect
        options={OPTIONS}
        value=""
        onChange={(id) => chosen.push(id)}
        allowEmpty
      />,
    );
    const select = screen.getByRole("combobox");
    expect(screen.getByRole("option", { name: /night agent/ })).toBeTruthy();
    await userEvent.selectOptions(select, "p2");
    expect(chosen).toEqual(["p2"]);
  });

  it("offers no empty choice when allowEmpty is absent", () => {
    render(<PassportSelect options={OPTIONS} value="p1" onChange={() => {}} />);
    expect(screen.getAllByRole("option")).toHaveLength(2);
  });
});

describe("ScopeChips", () => {
  it("renders every scope as one chip, all reading the same", () => {
    render(<ScopeChips scopes={["read", "write"]} />);
    const read = screen.getByText("read");
    const write = screen.getByText("write");
    // Each chip is exactly its scope name with nothing appended: a connection
    // gets the scopes of the passport lent to it, so no chip is qualified as
    // withheld, and neither chip may read as weaker than the other.
    expect(read.textContent).toBe("read");
    expect(write.textContent).toBe("write");
    expect(write.className).toBe(read.className);
  });
});
