// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

/** @vitest-environment jsdom */
import "@testing-library/jest-dom/vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { TextInput } from "./atoms";
import { SettingList, SettingRow } from "./settingrow";
import { Switch } from "./switch";

afterEach(cleanup);

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
});
