/** @vitest-environment jsdom */
import { cleanup, render as rtlRender, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ConfirmModal } from "./confirmmodal";

// ConfirmModal is the extracted state-driven confirm-dialog shape that used
// to live duplicated inline in the deals.tsx terminal-stage advance confirm
// and archive.tsx's ArchiveAction. These specs pin the shared behaviour both
// call sites relied on: a Cancel/Confirm button pair, an optional autonomy
// dot before the title, an inline (not thrown) mutation error, and both
// buttons disabling while a mutation is pending.

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

  it("disables both buttons while pending", () => {
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
    expect((screen.getByText("Archive") as HTMLButtonElement).disabled).toBe(
      true,
    );
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
});
