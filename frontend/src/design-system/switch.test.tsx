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

  // `reason` is the sentence for a change this reader may not make, so the
  // control it sits under has to be one they cannot flip. Spelling that as two
  // independent props left the refusal advisory: a caller stating why the
  // setting is locked, and no `disabled` beside it, shipped a live switch that
  // announced its own denial and then wrote anyway. `Button` reads the same
  // way — one contract, one spelling.
  describe("the refusal contract does not wait for a second prop", () => {
    it("refuses the flip on the reason alone, and says why from the control", () => {
      render(
        <Switch
          label="Auto-enrich"
          checked
          reason="Only an admin or ops can change this."
          onChange={() => undefined}
        />,
      );
      const control = screen.getByRole("switch", { name: "Auto-enrich" });
      expect(control.hasAttribute("disabled")).toBe(true);
      // Inert is half a refusal. A reader who cannot see the dimmed track has
      // only this pointer to the sentence explaining it.
      const described = control.getAttribute("aria-describedby");
      expect(described).toBeTruthy();
      expect(document.getElementById(described ?? "")?.textContent).toBe(
        "Only an admin or ops can change this.",
      );
    });

    it("writes nothing when a refused switch is clicked", async () => {
      const user = userEvent.setup();
      const onChange = vi.fn();
      render(
        <Switch
          label="Auto-enrich"
          checked
          reason="Only an admin or ops can change this."
          onChange={onChange}
        />,
      );
      await user.click(screen.getByRole("switch", { name: "Auto-enrich" }));
      expect(onChange).not.toHaveBeenCalled();
    });

    it("stays refused when the caller passes disabled={false}", () => {
      render(
        <Switch
          label="Auto-enrich"
          checked
          reason="Only an admin or ops can change this."
          disabled={false}
          onChange={() => undefined}
        />,
      );
      expect(
        screen
          .getByRole("switch", { name: "Auto-enrich" })
          .hasAttribute("disabled"),
      ).toBe(true);
    });
  });

  // The row language moved what used to be a switch's own `hint` into the ROW's
  // description, and a node-form control cannot see the id the row minted for
  // it — so without this the sentence saying what the setting DOES stopped
  // reaching anyone who cannot see the screen, which is the one guarantee the
  // pre-row spelling did make.
  describe("a description the caller owns", () => {
    it("names the caller's element alongside its own refusal", () => {
      render(
        <>
          <p id="what-it-does">Captured mail is readable by the team.</p>
          <Switch
            label="Email sharing"
            describedBy="what-it-does"
            checked
            reason="Only an admin or ops can change this."
            onChange={() => undefined}
          />
        </>,
      );
      const named = (
        screen.getByRole("switch").getAttribute("aria-describedby") ?? ""
      ).split(" ");
      // Both, and the caller's first: it describes the setting, and the refusal
      // qualifies it.
      expect(named[0]).toBe("what-it-does");
      expect(named).toHaveLength(2);
      expect(document.getElementById(named[1] ?? "")?.textContent).toBe(
        "Only an admin or ops can change this.",
      );
    });

    it("names it alone when the switch has nothing of its own to say", () => {
      render(
        <Switch
          label="Email sharing"
          describedBy="what-it-does"
          checked
          onChange={() => undefined}
        />,
      );
      expect(screen.getByRole("switch")).toHaveAttribute(
        "aria-describedby",
        "what-it-does",
      );
    });
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

    it("draws the mark beside the label, leaving the knob showing the state", () => {
      render(
        <Switch
          label="Auto-enrich"
          checked
          pending
          onChange={() => undefined}
        />,
      );
      const control = screen.getByRole("switch");
      // The knob is the only thing carrying the state the reader is changing
      // FROM, so covering it during the write would hide what they are moving
      // away from. The mark goes beside the label instead — and it is inside
      // the control, so both are on screen at once.
      expect(control).toHaveAttribute("aria-checked", "true");
      expect(control.querySelector(".busy-mark")).toBeTruthy();
      expect(control.querySelector(".switchknob")).toBeTruthy();
    });

    // Two spellings of refusal, and BOTH outrank a write in flight — tested
    // apart, because passing them together proves only that one of them works
    // and hides whichever is broken.
    it("lets a bare denial outrank it", () => {
      render(
        <Switch
          label="Auto-enrich"
          checked
          pending
          disabled
          onChange={() => undefined}
        />,
      );
      const control = screen.getByRole("switch", { name: "Auto-enrich" });
      expect(control.hasAttribute("disabled")).toBe(true);
      expect(control.hasAttribute("aria-busy")).toBe(false);
      expect(control.querySelector(".busy-mark")).toBeNull();
    });

    it("lets a stated reason outrank it, so no row says both things at once", () => {
      render(
        <Switch
          label="Auto-enrich"
          checked
          pending
          reason="Your seat cannot change this."
          onChange={() => undefined}
        />,
      );
      const control = screen.getByRole("switch", { name: "Auto-enrich" });
      // "Your seat cannot change this" and a turning mark in one row tells the
      // reader their write is going through AND that they were never allowed
      // to make it.
      expect(control.hasAttribute("aria-busy")).toBe(false);
      expect(control.querySelector(".busy-mark")).toBeNull();
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
