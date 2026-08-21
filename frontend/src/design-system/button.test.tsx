/** @vitest-environment jsdom */
import "@testing-library/jest-dom/vitest";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { Button } from "./atoms";

const here = dirname(fileURLToPath(import.meta.url));

// jsdom resolves no custom properties and computes no layout, so the geometry
// this component owns — the shared control height, the width floor, the icon
// size — is held by tokens.css and base.css rather than by anything below. What
// IS testable here is the contract those rules key on: which classes a given set
// of props emits, and the two attributes Button computes for itself and must not
// let a caller's props overwrite.

afterEach(cleanup);

function classesOf(name: string): string[] {
  return (screen.getByRole("button", { name }).className ?? "").split(" ");
}

describe("Button", () => {
  it("names its variant and size in the class list the stylesheet keys on", () => {
    render(
      <>
        <Button variant="primary">Save</Button>
        <Button variant="danger" small>
          Delete
        </Button>
      </>,
    );
    expect(classesOf("Save")).toContain("btn");
    expect(classesOf("Save")).toContain("btn-primary");
    expect(classesOf("Save")).not.toContain("btn-sm");
    expect(classesOf("Delete")).toContain("btn-danger");
    expect(classesOf("Delete")).toContain("btn-sm");
  });

  it("defaults to the ghost variant, so a bare Button is still a styled one", () => {
    render(<Button>Cancel</Button>);
    expect(classesOf("Cancel")).toContain("btn-ghost");
  });

  it("marks an icon-only button so it drops the width floor and turns square", () => {
    render(
      <Button iconOnly aria-label="Reconnect">
        <svg aria-hidden />
      </Button>,
    );
    expect(classesOf("Reconnect")).toContain("btn-icon");
  });

  it("keeps a caller's own class beside its own", () => {
    render(<Button className="connector-verb">Reconnect</Button>);
    expect(classesOf("Reconnect")).toEqual(
      expect.arrayContaining(["btn", "btn-ghost", "connector-verb"]),
    );
  });

  // The reason contract promises two things at once: the control is refused,
  // and the sentence saying why is announced from it. Both were defeatable,
  // because `{...rest}` was spread AFTER the computed attributes — so a caller
  // passing `disabled={false}` re-enabled a button the contract had refused,
  // and a caller passing its own `aria-describedby` dropped the pointer to the
  // explanation. A disabled control cannot be focused and a `title` on one is
  // announced by nobody, so losing that pointer loses the reason entirely.
  describe("the refusal contract survives the caller's props", () => {
    it("disables the button and points it at the sentence", () => {
      render(<Button reason="Connect an inbox first.">Send</Button>);
      const button = screen.getByRole("button", { name: "Send" });
      expect(button.hasAttribute("disabled")).toBe(true);
      const describedBy = button.getAttribute("aria-describedby");
      expect(describedBy).toBeTruthy();
      expect(document.getElementById(describedBy ?? "")?.textContent).toBe(
        "Connect an inbox first.",
      );
    });

    it("stays refused when the caller passes disabled={false}", () => {
      render(
        <Button reason="Connect an inbox first." disabled={false}>
          Send
        </Button>,
      );
      expect(
        screen.getByRole("button", { name: "Send" }).hasAttribute("disabled"),
      ).toBe(true);
    });

    it("keeps its own description when the caller passes one too", () => {
      render(
        <>
          <p id="caller-note">Something else entirely.</p>
          <Button
            reason="Connect an inbox first."
            aria-describedby="caller-note"
          >
            Send
          </Button>
        </>,
      );
      const describedBy = screen
        .getByRole("button", { name: "Send" })
        .getAttribute("aria-describedby");
      expect(describedBy).not.toBe("caller-note");
      expect(document.getElementById(describedBy ?? "")?.textContent).toBe(
        "Connect an inbox first.",
      );
    });

    it("points several refused controls at one sentence via reasonId", () => {
      render(
        <>
          <p id="seat-note">Your seat cannot send.</p>
          <Button reasonId="seat-note">Send</Button>
          <Button reasonId="seat-note">Schedule</Button>
        </>,
      );
      for (const name of ["Send", "Schedule"]) {
        const button = screen.getByRole("button", { name });
        expect(button.hasAttribute("disabled")).toBe(true);
        expect(button.getAttribute("aria-describedby")).toBe("seat-note");
      }
    });

    it("leaves a caller's aria-describedby alone when nothing is refused", () => {
      render(
        <>
          <p id="hint">Sends to everyone on the list.</p>
          <Button aria-describedby="hint">Send</Button>
        </>,
      );
      const button = screen.getByRole("button", { name: "Send" });
      expect(button.getAttribute("aria-describedby")).toBe("hint");
      expect(button.hasAttribute("disabled")).toBe(false);
    });
  });

  // A write in flight and a write you are not allowed to make are opposite
  // facts, and before `pending` existed the product spelled both as `disabled`:
  // the same dimmed, barred control, and the same detached focus. What is held
  // here is the difference — the reader keeps their place, the state is
  // announced from where they are standing, and the second press does not land.
  describe("the pending contract", () => {
    it("refuses the press without taking the button out of the tab order", () => {
      render(<Button pending>Save</Button>);
      const button = screen.getByRole("button", { name: "Save" });
      // Not `disabled`: that is what drops focus to <body> on the very click
      // that started the write, leaving the announcement with nobody on it.
      expect(button.hasAttribute("disabled")).toBe(false);
      expect(button).toHaveAttribute("aria-disabled", "true");
      expect(button).toHaveAttribute("aria-busy", "true");
      button.focus();
      expect(button).toHaveFocus();
    });

    it("keeps the label it had, so the accessible name does not move", () => {
      const { rerender } = render(<Button>Save</Button>);
      rerender(<Button pending>Save</Button>);
      // Found by the SAME name while busy. A caller that swapped in "Saving…"
      // would rename a control the reader is focused on, and a screen reader
      // re-reads a name that changes under it.
      expect(screen.getByRole("button", { name: "Save" })).toBeTruthy();
    });

    it("swallows a second press while the first write is out", async () => {
      const user = userEvent.setup();
      const onClick = vi.fn();
      render(
        <Button pending onClick={onClick}>
          Save
        </Button>,
      );
      await user.click(screen.getByRole("button", { name: "Save" }));
      expect(onClick).not.toHaveBeenCalled();
    });

    it("does not submit the form it sits in while a write is out", async () => {
      const user = userEvent.setup();
      const onSubmit = vi.fn((event: { preventDefault(): void }) =>
        event.preventDefault(),
      );
      render(
        // A `type="submit"` is the case an early return in the handler does not
        // cover: the browser posts on the click itself, so only
        // `preventDefault` stops a form going out twice.
        <form onSubmit={onSubmit}>
          <Button type="submit" pending>
            Save
          </Button>
        </form>,
      );
      await user.click(screen.getByRole("button", { name: "Save" }));
      expect(onSubmit).not.toHaveBeenCalled();
    });

    it("carries no busy attributes at all when nothing is in flight", () => {
      render(<Button>Save</Button>);
      const button = screen.getByRole("button", { name: "Save" });
      expect(button.hasAttribute("aria-busy")).toBe(false);
      expect(button.hasAttribute("aria-disabled")).toBe(false);
    });

    it("still calls the caller's handler once the write has landed", async () => {
      const user = userEvent.setup();
      const onClick = vi.fn();
      const { rerender } = render(
        <Button pending onClick={onClick}>
          Save
        </Button>,
      );
      rerender(<Button onClick={onClick}>Save</Button>);
      await user.click(screen.getByRole("button", { name: "Save" }));
      expect(onClick).toHaveBeenCalledTimes(1);
    });

    // A refused button was never pressed, so it cannot also be waiting for an
    // answer. If a caller says both, the refusal is the true one — drawing the
    // mark would claim a write nobody started.
    it("lets a reason outrank it, and refuses properly rather than busily", () => {
      render(
        <Button pending reason="Connect an inbox first.">
          Send
        </Button>,
      );
      const button = screen.getByRole("button", { name: "Send" });
      expect(button.hasAttribute("disabled")).toBe(true);
      expect(button.hasAttribute("aria-busy")).toBe(false);
    });

    // `iconOnly` documents TWO ways to name the control — an `aria-label` or a
    // visually-hidden child — and an earlier cut of this feature dropped the
    // children while busy, which took the second one with it and left a
    // focusable control with no accessible name (WCAG 4.1.2). The glyph gives
    // way in CSS now; the children stay.
    it("keeps an icon-only button's name when that name is a hidden child", () => {
      render(
        <Button iconOnly pending>
          <svg aria-hidden />
          <span className="sr-only">Reconnect</span>
        </Button>,
      );
      expect(screen.getByRole("button", { name: "Reconnect" })).toBeTruthy();
    });

    // A control nobody may press cannot also be mid-press. Getting this wrong
    // produced the exact failure the prop exists to prevent: a natively
    // disabled button — focus already gone — announcing itself busy.
    it("lets disabled outrank it, and never sets both", () => {
      render(
        <Button disabled pending>
          Save
        </Button>,
      );
      const button = screen.getByRole("button", { name: "Save" });
      expect(button.hasAttribute("disabled")).toBe(true);
      expect(button.hasAttribute("aria-busy")).toBe(false);
      expect(button.hasAttribute("aria-disabled")).toBe(false);
    });

    it("owns the busy attributes, so a caller cannot set them by hand", () => {
      render(
        <Button aria-busy="true" aria-disabled="true">
          Save
        </Button>,
      );
      const button = screen.getByRole("button", { name: "Save" });
      // Nothing is in flight, so nothing may claim it is. A caller's own
      // spelling of this state can only disagree with the component's.
      expect(button.hasAttribute("aria-busy")).toBe(false);
      expect(button.hasAttribute("aria-disabled")).toBe(false);
    });
  });

  // `aria-busy` is a legal global state on a button, but ARIA defines it as
  // permission for assistive tech to DEFER exposing a change — not as an
  // instruction to announce one. So a screen with something worth saying says
  // it through a description, which a reader focused on this control does hear,
  // rather than through a renamed button, which makes them re-hear the control.
  describe("busyLabel", () => {
    it("describes the wait without touching the accessible name", () => {
      render(
        <Button pending busyLabel="Signing in…">
          Sign in
        </Button>,
      );
      const button = screen.getByRole("button", { name: "Sign in" });
      const describedBy = button.getAttribute("aria-describedby");
      expect(describedBy).toBeTruthy();
      expect(document.getElementById(describedBy ?? "")?.textContent).toBe(
        "Signing in…",
      );
    });

    it("keeps the sentence OUTSIDE the button, or it would rename it", () => {
      render(
        <Button pending busyLabel="Signing in…">
          Sign in
        </Button>,
      );
      const button = screen.getByRole("button", { name: "Sign in" });
      // Anything rendered inside a button joins its accessible name, which is
      // the one thing holding the label steady was for.
      expect(button.textContent).toBe("Sign in");
    });

    it("holds the description element from the first render, and empties it", () => {
      const { rerender } = render(
        <Button busyLabel="Signing in…">Sign in</Button>,
      );
      const idle = screen.getByRole("button", { name: "Sign in" });
      // Present before the write, so the sentence is a CHANGE a screen reader
      // reads rather than a node arriving with its text already in it — which
      // is frequently missed.
      expect(idle.hasAttribute("aria-describedby")).toBe(false);
      const holder = document.querySelector(".btn-shell .sr-only");
      expect(holder?.textContent).toBe("");
      rerender(
        <Button pending busyLabel="Signing in…">
          Sign in
        </Button>,
      );
      expect(holder?.textContent).toBe("Signing in…");
    });
  });

  it("is type=button unless the caller asks for a submit", () => {
    render(
      <>
        <Button>Cancel</Button>
        <Button type="submit">Save</Button>
      </>,
    );
    expect(
      screen.getByRole("button", { name: "Cancel" }).getAttribute("type"),
    ).toBe("button");
    expect(
      screen.getByRole("button", { name: "Save" }).getAttribute("type"),
    ).toBe("submit");
  });

  // The federated door and its resting refusal. The sign-in surface draws both
  // dims at once — every provider goes pale while the password form beside it
  // writes, and a provider with nothing behind it is dead at the same moment —
  // so what matters is that they are two states rather than one selector tuned
  // at the other's expense.
  describe("the unavailable contract", () => {
    it("refuses the press and names the resting state for the stylesheet", () => {
      render(
        <Button variant="federated" unavailable>
          Continue with Microsoft
        </Button>,
      );
      const door = screen.getByRole("button", {
        name: "Continue with Microsoft",
      });
      expect(door).toBeDisabled();
      expect(classesOf("Continue with Microsoft")).toEqual(
        expect.arrayContaining(["btn", "btn-federated", "btn-unavailable"]),
      );
    });

    it("stays refused when the caller passes disabled={false}", () => {
      render(
        <Button variant="federated" unavailable disabled={false}>
          Continue with Microsoft
        </Button>,
      );
      expect(
        screen.getByRole("button", { name: "Continue with Microsoft" }),
      ).toBeDisabled();
    });

    it("outranks pending, so a door with nothing behind it never draws a mark", () => {
      const { container } = render(
        <Button variant="federated" unavailable pending>
          Continue with Microsoft
        </Button>,
      );
      const door = screen.getByRole("button", {
        name: "Continue with Microsoft",
      });
      expect(door).toBeDisabled();
      expect(door.getAttribute("aria-busy")).toBeNull();
      expect(container.querySelector(".busy-mark")).toBeNull();
    });

    it("adds no words to the name the caller gave it", () => {
      render(
        <Button variant="federated" unavailable>
          Anmeldung über Werk-IT
        </Button>,
      );
      expect(
        screen.getByRole("button", { name: "Anmeldung über Werk-IT" })
          .textContent,
      ).toBe("Anmeldung über Werk-IT");
    });
  });
});

// A swallowed rule body still parses and still paints, so the two promises the
// federated variant makes about somebody else's logo are asserted on the
// stylesheet itself rather than on the class list above.
describe("base.css draws the federated door without touching the mark", () => {
  it("recolours nothing about the provider mark, and only sizes it", () => {
    const css = readFileSync(join(here, "base.css"), "utf8");
    const rules = [...css.matchAll(/([^{}]*)\{([^}]*)\}/g)].filter(([, sel]) =>
      sel.includes(".provider-mark"),
    );
    expect(rules.length).toBe(1);
    const body = rules[0][2];
    // Sizing is ours — the mark's own proportions against the atom's 16px glyph
    // rule. Its colours are not: a fill, a stroke or a filter here would be this
    // sheet recolouring another company's logo, which is the whole reason
    // provider-mark.tsx is the one file the colour gates exempt by name.
    expect(body).toMatch(/inline-size:\s*var\(--providerMarkSize/);
    expect(body).not.toMatch(/fill|stroke|filter|color/);
  });

  it("fades the dead door deeper than the pale one, and later in the sheet", () => {
    const css = readFileSync(join(here, "base.css"), "utf8");
    const pale = /(?:^|\n)\.btn:disabled\s*\{([^}]*)\}/.exec(css);
    const dead = /(?:^|\n)\.btn-unavailable:disabled\s*\{([^}]*)\}/.exec(css);
    expect(pale).not.toBeNull();
    expect(dead).not.toBeNull();
    // Equal specificity, so the deeper fade wins by POSITION or not at all.
    expect((dead?.index ?? 0) > (pale?.index ?? 0)).toBe(true);
    expect(pale?.[1]).toMatch(/opacity:\s*0\.5/);
    expect(dead?.[1]).toMatch(/opacity:\s*0\.4/);
  });

  // The dead door is drawn by TWO rules — the fade and the border it gives up —
  // and they have to agree about when they apply. An enabled `.btn-unavailable`
  // is a state Button cannot emit, so neither may key on the class alone: the
  // border rule did, which left a pale box with no fade behind it as the drawing
  // of a state the component has no way to be in.
  it("keys the border it gives up on the same refusal as the fade", () => {
    const css = readFileSync(join(here, "base.css"), "utf8");
    const border = /(?:^|\n)(\.btn-federated\.btn-unavailable[^{]*)\{/.exec(
      css,
    );
    expect(border).not.toBeNull();
    expect(border?.[1].trim()).toBe(".btn-federated.btn-unavailable:disabled");
  });

  it("floors the door at the touch target on a fine pointer too", () => {
    const css = readFileSync(join(here, "base.css"), "utf8");
    const rule = /(?:^|\n)\.btn-federated\s*\{([^}]*)\}/.exec(css);
    expect(rule).not.toBeNull();
    // `--control-h` is 40px for a fine pointer and rises to 44 only for a coarse
    // one, and `.btn` pins a 1.25 line-height — so leaning on the shared height
    // alone lands this box at 41px on a mouse. The floor is declared here, and
    // `max()` keeps the shared height wherever it is the taller of the two.
    // The RENDERED height is what actually matters and jsdom cannot compute
    // `max()`; the login spec measures it. This asserts the declaration survives,
    // because a deletion here would only surface in that slower lane.
    expect(rule?.[1]).toMatch(
      /min-block-size:\s*max\(var\(--control-h\),\s*44px\)/,
    );
  });
});
