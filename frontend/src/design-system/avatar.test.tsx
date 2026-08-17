/** @vitest-environment jsdom */
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { Avatar } from "./atoms";

// A55: the monogram is the FLOOR, not a fallback of last resort. Every company
// renders a clean mark — never a broken image, never an empty slot — so the
// initials are present in the markup whether or not a logo resolved, and a
// logo that fails to load simply stops being drawn.

afterEach(cleanup);

function chipOf(container: HTMLElement): string {
  return container.querySelector("span")?.className ?? "";
}

function toneOf(container: HTMLElement): string | undefined {
  return chipOf(container)
    .split(" ")
    .find((cls) => cls.startsWith("avatar-t"));
}

describe("Avatar", () => {
  it("renders the monogram when no logo resolved", () => {
    render(<Avatar name="Voltaq Systems" />);
    expect(screen.getByText("VS")).toBeTruthy();
    expect(document.querySelector("img")).toBeNull();
  });

  it("draws the logo over the monogram, never instead of it", () => {
    render(<Avatar name="Voltaq Systems" src="/v1/organizations/abc/logo" />);
    const img = document.querySelector("img");
    expect(img?.getAttribute("src")).toBe("/v1/organizations/abc/logo");
    // The image is decorative: the record's name is already beside it, so a
    // screen reader announcing the mark again would only repeat the row.
    expect(img?.getAttribute("alt")).toBe("");
    // The monogram stays in the markup underneath — that is what shows until
    // the image paints.
    expect(screen.getByText("VS")).toBeTruthy();
  });

  it("falls back to the monogram when the logo fails to load", () => {
    render(<Avatar name="Voltaq Systems" src="/v1/organizations/abc/logo" />);
    const img = document.querySelector("img");
    expect(img).toBeTruthy();
    if (img) fireEvent.error(img);
    expect(document.querySelector("img")).toBeNull();
    expect(screen.getByText("VS")).toBeTruthy();
  });

  // The monogram rule reaches names that are not two words with a space between
  // them, because the signed-in reader is frequently known to the product only
  // by their address — and a whitespace-only split turns every colleague whose
  // address begins with the same letter into the same chip.
  describe("the monogram", () => {
    it.each([
      ["Voltaq Systems", "VS"],
      ["jane.doe@example.com", "JD"],
      ["ops-team@example.com", "OT"],
      ["Ana-Sofía Ruiz", "AS"],
      ["Müller", "M"],
      ["李", "李"],
    ])("reads %s as %s", (name, expected) => {
      const { container } = render(<Avatar name={name} />);
      expect(container.textContent).toBe(expected);
      cleanup();
    });
  });

  describe("the tone", () => {
    it("is the same for the same record every time", () => {
      const { container: first } = render(<Avatar name="Nordwind Energie" />);
      const firstTone = toneOf(first);
      expect(firstTone).toBeTruthy();
      cleanup();
      const { container: second } = render(<Avatar name="Nordwind Energie" />);
      expect(toneOf(second)).toBe(firstTone);
    });

    // The tint used to be opt-in, so the same company was a coloured chip in
    // the list it was found in and a neutral accent chip on the record page
    // that list opened. A chip that identifies a record on one screen and not
    // on the next identifies nothing.
    it("is drawn without being asked for", () => {
      const { container } = render(<Avatar name="Nordwind Energie" />);
      expect(toneOf(container)).toBeTruthy();
    });

    // A key is what makes the colour a property of the RECORD rather than of
    // the string currently displayed for it. Without one, renaming a company
    // moves it to a different colour on every screen at once.
    it("follows the identity key across a rename", () => {
      const { container: before } = render(
        <Avatar identity="org_7f3" name="Voltaq Systems" />,
      );
      const toneBefore = toneOf(before);
      cleanup();
      const { container: after } = render(
        <Avatar identity="org_7f3" name="Voltaq Systems GmbH" />,
      );
      expect(toneOf(after)).toBe(toneBefore);
    });

    it("falls back to the name when no key is given", () => {
      const { container: keyed } = render(
        <Avatar identity="Voltaq Systems" name="Something else" />,
      );
      const keyedTone = toneOf(keyed);
      cleanup();
      const { container: unkeyed } = render(<Avatar name="Voltaq Systems" />);
      expect(toneOf(unkeyed)).toBe(keyedTone);
    });
  });

  // Four sizes were being rendered — 20, 28, 44 and 76 — by three different
  // stylesheets, for a prop that admitted two. They are one scale now, and the
  // chip names its own rung rather than inheriting one from an ancestor's class.
  describe("the size", () => {
    it("is the list rung unless a caller asks otherwise", () => {
      const { container } = render(<Avatar name="Voltaq Systems" />);
      expect(chipOf(container).split(" ")).toContain("avatar-sm");
    });

    it.each(["xs", "sm", "md", "lg"] as const)("names the %s rung", (size) => {
      const { container } = render(
        <Avatar name="Voltaq Systems" size={size} />,
      );
      expect(chipOf(container).split(" ")).toContain(`avatar-${size}`);
      cleanup();
    });
  });
});
