/** @vitest-environment jsdom */
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { afterEach, describe, expect, it } from "vitest";
import { Button, Modal } from "./atoms";

// A dialog covers the page. `aria-modal` says so to a screen reader and does
// nothing for the Tab key, so these are the two keyboard obligations the
// attribute cannot discharge on its own.

afterEach(cleanup);

function Harness() {
  const [open, setOpen] = useState(false);
  return (
    <>
      <Button onClick={() => setOpen(true)}>Open</Button>
      <button type="button">Behind the dialog</button>
      <Modal open={open} onClose={() => setOpen(false)} labelledBy="t">
        <h2 id="t">Log activity</h2>
        <button type="button">First</button>
        <button type="button">Last</button>
      </Modal>
    </>
  );
}

describe("a dialog holds the keyboard", () => {
  it("moves focus in when it opens", async () => {
    render(<Harness />);
    await userEvent.click(screen.getByRole("button", { name: "Open" }));
    expect(document.activeElement).toBe(
      screen.getByRole("button", { name: "First" }),
    );
  });

  it("wraps Tab at the last stop instead of leaving for the page behind", async () => {
    render(<Harness />);
    await userEvent.click(screen.getByRole("button", { name: "Open" }));
    await userEvent.tab();
    expect(document.activeElement).toBe(
      screen.getByRole("button", { name: "Last" }),
    );
    await userEvent.tab();
    expect(document.activeElement).toBe(
      screen.getByRole("button", { name: "First" }),
    );
    expect(document.activeElement).not.toBe(
      screen.getByRole("button", { name: "Behind the dialog" }),
    );
  });

  it("wraps backwards from the first stop", async () => {
    render(<Harness />);
    await userEvent.click(screen.getByRole("button", { name: "Open" }));
    await userEvent.tab({ shift: true });
    expect(document.activeElement).toBe(
      screen.getByRole("button", { name: "Last" }),
    );
  });

  it("pulls Tab back in when focus is already outside, in either direction", async () => {
    render(<Harness />);
    await userEvent.click(screen.getByRole("button", { name: "Open" }));
    const behind = screen.getByRole("button", { name: "Behind the dialog" });

    // Something on the covered page took focus while the dialog was open. A
    // plain Tab from there would keep walking that page, so both directions
    // have to catch it — not only Shift+Tab.
    behind.focus();
    await userEvent.tab();
    expect(document.activeElement).toBe(
      screen.getByRole("button", { name: "First" }),
    );

    behind.focus();
    await userEvent.tab({ shift: true });
    expect(document.activeElement).toBe(
      screen.getByRole("button", { name: "Last" }),
    );
  });

  it("gives focus back to whatever opened it, so the reader keeps their place", async () => {
    render(<Harness />);
    const opener = screen.getByRole("button", { name: "Open" });
    await userEvent.click(opener);
    await userEvent.keyboard("{Escape}");
    expect(document.activeElement).toBe(opener);
  });
});
