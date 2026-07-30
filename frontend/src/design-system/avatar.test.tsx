/** @vitest-environment jsdom */
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { Avatar } from "./atoms";

// A55: the monogram is the FLOOR, not a fallback of last resort. Every company
// renders a clean mark — never a broken image, never an empty slot — so the
// initials are present in the markup whether or not a logo resolved, and a
// logo that fails to load simply stops being drawn.

afterEach(cleanup);

describe("Avatar", () => {
  it("renders the monogram when no logo resolved", () => {
    render(<Avatar name="Voltaq Systems" />);
    expect(screen.getByText("VS")).toBeTruthy();
    expect(document.querySelector("img")).toBeNull();
  });

  it("draws the logo over the monogram, never instead of it", () => {
    render(
      <Avatar name="Voltaq Systems" src="/v1/organizations/abc/logo" tinted />,
    );
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

  it("gives the same company the same tone every time", () => {
    const { container: first } = render(
      <Avatar name="Nordwind Energie" tinted />,
    );
    const firstClass = first.querySelector("span")?.className;
    cleanup();
    const { container: second } = render(
      <Avatar name="Nordwind Energie" tinted />,
    );
    expect(second.querySelector("span")?.className).toBe(firstClass);
  });
});
