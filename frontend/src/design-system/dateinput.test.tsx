/** @vitest-environment jsdom */
// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { DateInput } from "./dateinput";

// This control owns two things — being a date input, and the format of the value
// a caller may hand it — and the second is the interesting one. A malformed date
// is not refused at runtime by anything: the element sanitizes what it cannot
// read down to "" per the HTML spec, so the value simply disappears with nothing
// said about why. That silence is what the prop type exists to prevent, and both
// halves are asserted below: the type refuses the shapes at compile time, and the
// runtime shows what would otherwise happen.

afterEach(cleanup);

const box = () => screen.getByLabelText<HTMLInputElement>("Closes on");

describe("the date control", () => {
  it("is a date input, which is the whole reason it exists", () => {
    render(<DateInput aria-label="Closes on" defaultValue="2026-07-18" />);

    // A `type="date"` box exposes no role of its own, so the attribute is the
    // observable — and it is the one thing a caller cannot override, since
    // `type` is omitted from the props.
    expect(box().getAttribute("type")).toBe("date");
    expect(box().value).toBe("2026-07-18");
  });

  it("renders empty when handed no value, which is a cleared date field", () => {
    render(<DateInput aria-label="Closes on" defaultValue="" />);

    expect(box().value).toBe("");
  });

  it("keeps the house class ahead of a caller's own", () => {
    render(<DateInput aria-label="Closes on" className="clause-value" />);

    expect(box().className).toBe("input clause-value");
  });
});

describe("the value contract", () => {
  it("admits YYYY-MM-DD", () => {
    render(<DateInput aria-label="Closes on" value="2026-07-18" readOnly />);

    expect(box().value).toBe("2026-07-18");
  });

  // Each of the three below carries two assertions. The `@ts-expect-error` is
  // one: the line fails to compile if the narrowing stops working, because an
  // unneeded expect-error is itself an error (TS2578, and `tsconfig.node.json`
  // includes `src`, so these files are in a typechecked project). The `expect` is
  // the other, and records the cost of getting past the type — the value is gone,
  // and nothing anywhere says so.
  //
  // React stringifies whatever it is given, which is why a ONE-element list is
  // absent here: `["2026-07-18"]` coerces to exactly the right string and works
  // by accident. That is the strongest argument for putting the check in the type
  // — a mistake that sometimes survives is one no runtime gate will find.

  it("refuses a locale-formatted date, which would otherwise vanish silently", () => {
    // @ts-expect-error the format is the contract; 18/07/2026 is not it
    render(<DateInput aria-label="Closes on" value="18/07/2026" readOnly />);

    expect(box().value).toBe("");
  });

  it("refuses an epoch number", () => {
    // @ts-expect-error React's own prop type allows a number
    render(<DateInput aria-label="Closes on" value={1710000000000} readOnly />);

    expect(box().value).toBe("");
  });

  it("refuses a list, since a date is one value", () => {
    render(
      <DateInput
        aria-label="Closes on"
        // @ts-expect-error React's own prop type allows a list
        value={["2026-07-18", "2026-07-19"]}
        readOnly
      />,
    );

    expect(box().value).toBe("");
  });
});
