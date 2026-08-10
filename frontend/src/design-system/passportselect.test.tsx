/** @vitest-environment jsdom */
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it } from "vitest";
import { PassportSelect, ScopeChips } from "./passportselect";
import { pickOption } from "./select-testing";

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
    const user = userEvent.setup();
    const chosen: string[] = [];
    render(
      <PassportSelect
        options={OPTIONS}
        value=""
        onChange={(id) => chosen.push(id)}
        allowEmpty
      />,
    );

    await pickOption(user, screen.getByRole("combobox"), "reporter");

    expect(chosen).toEqual(["p2"]);
  });

  // The options only exist while the popup is open — the control renders no
  // listbox when closed — so counting them means opening it first.
  it("offers no empty choice when allowEmpty is absent", async () => {
    const user = userEvent.setup();
    render(<PassportSelect options={OPTIONS} value="p1" onChange={() => {}} />);

    await user.click(screen.getByRole("combobox"));

    expect(screen.getAllByRole("option").map((o) => o.textContent)).toEqual([
      "night agent",
      "reporter",
    ]);
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
