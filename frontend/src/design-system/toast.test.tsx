/** @vitest-environment jsdom */
import "@testing-library/jest-dom/vitest";
import { act, cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { type ToastOptions, ToastRegion, useToast } from "./toast";

// A harness rather than a hook-only test: the withdrawal is the behaviour worth
// pinning, and it is only observable through what is on screen.
function Harness({
  message = "Saved.",
  options,
  second,
}: Readonly<{
  message?: string;
  options?: ToastOptions;
  second?: string;
}>) {
  const toast = useToast();
  return (
    <>
      <button type="button" onClick={() => toast.show(message, options)}>
        show
      </button>
      {second && (
        <button type="button" onClick={() => toast.show(second, options)}>
          show second
        </button>
      )}
      <button type="button" onClick={toast.dismiss}>
        dismiss
      </button>
      <ToastRegion toast={toast} />
    </>
  );
}

const press = (name: string) => {
  act(() => {
    screen.getByRole("button", { name }).click();
  });
};

const wait = (ms: number) => {
  act(() => {
    vi.advanceTimersByTime(ms);
  });
};

afterEach(() => {
  vi.useRealTimers();
  cleanup();
});

describe("useToast", () => {
  it("says nothing until something is shown", () => {
    render(<Harness />);
    expect(screen.queryByRole("status")).toBeNull();
  });

  it("withdraws a confirmation on its own", () => {
    vi.useFakeTimers();
    render(<Harness />);
    press("show");
    expect(screen.getByRole("status")).toHaveTextContent("Saved.");
    // Just short of the deadline it is still there — the point of the pair is
    // that the message is readable for a while, not that it eventually goes.
    wait(3400);
    expect(screen.getByRole("status")).toHaveTextContent("Saved.");
    wait(200);
    expect(screen.queryByRole("status")).toBeNull();
  });

  it("gives a second confirmation its own full life", () => {
    // The defect this pins: a shared deadline. With the timer left running, the
    // first message's timeout fires while the second is on screen and takes it
    // down early — so a reader making two quick saves sees the second one blink.
    vi.useFakeTimers();
    render(<Harness message="First." second="Second." />);
    press("show");
    wait(3000);
    press("show second");
    wait(1000);
    expect(screen.getByRole("status")).toHaveTextContent("Second.");
    wait(2600);
    expect(screen.queryByRole("status")).toBeNull();
  });

  it("keeps a sticky confirmation until it is dismissed", () => {
    // What a toast carrying a verb needs: a reader reaching for Undo must not
    // lose it mid-reach.
    vi.useFakeTimers();
    render(<Harness options={{ sticky: true }} />);
    press("show");
    wait(30_000);
    expect(screen.getByRole("status")).toHaveTextContent("Saved.");
    press("dismiss");
    expect(screen.queryByRole("status")).toBeNull();
  });

  it("marks a completion and leaves a refusal unmarked", () => {
    const done = render(<Harness />);
    press("show");
    expect(done.container.querySelector(".dot-auto")).not.toBeNull();
    cleanup();

    render(<Harness message="That did not work." options={{ mark: false }} />);
    press("show");
    expect(screen.getByRole("status")).toHaveTextContent("That did not work.");
    expect(document.querySelector(".dot-auto")).toBeNull();
  });

  it("cancels its timer when the tree goes away", () => {
    // The cleanup one of the three hand-copied toasts was missing. A settings
    // tab is exactly the screen a reader leaves right after saving, so the
    // orphaned timeout fired against an unmounted tree on every save they made.
    vi.useFakeTimers();
    const view = render(<Harness />);
    press("show");
    expect(vi.getTimerCount()).toBe(1);
    view.unmount();
    expect(vi.getTimerCount()).toBe(0);
  });
});
