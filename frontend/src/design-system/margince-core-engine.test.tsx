// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

/** @vitest-environment jsdom */
import "@testing-library/jest-dom/vitest";
import { cleanup, render } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { MarginceCoreScene } from "./margince-core";
import { WINDOW_BLURRED_ATTRIBUTE } from "./window-focus";

// What this file counts is the Core's whole cost: the frames it DRAWS and the
// times it wakes the main thread to decide whether to. A reader sees one ball
// either way, so a count is the only thing that can fail when either regresses —
// and the prototype this engine came from requested frames forever, in every tab,
// which on a primitive that lives in permanent chrome is a timer nobody can
// switch off.

// vite.config.ts does not enable globals, so @testing-library's auto-cleanup
// never runs here: without this the rendered Core is never unmounted and its
// loop keeps running for the rest of the file.
afterEach(cleanup);

/** Drives rAF by hand, so a "frame" is something this test decides to grant. */
function frameClock() {
  // Keyed by handle, because `cancelAnimationFrame` has to actually cancel: with
  // a no-op stand-in the queue keeps a callback the engine already withdrew, and
  // "asks for nothing more once unmounted" would fail against a correct teardown.
  const pending = new Map<number, FrameRequestCallback>();
  let handle = 0;
  let now = 0;
  // The engine seeds `last` from performance.now() and reads the frame's own
  // timestamp here, so both have to come off ONE clock. Left unstubbed, every
  // frame computes a negative delta against the real clock and the easing runs
  // backwards — which the assertions below would not notice, because they only
  // ask whether a value moved.
  vi.spyOn(performance, "now").mockImplementation(() => now);
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
    /** Runs every queued callback once, advancing the clock a frame's worth. */
    tick: (times = 1) => {
      for (let step = 0; step < times; step += 1) {
        const due = [...pending.entries()];
        pending.clear();
        now += 16;
        for (const [, cb] of due) {
          cb(now);
        }
      }
    },
  };
}

function dotTransforms(): string[] {
  return [...document.querySelectorAll<HTMLElement>(".core-dot")].map(
    (dot) => dot.style.transform,
  );
}

describe("the Core's engine", () => {
  let clock: ReturnType<typeof frameClock>;

  beforeEach(() => {
    clock = frameClock();
    // jsdom's document never has focus, and an unfocused window parks the Core
    // (see window-focus.ts) — so left alone every case here would measure a
    // parked loop. The default fixture is a window somebody is looking at; the
    // cases about losing focus say so themselves.
    vi.spyOn(document, "hasFocus").mockReturnValue(true);
    // Nothing in jsdom has a size, so the engine would measure a 0px ball. The
    // dots are placed as a FRACTION of it, and at zero every placement collapses
    // to the middle — which is the one arrangement no assertion could tell from a
    // broken engine.
    vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockReturnValue({
      width: 172,
      height: 172,
      left: 40,
      top: 60,
      right: 212,
      bottom: 232,
      x: 40,
      y: 60,
      toJSON: () => ({}),
    });
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("places the dots apart, so the ball is a formation and not a lump", () => {
    render(<MarginceCoreScene state="dormant" feed={false} />);
    clock.tick();

    const transforms = dotTransforms();
    expect(transforms).toHaveLength(4);
    expect(new Set(transforms).size).toBe(4);
    for (const transform of transforms) {
      expect(transform).toMatch(/translate3d/);
    }
  });

  it("moves the liquid the dots are suspended in", () => {
    render(<MarginceCoreScene state="dormant" feed={false} />);
    clock.tick();
    const first = [...document.querySelectorAll<HTMLElement>(".core-blob")].map(
      (blob) => blob.style.transform,
    );
    clock.tick(30);
    const later = [...document.querySelectorAll<HTMLElement>(".core-blob")].map(
      (blob) => blob.style.transform,
    );

    expect(first).toHaveLength(3);
    expect(later).not.toEqual(first);
  });

  it("keeps asking for frames while somebody is looking", () => {
    render(<MarginceCoreScene state="reasoning" feed={false} />);
    clock.tick(5);

    // One frame in flight at a time: a loop that queued two would run at double
    // rate and cost double, and the count is the only place that shows.
    expect(clock.queued()).toBe(1);
  });

  it("stops in a hidden tab and resumes when it is shown again", () => {
    render(<MarginceCoreScene state="reasoning" feed={false} />);
    clock.tick(2);
    const parked = dotTransforms();

    vi.spyOn(document, "hidden", "get").mockReturnValue(true);
    clock.tick(3);

    // Nothing queued and nothing moved: STOPPED, not throttled.
    expect(clock.queued()).toBe(0);
    expect(dotTransforms()).toEqual(parked);

    vi.spyOn(document, "hidden", "get").mockReturnValue(false);
    document.dispatchEvent(new Event("visibilitychange"));
    clock.tick(2);

    expect(dotTransforms()).not.toEqual(parked);
  });

  it("draws one frame even where it will immediately park", () => {
    vi.spyOn(document, "hasFocus").mockReturnValue(false);
    render(<MarginceCoreScene state="dormant" feed={false} />);
    clock.tick(4);

    // The frame is OWED: an unpainted dot sits at the element's origin, so
    // parking before one leaves four dots stacked in the middle of the glass —
    // which is what a broken Core looks like.
    for (const transform of dotTransforms()) {
      expect(transform).toMatch(/translate3d/);
    }
    expect(clock.queued()).toBe(0);
  });

  it("marks the document while the window has no focus", () => {
    render(<MarginceCoreScene state="dormant" feed={false} />);
    dispatchEvent(new Event("blur"));

    // The stylesheet's half of the same stillness: the sheen and the feed pause
    // off this attribute, so the ball and its glow can never disagree about
    // whether the Core is moving.
    expect(document.documentElement).toHaveAttribute(WINDOW_BLURRED_ATTRIBUTE);

    dispatchEvent(new Event("focus"));
    expect(document.documentElement).not.toHaveAttribute(
      WINDOW_BLURRED_ATTRIBUTE,
    );
  });

  it("asks for nothing more once it is unmounted", () => {
    const view = render(<MarginceCoreScene state="reasoning" feed={false} />);
    clock.tick(2);
    view.unmount();
    clock.tick(3);

    expect(clock.queued()).toBe(0);
  });
});
