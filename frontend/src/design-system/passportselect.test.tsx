/** @vitest-environment jsdom */
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it } from "vitest";
import { PassportSelect, ScopeChips } from "./passportselect";

// PassportSelect and ScopeChips are the extracted shapes the tool console's
// passport filter and the OAuth consent screen (Task 7) both render — these
// specs pin the behaviour either caller relies on: every option listed, the
// empty choice gated by `allowEmpty`, the chosen id reported back, and a
// granted vs. non-granted scope actually distinguishable in the DOM.

afterEach(cleanup);

const OPTIONS = [
  {
    id: "p1",
    label: "night agent",
    scopes: ["read", "write"],
    granted: ["read"],
  },
  { id: "p2", label: "reporter", scopes: ["read"], granted: ["read"] },
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
  it("dims the scopes a selection does not grant", () => {
    render(<ScopeChips scopes={["read", "write"]} dim={new Set(["write"])} />);
    expect(screen.getByText("write").className).toContain("dim");
  });

  it("does not dim a granted scope, and the two read as different chips", () => {
    render(<ScopeChips scopes={["read", "write"]} dim={new Set(["write"])} />);
    const granted = screen.getByText("read");
    const notGranted = screen.getByText("write");
    expect(granted.className).not.toContain("dim");
    expect(notGranted.className).not.toBe(granted.className);
  });

  it("pairs the dim class with an accessible label, not opacity alone", () => {
    render(<ScopeChips scopes={["write"]} dim={new Set(["write"])} />);
    // The visible chip text stays exactly the scope name; the "not granted"
    // signal reaches assistive tech through a paired sr-only span rather
    // than a contrast-dropping style on the chip itself.
    expect(screen.getByText("write").textContent).toBe("write not granted");
    expect(
      screen.getByText("write", { selector: "span:not(.sr-only)" }).style
        .opacity,
    ).toBe("");
  });
});
