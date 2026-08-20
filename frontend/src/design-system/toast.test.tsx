/** @vitest-environment jsdom */
import "@testing-library/jest-dom/vitest";
import { act, cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { steppedClock } from "../testing/steppedclock";
import { Button } from "./atoms";
import { type ToastOptions, ToastRegion, useToast } from "./toast";

// A harness rather than a hook-only test: the withdrawal is the behaviour worth
// pinning, and it is only observable through what is on screen.
//
// The triggers are the design-system `Button`, not native ones, because that is
// what shows a toast everywhere in the product: `Button` owns a pending state
// that stops taking clicks, and if it ever stopped delivering one the suite that
// notices should be the one whose whole subject is what a click puts on screen.
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
      <Button onClick={() => toast.show(message, options)}>show</Button>
      {second && (
        <Button onClick={() => toast.show(second, options)}>show second</Button>
      )}
      <Button onClick={toast.dismiss}>dismiss</Button>
      <ToastRegion toast={toast} />
    </>
  );
}

// The clock is driven in every test, not only the ones that watch a message go:
// what this suite measures is a deadline, and `userEvent` waits on timers of its
// own between the events that make up a click. `steppedClock` puts both on the
// same clock and carries the reason it takes doing.
afterEach(() => {
  vi.useRealTimers();
  cleanup();
});

const wait = (ms: number) => {
  act(() => {
    vi.advanceTimersByTime(ms);
  });
};

const press = (name: string) => screen.getByRole("button", { name });

describe("useToast", () => {
  it("says nothing until something is shown", () => {
    render(<Harness />);
    expect(screen.queryByRole("status")).toBeNull();
  });

  it("withdraws a confirmation on its own", async () => {
    const acting = steppedClock();
    render(<Harness />);
    await acting.click(press("show"));
    expect(screen.getByRole("status")).toHaveTextContent("Saved.");
    // Just short of the deadline it is still there — the point of the pair is
    // that the message is readable for a while, not that it eventually goes.
    wait(3400);
    expect(screen.getByRole("status")).toHaveTextContent("Saved.");
    wait(200);
    expect(screen.queryByRole("status")).toBeNull();
  });

  it("gives a second confirmation its own full life", async () => {
    // The defect this pins: a shared deadline. With the timer left running, the
    // first message's timeout fires while the second is on screen and takes it
    // down early — so a reader making two quick saves sees the second one blink.
    const acting = steppedClock();
    render(<Harness message="First." second="Second." />);
    await acting.click(press("show"));
    wait(3000);
    await acting.click(press("show second"));
    wait(1000);
    expect(screen.getByRole("status")).toHaveTextContent("Second.");
    wait(2600);
    expect(screen.queryByRole("status")).toBeNull();
  });

  it("keeps a sticky confirmation until it is dismissed", async () => {
    // What a toast carrying a verb needs: a reader reaching for Undo must not
    // lose it mid-reach.
    const acting = steppedClock();
    render(<Harness options={{ sticky: true }} />);
    await acting.click(press("show"));
    wait(30_000);
    expect(screen.getByRole("status")).toHaveTextContent("Saved.");
    await acting.click(press("dismiss"));
    expect(screen.queryByRole("status")).toBeNull();
  });

  it("gives a sticky confirmation its own way out", async () => {
    // Sticky means no timer, so a message whose body happens to be plain text
    // would otherwise stay on screen until its parent unmounted. The one sticky
    // caller today puts a verb in the body, but that is a property of that
    // caller's message rather than of this contract.
    const acting = steppedClock();
    const view = render(<Harness options={{ sticky: true }} />);
    await acting.click(press("show"));
    expect(view.container.querySelector(".toast-dismiss")).not.toBeNull();
  });

  it("gives a timed confirmation none", async () => {
    // A confirmation that withdraws itself needs no control: it is gone in three
    // and a half seconds, and a button beside it invites a decision about
    // something already decided.
    const acting = steppedClock();
    const view = render(<Harness />);
    await acting.click(press("show"));
    expect(view.container.querySelector(".toast-dismiss")).toBeNull();
  });

  it("marks a completion", async () => {
    const acting = steppedClock();
    const view = render(<Harness />);
    await acting.click(press("show"));
    expect(view.container.querySelector(".dot-auto")).not.toBeNull();
  });

  it("leaves a refusal unmarked", async () => {
    const acting = steppedClock();
    const view = render(
      <Harness message="That did not work." options={{ mark: false }} />,
    );
    await acting.click(press("show"));
    expect(screen.getByRole("status")).toHaveTextContent("That did not work.");
    expect(view.container.querySelector(".dot-auto")).toBeNull();
  });

  it("cancels its timer when the tree goes away", async () => {
    // The cleanup one of the three hand-copied toasts was missing. A settings
    // tab is exactly the screen a reader leaves right after saving, so the
    // orphaned timeout fired against an unmounted tree on every save they made.
    const acting = steppedClock();
    const view = render(<Harness />);
    await acting.click(press("show"));
    expect(vi.getTimerCount()).toBe(1);
    view.unmount();
    expect(vi.getTimerCount()).toBe(0);
  });
});
