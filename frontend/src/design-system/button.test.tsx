/** @vitest-environment jsdom */
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
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
