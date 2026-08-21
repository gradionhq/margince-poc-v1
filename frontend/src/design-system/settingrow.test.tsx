// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

/** @vitest-environment jsdom */
import "@testing-library/jest-dom/vitest";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { TextInput } from "./atoms";
import { SettingList, SettingRow } from "./settingrow";
import { Switch } from "./switch";

afterEach(cleanup);

// jsdom resolves no custom property and applies no stylesheet, so a claim about
// WHICH rule holds is read off the sheet itself — the same way panel.test.tsx
// holds its seam rule.
const here = dirname(fileURLToPath(import.meta.url));

function settingRowCss(): string {
  return readFileSync(join(here, "settingrow.css"), "utf8");
}

describe("SettingRow", () => {
  // The whole reason the control arrives as a function: a row draws the label
  // and the control announces it, and the two have to be the same string
  // without the caller writing it twice.
  it("names the control with the label it drew", () => {
    render(
      <SettingRow
        label="Reply-to address"
        description="Where a reply to a captured thread is sent."
        control={(control) => <TextInput {...control} defaultValue="a@b.com" />}
      />,
    );
    expect(
      screen.getByRole("textbox", { name: "Reply-to address" }),
    ).toHaveAccessibleDescription(
      "Where a reply to a captured thread is sent.",
    );
  });

  // A control pointing at a description element that is not on the page is a
  // dangling reference, and jsdom reports the name and description of whatever
  // it finds — which is nothing. So the row hands `undefined` rather than an
  // id it did not render.
  it("describes the control with nothing when the row has no description", () => {
    render(
      <SettingRow
        label="Reply-to address"
        control={(control) => <TextInput {...control} />}
      />,
    );
    const control = screen.getByRole("textbox", { name: "Reply-to address" });
    expect(control).not.toHaveAttribute("aria-describedby");
  });

  // The value beside the verb — "luitpold.me [Edit]" — is a fact about the
  // setting, so it must not join the button's accessible name: a button called
  // "marek@gradion.com Edit" reads as a different control on every record.
  it("keeps the current value out of the control's name", () => {
    render(
      <SettingRow
        label="Reply-to address"
        value="marek@gradion.com"
        control={<button type="button">Edit</button>}
      />,
    );
    expect(screen.getByRole("button", { name: "Edit" })).toBeInTheDocument();
    expect(screen.getByText("marek@gradion.com")).toBeInTheDocument();
  });

  // A `Switch` owns its own hidden label, so the row must not name it a second
  // time. What this proves is that the composition the switch documents —
  // `labelHidden` beside a row that draws the heading — leaves exactly one
  // accessible name behind.
  it("leaves a switch with one name when the row draws the heading", () => {
    render(
      <SettingRow
        label="Auto-enrich captured companies"
        description="Looks a company up the first time it is captured."
        control={
          <Switch
            label="Auto-enrich captured companies"
            labelHidden
            checked
            onChange={() => undefined}
          />
        }
      />,
    );
    expect(
      screen.getByRole("switch", { name: "Auto-enrich captured companies" }),
    ).toBeInTheDocument();
  });

  // A `Switch` stacks its own `reason` under the track in a left-aligned
  // column, which is right where the switch draws its own label at the left of a
  // row. In a row's ANSWER column it left the track floating in the middle of
  // the row while the sentence reached the card's edge — the one thing that
  // column exists to prevent. Which alignment applies is a stylesheet claim, so
  // what is asserted here is the pair the rule keys on: an inline row, and the
  // switch's own wrapper inside the control.
  it("puts a refused switch and its reason in the row's answer column", () => {
    render(
      <SettingRow
        testId="posture-row"
        label="Retain-only posture"
        description="While this is on, this installation destroys nothing."
        control={
          <Switch
            label="Retain-only posture"
            labelHidden
            reason="Only an admin or ops can change retention."
            checked={false}
            onChange={() => undefined}
          />
        }
      />,
    );
    const row = screen.getByTestId("posture-row");
    expect(row).not.toHaveClass("settingrow-stack");
    const control = row.querySelector(".settingrow-control");
    expect(control?.querySelector(".switchrow")).not.toBeNull();
    expect(settingRowCss()).toMatch(
      /\.settingrow:not\(\.settingrow-stack\) \.settingrow-control \.switchrow \{[^}]*align-items:\s*flex-end/,
    );
  });

  it("gives a stacked row's control the full width below the naming", () => {
    render(
      <SettingRow
        testId="matrix-row"
        label="What each role may reach"
        layout="stack"
        control={<table />}
      />,
    );
    expect(screen.getByTestId("matrix-row")).toHaveClass("settingrow-stack");
  });
});

describe("SettingList", () => {
  // The hairline rides on the FOLLOWING sibling, which is a claim about the
  // stylesheet a jsdom test cannot read. What it can hold is the structure the
  // rule depends on: the rows are the list's own children, so `> * + *`
  // matches. A caller that wrapped its rows in a div would break the ruling
  // silently and still pass a render test.
  it("keeps every row as its own child so the rule between them matches", () => {
    render(
      <SettingList testId="list">
        <SettingRow testId="one" label="One" control={<span />} />
        <SettingRow testId="two" label="Two" control={<span />} />
      </SettingList>,
    );
    const list = screen.getByTestId("list");
    expect(
      [...list.children].map((child) => child.getAttribute("data-testid")),
    ).toEqual(["one", "two"]);
  });

  // A filled box is bounded by its own fill, so the rule after one says the
  // same thing twice — and lands flush on that box's rounded bottom, reading as
  // a line hanging off it. Three surfaces had grown the symptom (connected
  // agents, the extension-access read-only notice, the LinkedIn account-read
  // failure) before it was recognised as the list's rule rather than any card's,
  // and one of them had already spelled a screen-level workaround.
  it("draws no hairline against a filled slab above it", () => {
    const css = settingRowCss();
    const rule =
      /\.settinglist > \.empty \+ \*,\s*\.settinglist > \.callout \+ \*\s*\{([^}]*)\}/.exec(
        css,
      );
    expect(rule).not.toBeNull();
    expect(rule?.[1] ?? "").toMatch(/border-top:\s*0/);
    // And only against a slab: the rule between two ordinary rows is the whole
    // point of the list.
    expect(css).toMatch(
      /\.settinglist > \* \+ \*\s*\{\s*border-top:\s*1px solid var\(--borderSubtle\)/,
    );
  });
});
