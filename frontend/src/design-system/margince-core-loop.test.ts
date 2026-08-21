// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

/** @vitest-environment jsdom */

import { afterEach, describe, expect, it, vi } from "vitest";
import { runCoreLoop, type Wanted } from "./margince-core-engine";
import type { CoreFrame, CoreRenderer } from "./margince-core-gl";
import { FRAME_MS, rowFor } from "./margince-core-motion";

// The draw loop's contract with the GPU, which is a contract about WHEN rather
// than about pixels. `margince-core-engine.test.tsx` covers the same engine from
// the outside, on a host with no WebGL2 at all, and can therefore say nothing
// about a running loop: jsdom refuses the context, so no loop is ever built.
// Here the context is stood in for, because a stand-in is the only thing that
// can be asked how often it was drawn to. What it is NOT asked is how anything
// looks: every expectation below is a count or a comparison between two runs of
// the real code.

/** A renderer that keeps every frame it was handed, and when. */
function recorder(clock: () => number): CoreRenderer & {
  frames: CoreFrame[];
  at: number[];
} {
  const frames: CoreFrame[] = [];
  const at: number[] = [];
  return {
    frames,
    at,
    resize: () => {},
    draw: (frame) => {
      // The engine hands out its own live dials object, so a stored reference
      // would read the LAST frame's numbers from every slot in this array.
      frames.push({ ...frame, tintCol: [...frame.tintCol] });
      at.push(clock());
    },
    dispose: () => {},
  };
}

/**
 * Runs a loop for a stretch of time, granting frames at a fixed interval.
 *
 * The interval is the display's, which is the variable the budget exists to be
 * independent of: a 60 Hz screen offers frames twice as often as a 30 Hz one and
 * must still get the same animation out of the same wall-clock time.
 */
function runFor(
  ms: number,
  everyMs: number,
  target: Wanted,
): { frames: CoreFrame[]; at: number[]; parked: boolean } {
  // jsdom's document reports no focus, and the loop parks on an unfocused
  // window: without this it never asks for a frame at all, and every count
  // below would be zero for a reason that has nothing to do with the budget.
  vi.spyOn(document, "hasFocus").mockReturnValue(true);
  const canvas = document.createElement("canvas");
  let now = 0;
  const renderer = recorder(() => now);
  // The frames nobody has spent yet. A queue rather than one slot because the
  // loop only ever holds one request at a time, and a queue that grows past one
  // would be a defect this harness should not hide.
  const asked: FrameRequestCallback[] = [];
  vi.spyOn(globalThis, "requestAnimationFrame").mockImplementation((cb) => {
    asked.push(cb);
    return asked.length;
  });
  vi.spyOn(globalThis, "cancelAnimationFrame").mockImplementation(() => {
    asked.length = 0;
  });

  const wanted = { current: target };
  const loop = runCoreLoop(canvas, wanted, () => renderer);
  if (!loop) {
    throw new Error("the loop refused a renderer that was handed to it");
  }
  // The mount draw is not part of what the budget governs, and counting it as
  // one would make every draw total off by one.
  const mounted = renderer.frames.length;

  for (now = everyMs; now <= ms; now += everyMs) {
    const frame = asked.shift();
    if (!frame) {
      break;
    }
    frame(now);
  }
  const parked = asked.length === 0;
  loop.stop();
  return {
    frames: renderer.frames.slice(mounted),
    at: renderer.at.slice(mounted),
    parked,
  };
}

/** A state that keeps drifting, so the loop under test never parks early. */
const drifting = (): Wanted => ({ behaviour: rowFor("ingest"), paper: 0 });

afterEach(() => {
  vi.restoreAllMocks();
});

describe("the Core's draw budget", () => {
  it("draws about thirty times a second, not once per offered frame", () => {
    // The whole point of the budget: a fragment-heavy shader on a 120 Hz panel
    // costs four times what the motion needs, forever, on every screen.
    const { frames } = runFor(1000, 1000 / 60, drifting());

    expect(frames.length).toBeLessThan(40);
    expect(frames.length).toBeGreaterThan(20);
  });

  it("never draws twice inside one budget window, at any offered rate", () => {
    // The rule rather than a total, because a total is only the rule plus
    // whichever rounding the offered interval happens to impose: a display that
    // offers frames every 8ms cannot land a draw exactly on the budget, so it
    // waits out the next whole offer. Both bounds together say the loop draws as
    // soon as it may and never sooner.
    // Whole milliseconds, so no case sits exactly on the budget: a 33.333ms
    // offer against a 33.333ms budget decides on the last bit of a float, which
    // is a property of the arithmetic rather than of the loop.
    for (const everyMs of [8, 16, 40]) {
      const { at } = runFor(1000, everyMs, drifting());
      const gaps = at.slice(1).map((moment, i) => moment - (at[i] ?? 0));

      expect(gaps.length).toBeGreaterThan(5);
      for (const gap of gaps) {
        expect(gap).toBeGreaterThanOrEqual(FRAME_MS);
        expect(gap).toBeLessThan(FRAME_MS + everyMs);
      }
    }
  });
});

describe("the Core's motion, against the budget", () => {
  it("reaches the same point in the same time on a faster display", () => {
    // The defect this case exists for: easing once per DRAW rather than once per
    // frame made the orb take twice as long to reach a state on a 60 Hz screen
    // as the table is written for. The two runs cover the same second of wall
    // clock, so the last frame each drew has to describe the same object.
    const slow = runFor(1000, 1000 / 30, drifting()).frames.at(-1);
    const fast = runFor(1000, 1000 / 120, drifting()).frames.at(-1);
    if (!slow || !fast) {
      throw new Error("a drifting Core drew nothing over a whole second");
    }

    // Not equality: the two runs land their last frame at different moments
    // inside the same second, and one eased step apart is the honest tolerance.
    expect(fast.level).toBeCloseTo(slow.level, 2);
    expect(fast.ingest).toBeCloseTo(slow.ingest, 2);
    expect(fast.phase).toBeCloseTo(slow.phase, 1);
  });

  it("advances the phase under its own speed rather than by frame count", () => {
    const { frames } = runFor(1000, 1000 / 60, drifting());
    const first = frames[0];
    const last = frames.at(-1);
    if (!first || !last) {
      throw new Error("a drifting Core drew nothing over a whole second");
    }

    expect(last.phase).toBeGreaterThan(first.phase);
  });
});

describe("the Core's parking", () => {
  it("draws the frame it parks on, so a settled orb is never a stale one", () => {
    // A budget that skipped this frame would leave the last drawn position one
    // step short of where the object came to rest, and nothing would ever
    // correct it: the loop is parked, by definition.
    const settled: Wanted = { behaviour: rowFor("idle"), paper: 0 };
    const still: Wanted = {
      behaviour: { ...settled.behaviour, speed: 0 },
      paper: 0,
    };
    const { frames, parked } = runFor(1000, 1000 / 60, still);

    expect(parked).toBe(true);
    expect(frames.length).toBeGreaterThan(0);
  });

  it("keeps running while the state it was given still drifts", () => {
    expect(runFor(1000, 1000 / 60, drifting()).parked).toBe(false);
  });
});
