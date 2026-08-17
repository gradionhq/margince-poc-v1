// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

/** @vitest-environment jsdom */
import "@testing-library/jest-dom/vitest";
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { Switch } from "./switch";

afterEach(cleanup);

describe("Switch", () => {
  it("announces itself as a switch carrying its state", () => {
    render(<Switch label="Auto-enrich" checked onChange={() => undefined} />);
    const control = screen.getByRole("switch", { name: "Auto-enrich" });
    expect(control).toHaveAttribute("aria-checked", "true");
  });

  // The state a server payload can actually deliver. `auto_enrich` is required
  // on the contract's CaptureSettings, so the compiler believes every read of
  // it is a boolean — and a body that arrived without it hands `undefined` to
  // this prop regardless. React then omits the attribute entirely, which is how
  // a shipped `role="switch"` came to announce no state at all.
  it("still announces a state when the caller has none to give", () => {
    render(
      <Switch
        label="Auto-enrich"
        checked={undefined}
        onChange={() => undefined}
      />,
    );
    const control = screen.getByRole("switch", { name: "Auto-enrich" });
    // Present, and specifically "false": the knob is drawn off, and a control
    // whose paint and whose announcement disagree is worse than either.
    expect(control).toHaveAttribute("aria-checked", "false");
  });

  it("writes ON from a state it never received", async () => {
    const onChange = vi.fn();
    render(
      <Switch label="Auto-enrich" checked={undefined} onChange={onChange} />,
    );
    await userEvent.click(screen.getByRole("switch"));
    // `!undefined` is also true, so this would pass by accident on the old
    // code — it is here to pin that the resolved state drives the write as well
    // as the announcement, so the two cannot drift apart later.
    expect(onChange).toHaveBeenCalledWith(true);
  });

  it("hands back the value the caller would write, not the one it holds", async () => {
    const onChange = vi.fn();
    render(<Switch label="Auto-enrich" checked onChange={onChange} />);
    await userEvent.click(screen.getByRole("switch"));
    // A setting writes on flip, so the caller needs the NEXT value. Passing
    // the current one would make every call site invert it itself.
    expect(onChange).toHaveBeenCalledWith(false);
  });

  it("does not fire while disabled", async () => {
    const onChange = vi.fn();
    render(<Switch label="Auto-enrich" checked disabled onChange={onChange} />);
    await userEvent.click(screen.getByRole("switch"));
    expect(onChange).not.toHaveBeenCalled();
  });

  it("points the control at its hint and its reason, so both are announced", () => {
    render(
      <Switch
        label="Auto-enrich"
        hint="Looks a company up on first capture."
        reason="Only an admin or ops can change this."
        checked={false}
        disabled
        onChange={() => undefined}
      />,
    );
    const described = screen
      .getByRole("switch")
      .getAttribute("aria-describedby");
    expect(described).toBeTruthy();
    const ids = (described ?? "").split(" ").filter(Boolean);
    expect(ids).toHaveLength(2);
    const text = ids
      .map((id) => document.getElementById(id)?.textContent)
      .join(" ");
    expect(text).toContain("Looks a company up on first capture.");
    expect(text).toContain("Only an admin or ops can change this.");
  });

  it("names nothing it did not render", () => {
    render(
      <Switch label="Auto-enrich" checked={false} onChange={() => undefined} />,
    );
    // With neither hint nor reason there is no element to point at, and an
    // aria-describedby naming a missing id is a dangling reference a reader
    // hears as silence.
    expect(screen.getByRole("switch")).not.toHaveAttribute("aria-describedby");
  });

  // The state the docblock and the design-system catalog have both claimed
  // this control carries since it was written, while the implementation had
  // only `disabled` and `reason`. A switch IS the write, so the gap was
  // exactly where it mattered least noticeably and most: the reader flips it,
  // nothing says the write is out, and a second flip sends a value derived
  // from a state the server never confirmed.
  describe("a flip that is still being written", () => {
    it("refuses the next flip without dropping the reader off the control", () => {
      const onChange = vi.fn();
      render(
        <Switch label="Auto-enrich" checked pending onChange={onChange} />,
      );
      const control = screen.getByRole("switch", { name: "Auto-enrich" });
      expect(control.hasAttribute("disabled")).toBe(false);
      expect(control).toHaveAttribute("aria-disabled", "true");
      expect(control).toHaveAttribute("aria-busy", "true");
      control.focus();
      expect(control).toHaveFocus();
    });

    it("does not write again while the first write is out", async () => {
      const user = userEvent.setup();
      const onChange = vi.fn();
      render(
        <Switch label="Auto-enrich" checked pending onChange={onChange} />,
      );
      await user.click(screen.getByRole("switch", { name: "Auto-enrich" }));
      expect(onChange).not.toHaveBeenCalled();
    });

    it("keeps showing which way the setting is currently set", () => {
      render(
        <Switch
          label="Auto-enrich"
          checked
          pending
          onChange={() => undefined}
        />,
      );
      // The knob is the only thing carrying the state the reader is changing
      // FROM, so the busy mark sits beside the label rather than over it.
      expect(screen.getByRole("switch")).toHaveAttribute(
        "aria-checked",
        "true",
      );
    });

    it("lets a denial outrank it — nobody's switch cannot be mid-flip", () => {
      render(
        <Switch
          label="Auto-enrich"
          checked
          pending
          disabled
          reason="Your seat cannot change this."
          onChange={() => undefined}
        />,
      );
      const control = screen.getByRole("switch", { name: "Auto-enrich" });
      expect(control.hasAttribute("disabled")).toBe(true);
      expect(control.hasAttribute("aria-busy")).toBe(false);
    });
  });

  it("keeps a hidden label reachable by name", () => {
    render(
      <Switch
        label="Marketing email"
        labelHidden
        checked
        onChange={() => undefined}
      />,
    );
    expect(
      screen.getByRole("switch", { name: "Marketing email" }),
    ).toBeInTheDocument();
  });
});
