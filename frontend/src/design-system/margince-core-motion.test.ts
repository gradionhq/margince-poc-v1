// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { describe, expect, it } from "vitest";
import {
  BEHAVIOUR,
  breath,
  type CoreMotion,
  DOTS,
  dotTarget,
} from "./margince-core-motion";

// The Core's state vocabulary is carried by MOVEMENT first (WDS-CORE-2), and a
// movement is the one thing a story cannot show and a screenshot cannot catch: on
// a still frame `ingesting` and `reasoning` are two greens. So the formations are
// asserted here as arithmetic — what a design note claims about a state ("two
// halves reaching across", "one mass", "a check mark") is checked against the
// numbers the renderer actually receives.
//
// Nothing here touches the DOM. `dotTarget` is pure, which is what makes the
// choreography testable at all: the engine's job is easing and scheduling, and
// that is asserted separately.

/** Every dot's placement for a state at one moment. */
function formation(motion: CoreMotion, time: number) {
  return Array.from({ length: DOTS }, (_, index) =>
    dotTarget(motion, index, time, breath(time)),
  );
}

/** How far apart the two furthest dots are — the formation's own width. */
function spread(motion: CoreMotion, time: number): number {
  const dots = formation(motion, time);
  let widest = 0;
  for (const a of dots) {
    for (const b of dots) {
      widest = Math.max(widest, Math.hypot(a.x - b.x, a.y - b.y));
    }
  }
  return widest;
}

/** A coarse sample of a whole cycle, since every formation is periodic. */
const SAMPLES = Array.from({ length: 60 }, (_, step) => 11.3 + step * 0.37);

describe("the Core's formations", () => {
  it("places every dot inside the ball, in every state, all cycle long", () => {
    // The dots live in a clipped well: a placement past the glass is not a
    // dramatic gesture, it is a dot vanishing at the edge and reappearing. The
    // bound is the working radius the motion table is written in, with the room
    // the largest excursion legitimately needs.
    for (const [state, behaviour] of Object.entries(BEHAVIOUR)) {
      for (const time of SAMPLES) {
        for (const dot of formation(behaviour.motion, time)) {
          expect(
            Math.hypot(dot.x, dot.y),
            `${state} at t=${time.toFixed(2)}`,
          ).toBeLessThanOrEqual(110);
        }
      }
    }
  });

  it("never scales a dot to nothing, or inside out", () => {
    // A zero scale is a dot that blinks out; a negative one flips it through
    // itself, which the goo filter renders as a hole in the mass. Both are the
    // kind of fault that only shows at one moment in a long cycle.
    for (const [state, behaviour] of Object.entries(BEHAVIOUR)) {
      for (const time of SAMPLES) {
        for (const dot of formation(behaviour.motion, time)) {
          expect(
            dot.sx,
            `${state} sx at t=${time.toFixed(2)}`,
          ).toBeGreaterThanOrEqual(0);
          expect(
            dot.sy,
            `${state} sy at t=${time.toFixed(2)}`,
          ).toBeGreaterThanOrEqual(0);
          // The ceiling is generous because `applied`'s sx is not a scale but a
          // LENGTH in dot widths — its long stroke is a little over three of
          // them. What this bound is really for is a runaway multiplication,
          // which shows up as a dot the size of the page rather than as one a
          // third too wide.
          expect(dot.sx).toBeLessThan(4);
          expect(dot.sy).toBeLessThan(4);
        }
      }
    }
  });

  it("gathers into one mass for error, and only for error", () => {
    // `fail` is the collapse: four dots in one place, no orbit, no resolution. If
    // another state gathered this tightly the two would be indistinguishable, and
    // one of them is the state that says the run is over.
    expect(spread("fail", 20)).toBeLessThan(1);
    for (const [state, behaviour] of Object.entries(BEHAVIOUR)) {
      if (behaviour.motion === "fail") {
        continue;
      }
      const widest = Math.max(
        ...SAMPLES.map((time) => spread(behaviour.motion, time)),
      );
      expect(widest, `${state} must not collapse`).toBeGreaterThan(20);
    }
  });

  it("splits disconnected into two halves that keep reaching", () => {
    // The design is two masses on opposite sides, closing and snapping — so the
    // two dots on a side stay on that side, and the gap between the sides has to
    // change over the cycle. A fixed gap would be a broken connection that never
    // tries.
    const gaps = SAMPLES.map((time) => {
      const dots = formation("severed", time);
      expect(dots[0].x).toBeLessThan(0);
      expect(dots[1].x).toBeLessThan(0);
      expect(dots[2].x).toBeGreaterThan(0);
      expect(dots[3].x).toBeGreaterThan(0);
      return dots[2].x - dots[1].x;
    });

    expect(Math.max(...gaps) - Math.min(...gaps)).toBeGreaterThan(20);
  });

  it("draws applied as strokes that grow and clear", () => {
    // The check mark is the one formation with rotation, and it has to RESET: a
    // mark that grows and stays is a badge, and the state is a moment. The short
    // stroke and the long one carry the two angles.
    const angles = new Set(
      formation("resolve", 12).map((dot) => Math.round(dot.rotation)),
    );
    expect(angles.has(45)).toBe(true);
    expect([...angles].some((angle) => angle < 0)).toBe(true);

    const lengths = SAMPLES.map((time) => formation("resolve", time)[1].sx);
    expect(Math.min(...lengths)).toBeLessThan(0.1);
    expect(Math.max(...lengths)).toBeGreaterThan(2);
  });

  it("holds flagged apart without ever orbiting it", () => {
    // Tension, not progress: the dots shiver around fixed angles. The test is
    // that no dot travels far — a formation that circles reads as work in flight,
    // which is the opposite of what this state says.
    const paths = Array.from({ length: DOTS }, (_, index) =>
      SAMPLES.map((time) => dotTarget("alert", index, time, 0.5)),
    );
    for (const [index, path] of paths.entries()) {
      const xs = path.map((dot) => dot.x);
      const ys = path.map((dot) => dot.y);
      const travel = Math.hypot(
        Math.max(...xs) - Math.min(...xs),
        Math.max(...ys) - Math.min(...ys),
      );
      expect(travel, `dot ${index} must stay put`).toBeLessThan(30);
    }
  });

  it("breathes unevenly, which is what reads as alive", () => {
    // A sine spends equal time either side of its midpoint; this curve does not,
    // and the asymmetry IS the effect — the inhale is quicker than the exhale. A
    // regression to a plain sine would look mechanical and pass any test that
    // only checked the range.
    const samples = Array.from({ length: 400 }, (_, step) =>
      breath(step * 0.05),
    );

    expect(Math.min(...samples)).toBeGreaterThanOrEqual(0);
    expect(Math.max(...samples)).toBeLessThanOrEqual(1);
    const above = samples.filter((value) => value > 0.5).length;
    expect(above / samples.length).toBeGreaterThan(0.5);
  });
});
