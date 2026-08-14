/** @vitest-environment jsdom */
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useRef, useState } from "react";
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

// The case the plain restore above cannot serve: the dialog's own mutation
// removes the control that opened it. focus() on a detached node is a silent
// no-op, so focus lands on <body> and the next Tab restarts at the top of the
// document — the reader's place is lost by the action succeeding.
describe("a dialog whose mutation removes its own opener", () => {
  // The member row shape this exists for: Deactivate is replaced by Reactivate,
  // and the row survives both.
  function RowHarness({ named }: Readonly<{ named: boolean }>) {
    const [open, setOpen] = useState(false);
    const [off, setOff] = useState(false);
    const row = useRef<HTMLLIElement | null>(null);
    return (
      <ul>
        <li ref={row} tabIndex={-1}>
          Ada Active
          {/* Two slots rather than one ternary, as the member row spells it: a
              ternary would let React reuse the same <button> node for both, and
              a reused node is never the detached opener this is about. */}
          {!off && <Button onClick={() => setOpen(true)}>Deactivate</Button>}
          {off && <Button onClick={() => setOff(false)}>Reactivate</Button>}
        </li>
        <Modal
          open={open}
          onClose={() => setOpen(false)}
          labelledBy="row-h"
          returnFocusTo={named ? () => row.current : undefined}
        >
          <h2 id="row-h">Deactivate Ada Active?</h2>
          <Button
            onClick={() => {
              setOff(true);
              setOpen(false);
            }}
          >
            Confirm
          </Button>
        </Modal>
      </ul>
    );
  }

  it("hands focus to the named target once the opener is gone", async () => {
    render(<RowHarness named />);
    await userEvent.click(screen.getByRole("button", { name: "Deactivate" }));
    await userEvent.click(screen.getByRole("button", { name: "Confirm" }));

    const row = screen.getByRole("listitem");
    expect(document.activeElement).toBe(row);
    // Named, not merely "somewhere in the row": the row reads back the member
    // and the status the confirm just changed, which is why it is the target.
    expect(row.textContent).toContain("Ada Active");
  });

  it("drops focus to the document when nothing is named — the failure this fixes", async () => {
    render(<RowHarness named={false} />);
    await userEvent.click(screen.getByRole("button", { name: "Deactivate" }));
    await userEvent.click(screen.getByRole("button", { name: "Confirm" }));

    // Nothing here can honestly take focus back: the opener no longer exists,
    // which is exactly the state a caller passes returnFocusTo to answer.
    expect(screen.queryByRole("button", { name: "Deactivate" })).toBeNull();
    expect(document.activeElement).toBe(document.body);
  });

  function PrecedenceHarness({
    resolve,
  }: Readonly<{ resolve: () => HTMLElement | null }>) {
    const [open, setOpen] = useState(false);
    return (
      <>
        <Button onClick={() => setOpen(true)}>Open</Button>
        <Button>Elsewhere</Button>
        <Modal
          open={open}
          onClose={() => setOpen(false)}
          labelledBy="prec-h"
          returnFocusTo={resolve}
        >
          <h2 id="prec-h">Confirm</h2>
          <Button>Confirm</Button>
        </Modal>
      </>
    );
  }

  // A caller names a target because the mutation unmakes the opener, and the
  // unmaking usually lands with the refetch a moment AFTER the dialog closes.
  // Preferring a still-attached opener would restore focus to a button that is
  // about to be removed, which is the same lost place one tick later.
  it("prefers the named target over an opener that is still attached", async () => {
    render(
      <PrecedenceHarness
        resolve={() => screen.getByRole("button", { name: "Elsewhere" })}
      />,
    );
    await userEvent.click(screen.getByRole("button", { name: "Open" }));
    await userEvent.keyboard("{Escape}");
    expect(document.activeElement).toBe(
      screen.getByRole("button", { name: "Elsewhere" }),
    );
  });

  it("falls back to the opener when the named target is not in the document", async () => {
    // A resolver can answer with a node the DOM no longer holds — a row the
    // refetch dropped. Focusing it would be the same silent no-op, so the
    // opener, which is still there, gets the focus instead.
    const detached = document.createElement("button");
    render(<PrecedenceHarness resolve={() => detached} />);
    const opener = screen.getByRole("button", { name: "Open" });
    await userEvent.click(opener);
    await userEvent.keyboard("{Escape}");
    expect(document.activeElement).toBe(opener);
  });
});

// A right-anchored dialog is the same dialog: same portal, same Esc, same
// trap. Only where it sits changes.
describe("a drawer is a dialog anchored to the right edge", () => {
  it("keeps the dialog role and the Escape close", async () => {
    function DrawerHarness() {
      const [open, setOpen] = useState(true);
      return (
        <Modal
          open={open}
          onClose={() => setOpen(false)}
          labelledBy="d"
          placement="right"
        >
          <h2 id="d">Write email</h2>
        </Modal>
      );
    }
    render(<DrawerHarness />);
    const drawer = screen.getByRole("dialog", { name: "Write email" });
    expect(drawer.classList.contains("modal-drawer")).toBe(true);
    await userEvent.keyboard("{Escape}");
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  // The width of a drawer comes from the viewport, so the centred-box size
  // variants must not also apply — two width rules would fight.
  it("ignores the centred-box size variant", () => {
    render(
      <Modal
        open
        onClose={() => {}}
        labelledBy="d"
        placement="right"
        size="wide"
      >
        <h2 id="d">Evidence</h2>
      </Modal>,
    );
    const dialog = screen.getByRole("dialog", { name: "Evidence" });
    expect(dialog.classList.contains("modal-drawer")).toBe(true);
    expect(dialog.classList.contains("modal-wide")).toBe(false);
  });
});
