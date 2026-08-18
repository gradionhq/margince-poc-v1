/** @vitest-environment jsdom */
// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { afterEach, describe, expect, it } from "vitest";
import { TokenInput } from "./tokeninput";

// The `in` operator's value control. Every rule below is one a reader would
// otherwise discover by losing a value they typed.

afterEach(cleanup);

/** Controlled, because a test that never feeds the value back proves only that
 *  the callback fired. */
function Harness({ start = [] }: Readonly<{ start?: readonly string[] }>) {
  const [values, setValues] = useState<readonly string[]>(start);
  return (
    <TokenInput
      values={values}
      onChange={setValues}
      aria-label="Region"
      placeholder="DE, AT"
    />
  );
}

// The query's own type argument, rather than an assertion on its result: the
// value assertions below need the input's `value`, and naming the element type
// here asks the query for it instead of overriding what it answered.
const box = () =>
  screen.getByRole<HTMLInputElement>("textbox", { name: "Region" });

describe("committing a value", () => {
  it("turns typed text into a token on Enter", async () => {
    const user = userEvent.setup();
    render(<Harness />);

    await user.type(box(), "DE{Enter}");

    expect(screen.getByText("DE")).toBeTruthy();
    expect(box().value).toBe("");
  });

  it("commits on comma too, so a pasted list does not become one token", async () => {
    const user = userEvent.setup();
    render(<Harness />);

    await user.type(box(), "DE,AT,");

    expect(screen.getByText("DE")).toBeTruthy();
    expect(screen.getByText("AT")).toBeTruthy();
  });

  it("splits a single commit carrying several values", async () => {
    const user = userEvent.setup();
    render(<Harness />);

    // What a paste looks like once it reaches the box: one value with commas in
    // it, committed at once.
    await user.type(box(), "DE, AT, CH{Enter}");

    for (const region of ["DE", "AT", "CH"]) {
      expect(screen.getByText(region), region).toBeTruthy();
    }
  });

  it("commits on blur, so clicking away does not discard what was typed", async () => {
    const user = userEvent.setup();
    render(<Harness />);

    await user.type(box(), "DE");
    await user.tab();

    expect(screen.getByText("DE")).toBeTruthy();
  });

  it("drops a blank, which would otherwise match the empty string", async () => {
    const user = userEvent.setup();
    render(<Harness />);

    await user.type(box(), "   {Enter}");

    // Nothing was added: the only textbox content is the box itself.
    expect(screen.queryByRole("button")).toBeNull();
  });

  it("drops a duplicate silently, because `in` is a set", async () => {
    const user = userEvent.setup();
    render(<Harness start={["DE"]} />);

    await user.type(box(), "DE{Enter}");

    // One token, and no refusal shown — admitting DE twice would change nothing
    // about what matches, so a message would be noise.
    expect(screen.getAllByRole("button", { name: /^Remove/ })).toHaveLength(1);
  });

  it("drops a value a single commit repeats to itself", async () => {
    const user = userEvent.setup();
    render(<Harness />);

    // The colliding value is not on screen yet, so checking the committed set
    // alone admits both halves. What the reader would then see is two DE tokens
    // sharing one React key, and a remove control that takes away both.
    await user.type(box(), "DE, AT, DE{Enter}");

    expect(screen.getAllByRole("button", { name: /^Remove/ })).toHaveLength(2);
    expect(screen.getByRole("button", { name: "Remove DE" })).toBeTruthy();
  });
});

describe("removing a value", () => {
  it("removes the last token on Backspace in an empty box", async () => {
    const user = userEvent.setup();
    render(<Harness start={["DE", "AT"]} />);

    await user.click(box());
    await user.keyboard("{Backspace}");

    expect(screen.queryByText("AT")).toBeNull();
    expect(screen.getByText("DE")).toBeTruthy();
  });

  it("leaves the tokens alone when Backspace has text to delete instead", async () => {
    const user = userEvent.setup();
    render(<Harness start={["DE"]} />);

    await user.type(box(), "AT{Backspace}");

    // The keystroke edited the typed text, not the committed set — otherwise a
    // typo correction would silently eat a value the reader had finished with.
    expect(screen.getByText("DE")).toBeTruthy();
    expect(box().value).toBe("A");
  });

  it("removes the token its own control names, not the last one", async () => {
    const user = userEvent.setup();
    render(<Harness start={["DE", "AT", "CH"]} />);

    await user.click(screen.getByRole("button", { name: "Remove AT" }));

    expect(screen.queryByText("AT")).toBeNull();
    expect(screen.getByText("DE")).toBeTruthy();
    expect(screen.getByText("CH")).toBeTruthy();
  });
});
