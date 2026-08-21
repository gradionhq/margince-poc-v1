// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

/** @vitest-environment jsdom */
import "@testing-library/jest-dom/vitest";
import { cleanup, render } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { MarginceCoreScene, type MarginceCoreState } from "./margince-core";
import { WINDOW_BLURRED_ATTRIBUTE } from "./window-focus";

// jsdom's `getContext("webgl2")` always returns null, so every case here
// exercises the fallback path: the static dress the component wears on a host
// that cannot draw the shader at all, which is also what every other test in
// this tree renders when it mounts a Core. The live GL path is not faked here:
// a hand-rolled WebGL2 mock would prove something about the mock, not about a
// real context, so it is left to the browser this component actually runs in.

// vite.config.ts does not enable globals, so @testing-library's auto-cleanup
// never runs here: without this the rendered Core is never unmounted.
afterEach(cleanup);

/** Drives rAF by hand, so a "frame" is something this test decides to grant. */
function frameClock() {
  const pending = new Map<number, FrameRequestCallback>();
  let handle = 0;
  vi.spyOn(globalThis, "requestAnimationFrame").mockImplementation((cb) => {
    handle += 1;
    pending.set(handle, cb);
    return handle;
  });
  vi.spyOn(globalThis, "cancelAnimationFrame").mockImplementation((id) => {
    pending.delete(id);
  });
  return {
    /** How many frames are queued — the wake-ups nobody has spent yet. */
    queued: () => pending.size,
  };
}

/**
 * The closed vocabulary, written out rather than read off the behaviour table.
 *
 * A list derived from the table would pass on a table that had quietly lost a
 * state, which is the failure this case exists to catch. Typed, so a name that
 * is not a state does not compile.
 */
const STATES: readonly MarginceCoreState[] = [
  "idle",
  "ingest",
  "working",
  "warning",
  "error",
];

describe("the Core's engine, on a host without WebGL2", () => {
  let clock: ReturnType<typeof frameClock>;

  beforeEach(() => {
    clock = frameClock();
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("wears the static dress: still, not live, rim and glass in the markup", () => {
    const { container } = render(<MarginceCoreScene state="idle" />);
    const root = container.querySelector(".core");

    expect(root).toHaveClass("core-still");
    expect(root).not.toHaveClass("core-live");
    expect(container.querySelector(".core-rim")).toBeInTheDocument();
    expect(container.querySelector(".core-glass")).toBeInTheDocument();
  });

  it("carries data-core-state and aria-hidden on the root for every state", () => {
    // Other surfaces read the Core's state off this attribute, which has to
    // hold whether or not the GL path is live: the fallback is what a locked-
    // down browser and a lost context both fall back to, not a special case.
    for (const state of STATES) {
      const { container, unmount } = render(
        <MarginceCoreScene state={state} />,
      );
      const root = container.querySelector(".core");

      expect(root).toHaveAttribute("data-core-state", state);
      expect(root).toHaveAttribute("aria-hidden", "true");
      unmount();
    }
  });

  it("always puts the canvas in the markup, whether or not GL is live", () => {
    // A hole where the canvas should be would mean a context lost between two
    // frames has nowhere to come back into: the element stays, only the draw
    // loop stops.
    const { container } = render(<MarginceCoreScene state="idle" />);
    expect(container.querySelector("canvas.core-canvas")).toBeInTheDocument();
  });

  it("renders the progress ring only when progress is passed, and clamps it", () => {
    const bare = render(<MarginceCoreScene state="ingest" />);
    expect(bare.container.querySelector(".core-progress")).toBeNull();
    bare.unmount();

    const under = render(<MarginceCoreScene state="ingest" progress={-1} />);
    expect(
      under.container.querySelector(".core-progress-value"),
    ).toHaveAttribute("stroke-dasharray", "0 100");
    under.unmount();

    const over = render(<MarginceCoreScene state="ingest" progress={2} />);
    expect(
      over.container.querySelector(".core-progress-value"),
    ).toHaveAttribute("stroke-dasharray", "100 100");
    over.unmount();
  });

  it("asks for nothing once it is unmounted", () => {
    // The fallback path never has a loop to begin with, so this is the
    // invariant that stays true across both paths: nothing about mounting or
    // unmounting a Core leaves a frame request dangling.
    const view = render(<MarginceCoreScene state="working" />);
    expect(clock.queued()).toBe(0);
    view.unmount();
    expect(clock.queued()).toBe(0);
  });

  it("holds the window-focus signal live while a Core is mounted, GL or not", () => {
    render(<MarginceCoreScene state="idle" />);
    dispatchEvent(new Event("blur"));

    // The stylesheet's half of the same stillness, driven off this attribute
    // regardless of whether the engine has a GL context to run.
    expect(document.documentElement).toHaveAttribute(WINDOW_BLURRED_ATTRIBUTE);

    dispatchEvent(new Event("focus"));
    expect(document.documentElement).not.toHaveAttribute(
      WINDOW_BLURRED_ATTRIBUTE,
    );
  });
});
