/** @vitest-environment jsdom */
import "@testing-library/jest-dom/vitest";
import { act, cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
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
// a toast is timer-driven, and `userEvent` waits on timers of its own between
// the events that make up a click. Left on the real clock while the component
// is on a fake one, those two disagree and an awaited interaction can hang
// against a clock nothing is advancing. Installing fake timers for the whole
// suite and handing `advanceTimers` to the interaction driver puts both on the
// same clock, which is the only arrangement where neither waits on the other.
beforeEach(() => {
  vi.useFakeTimers();
  // Why a `jest` global exists in a vitest repo, and why it holds one method.
  //
  // @testing-library/react configures dom-testing-library's `asyncWrapper` to
  // drain the microtask queue by awaiting `setTimeout(resolve, 0)`, and it keeps
  // that from deadlocking under mocked timers by advancing the clock alongside —
  // but only `if (typeof jest !== "undefined")`, through
  // `jest.advanceTimersByTime`. There is no `jest` here, so the branch never
  // runs, the zero-delay timeout never fires, and EVERY awaited interaction
  // hangs until the test times out. It reads as a broken component rather than
  // as a missing global, which is what makes it worth naming.
  //
  // Installed per file rather than in vitest.setup.ts, and that is the load
  // bearing part: a suite that instead reaches for
  // `vi.useFakeTimers({ shouldAdvanceTime: true })` — several in this tree do,
  // and it is the other way out of the same deadlock — has a clock that moves
  // on its own, and advancing THAT one from inside `asyncWrapper` fires timers
  // it did not ask to fire. This file wants a clock that moves only when it
  // says so, because what it measures is a deadline.
  Object.assign(globalThis, {
    jest: {
      advanceTimersByTime: (ms: number) => {
        vi.advanceTimersByTime(ms);
      },
    },
  });
});

afterEach(() => {
  vi.useRealTimers();
  cleanup();
});

/**
 * The interaction driver, set up ONCE per test.
 *
 * A second `setup()` inside the same test starts a second view of the document's
 * input state — what is focused, which pointer buttons are held — and the two
 * then disagree, which shows up as an interaction that silently does nothing.
 */
const user = () => userEvent.setup({ advanceTimers: vi.advanceTimersByTime });

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
    const acting = user();
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
    const acting = user();
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
    const acting = user();
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
    const acting = user();
    const view = render(<Harness options={{ sticky: true }} />);
    await acting.click(press("show"));
    expect(view.container.querySelector(".toast-dismiss")).not.toBeNull();
  });

  it("gives a timed confirmation none", async () => {
    // A confirmation that withdraws itself needs no control: it is gone in three
    // and a half seconds, and a button beside it invites a decision about
    // something already decided.
    const acting = user();
    const view = render(<Harness />);
    await acting.click(press("show"));
    expect(view.container.querySelector(".toast-dismiss")).toBeNull();
  });

  it("marks a completion", async () => {
    const acting = user();
    const view = render(<Harness />);
    await acting.click(press("show"));
    expect(view.container.querySelector(".dot-auto")).not.toBeNull();
  });

  it("leaves a refusal unmarked", async () => {
    const acting = user();
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
    const acting = user();
    const view = render(<Harness />);
    await acting.click(press("show"));
    expect(vi.getTimerCount()).toBe(1);
    view.unmount();
    expect(vi.getTimerCount()).toBe(0);
  });
});
