/** @vitest-environment jsdom */
import "@testing-library/jest-dom/vitest";
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { Button } from "./atoms";

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

    it("replaces an icon-only button's glyph rather than crowding it", () => {
      const { rerender } = render(
        <Button iconOnly aria-label="Reconnect">
          <svg data-testid="verb-glyph" aria-hidden />
        </Button>,
      );
      expect(screen.getByTestId("verb-glyph")).toBeTruthy();
      rerender(
        <Button iconOnly pending aria-label="Reconnect">
          <svg data-testid="verb-glyph" aria-hidden />
        </Button>,
      );
      // One 16px mark in a square control, not two. The name still comes from
      // `aria-label`, so nothing the reader relies on went with the glyph.
      expect(screen.queryByTestId("verb-glyph")).toBeNull();
      expect(
        screen
          .getByRole("button", { name: "Reconnect" })
          .querySelectorAll("svg"),
      ).toHaveLength(1);
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
});
