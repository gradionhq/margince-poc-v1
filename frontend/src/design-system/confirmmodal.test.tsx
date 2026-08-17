/** @vitest-environment jsdom */
import "@testing-library/jest-dom/vitest";
import { cleanup, render as rtlRender, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ConfirmModal } from "./confirmmodal";

// ConfirmModal is the extracted state-driven confirm-dialog shape that used
// to live duplicated inline in the deals.tsx terminal-stage advance confirm
// and archive.tsx's ArchiveAction. These specs pin the shared behaviour both
// call sites relied on: a Cancel/Confirm button pair, an optional autonomy
// dot before the title, an inline (not thrown) mutation error, and the two
// different ways the pair refuses a press while a mutation is out.

afterEach(cleanup);

describe("ConfirmModal", () => {
  it("renders nothing while closed", () => {
    rtlRender(
      <ConfirmModal
        open={false}
        onClose={vi.fn()}
        title="Archive this person?"
        confirmLabel="Archive"
        onConfirm={vi.fn()}
      >
        <p>Body copy</p>
      </ConfirmModal>,
    );
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("renders the title and body without a dot when tier is omitted", () => {
    rtlRender(
      <ConfirmModal
        open
        onClose={vi.fn()}
        title="Archive this person?"
        confirmLabel="Archive"
        onConfirm={vi.fn()}
      >
        <p>This cannot be undone.</p>
      </ConfirmModal>,
    );
    expect(screen.getByText("Archive this person?")).toBeTruthy();
    expect(screen.getByText("This cannot be undone.")).toBeTruthy();
    expect(document.querySelector(".dot")).toBeNull();
  });

  it("renders an autonomy dot before the title when tier is set", () => {
    rtlRender(
      <ConfirmModal
        open
        onClose={vi.fn()}
        title="Move to Won?"
        tier="confirm"
        confirmLabel="Confirm"
        onConfirm={vi.fn()}
      >
        <p>Moving this deal to a terminal stage.</p>
      </ConfirmModal>,
    );
    expect(document.querySelector(".dot-confirm")).toBeTruthy();
  });

  it("fires onConfirm when the confirm button is clicked", async () => {
    const onConfirm = vi.fn();
    rtlRender(
      <ConfirmModal
        open
        onClose={vi.fn()}
        title="Archive this person?"
        confirmLabel="Archive"
        onConfirm={onConfirm}
      >
        <p>Body copy</p>
      </ConfirmModal>,
    );
    await userEvent.click(screen.getByText("Archive"));
    expect(onConfirm).toHaveBeenCalledTimes(1);
  });

  it("fires onClose when the cancel button is clicked", async () => {
    const onClose = vi.fn();
    rtlRender(
      <ConfirmModal
        open
        onClose={onClose}
        title="Archive this person?"
        confirmLabel="Archive"
        onConfirm={vi.fn()}
      >
        <p>Body copy</p>
      </ConfirmModal>,
    );
    await userEvent.click(screen.getByText("Cancel"));
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("renders a danger-styled error message when error is set", () => {
    rtlRender(
      <ConfirmModal
        open
        onClose={vi.fn()}
        title="Archive this person?"
        confirmLabel="Archive"
        onConfirm={vi.fn()}
        error="archive failed"
      >
        <p>Body copy</p>
      </ConfirmModal>,
    );
    const message = screen.getByText("archive failed");
    expect(message.className).toContain("t-caption");
    expect(message.getAttribute("style")).toContain("var(--danger)");
  });

  it("renders no error paragraph when error is null", () => {
    rtlRender(
      <ConfirmModal
        open
        onClose={vi.fn()}
        title="Archive this person?"
        confirmLabel="Archive"
        onConfirm={vi.fn()}
        error={null}
      >
        <p>Body copy</p>
      </ConfirmModal>,
    );
    expect(screen.queryByText("archive failed")).toBeNull();
  });

  // Both buttons refuse the press while the act is in flight, and they refuse
  // it in two different ways because they are two different facts. Confirm
  // started the write and stays focusable so the reader keeps their place;
  // Cancel started nothing and is simply not available, since backing out of
  // something already on its way to the server would tell the reader they
  // stopped it when they did not.
  it("keeps the confirm focusable and busy, and takes Cancel away", () => {
    rtlRender(
      <ConfirmModal
        open
        onClose={vi.fn()}
        title="Archive this person?"
        confirmLabel="Archive"
        onConfirm={vi.fn()}
        pending
      >
        <p>Body copy</p>
      </ConfirmModal>,
    );
    expect((screen.getByText("Cancel") as HTMLButtonElement).disabled).toBe(
      true,
    );
    const confirm = screen.getByText("Archive") as HTMLButtonElement;
    expect(confirm.disabled).toBe(false);
    expect(confirm).toHaveAttribute("aria-disabled", "true");
    expect(confirm).toHaveAttribute("aria-busy", "true");
  });

  it("does not confirm a second time while the first is still out", async () => {
    const user = userEvent.setup();
    const onConfirm = vi.fn();
    rtlRender(
      <ConfirmModal
        open
        onClose={vi.fn()}
        title="Archive this person?"
        confirmLabel="Archive"
        onConfirm={onConfirm}
        pending
      >
        <p>Body copy</p>
      </ConfirmModal>,
    );
    await user.click(screen.getByText("Archive"));
    expect(onConfirm).not.toHaveBeenCalled();
  });

  it("leaves both buttons enabled when not pending", () => {
    rtlRender(
      <ConfirmModal
        open
        onClose={vi.fn()}
        title="Archive this person?"
        confirmLabel="Archive"
        onConfirm={vi.fn()}
      >
        <p>Body copy</p>
      </ConfirmModal>,
    );
    expect((screen.getByText("Cancel") as HTMLButtonElement).disabled).toBe(
      false,
    );
    expect((screen.getByText("Archive") as HTMLButtonElement).disabled).toBe(
      false,
    );
  });

  it("lets the caller gate the confirm while its own precondition is unmet", async () => {
    const onConfirm = vi.fn();
    rtlRender(
      <ConfirmModal
        open
        onClose={() => undefined}
        title="Fulfil erasure request"
        confirmLabel="Erase + suppress"
        confirmVariant="danger"
        confirmDisabled
        onConfirm={onConfirm}
      >
        <p>Type ERASE to confirm.</p>
      </ConfirmModal>,
    );

    const confirm = screen.getByRole("button", { name: "Erase + suppress" });
    expect((confirm as HTMLButtonElement).disabled).toBe(true);
    // An unmet precondition is not a write in flight. The two used to share
    // one `disabled` on this control, so "type ERASE first" was drawn exactly
    // like "your erasure is going through".
    expect(confirm.hasAttribute("aria-busy")).toBe(false);
    await userEvent.click(confirm);
    expect(onConfirm).not.toHaveBeenCalled();
  });

  it("leaves the confirm enabled when the caller sets no gate", () => {
    rtlRender(
      <ConfirmModal
        open
        onClose={() => undefined}
        title="Revoke"
        confirmLabel="Revoke"
        onConfirm={() => undefined}
      >
        <p>body</p>
      </ConfirmModal>,
    );
    expect(
      (screen.getByRole("button", { name: "Revoke" }) as HTMLButtonElement)
        .disabled,
    ).toBe(false);
  });
});
