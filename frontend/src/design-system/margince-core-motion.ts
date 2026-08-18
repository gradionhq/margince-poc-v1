// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { MarginceCoreState } from "./margince-core";

/**
 * Where the Core's four liquid dots want to be, per state, at a given moment.
 *
 * Pure: time and index in, a placement out. Nothing here touches the DOM, reads
 * layout or holds a frame, which is what lets the whole choreography be asserted
 * as arithmetic — `margince-core-motion.test.ts` checks that a state's formation
 * is the one its design says (four apart, two halves, one mass, a check mark)
 * instead of a screenshot review noticing weeks later that two states move the
 * same way.
 *
 * The engine (`margince-core-engine.ts`) eases toward these targets and the goo
 * filter fuses whatever overlaps, so merging and splitting are free: a state says
 * where the four masses are, and surface tension is the renderer's problem.
 *
 * MOTION, not colour, is what a state is made of here. A new state picks an
 * existing archetype where one fits; a new archetype is a deliberate addition,
 * because two states that move alike are two states a reader cannot tell apart.
 */

/** Dots in the Core. Four: enough to read as a group, few enough to count. */
export const DOTS = 4;

/** The working radius every placement below is expressed in, at the reference size. */
const R = 78;

/** A placement: centre offset, the two scale axes, and a rotation in degrees. */
export type DotTarget = Readonly<{
  x: number;
  y: number;
  sx: number;
  sy: number;
  rotation: number;
}>;

/** The movement archetypes. One state maps to exactly one of these. */
export type CoreMotion =
  | "calm"
  | "absorb"
  | "churn"
  | "emit"
  | "resolve"
  | "alert"
  | "severed"
  | "fail";

/**
 * What a state IS, in three numbers and a shape.
 *
 * `speed` scales the clock the motion is sampled on and `amp` the depth of the
 * breath, so two states can share an archetype and still be distinguishable —
 * `dormant` and a slower future rest state would differ by these alone. Colour
 * is NOT here: it is three custom properties per state in `margince-core.css`,
 * because the stylesheet is where a theme can reach it and a shader constant is
 * not.
 */
export type CoreBehaviour = Readonly<{
  motion: CoreMotion;
  speed: number;
  amp: number;
}>;

export const BEHAVIOUR: Readonly<Record<MarginceCoreState, CoreBehaviour>> = {
  // Nothing staged, nothing running. The Core sits here almost all the time, so
  // it is the one state tuned for being looked at past rather than at.
  dormant: { motion: "calm", speed: 0.45, amp: 0.28 },
  // Captured calls, mail and meetings arriving. Volume taken on, one at a time.
  ingesting: { motion: "absorb", speed: 1.1, amp: 1.0 },
  // Traversing the context graph. The fastest state, and the only one that spins.
  reasoning: { motion: "churn", speed: 2.2, amp: 0.95 },
  // Composing staged proposals: the mass pinches one off and sets it down.
  drafting: { motion: "emit", speed: 1.6, amp: 1.35 },
  // A human confirmed. The one state that should feel finished.
  applied: { motion: "resolve", speed: 1.2, amp: 1.1 },
  // Contradiction, or an action without the permission it needs. Held under
  // tension: fast, and deliberately going nowhere.
  flagged: { motion: "alert", speed: 2.6, amp: 0.6 },
  // A source the agent cannot reach. Two halves that keep reaching across.
  disconnected: { motion: "severed", speed: 0.7, amp: 0.5 },
  // The run failed. One mass, one slow heartbeat, nothing orbiting.
  error: { motion: "fail", speed: 2.0, amp: 0.8 },
};

/** Smoothstep on 0..1 — every eased fraction below runs through it. */
function ease(p: number): number {
  const c = Math.max(0, Math.min(1, p));
  return c * c * (3 - 2 * c);
}

function at(
  x: number,
  y: number,
  sx: number,
  sy = sx,
  rotation = 0,
): DotTarget {
  return { x, y, sx, sy, rotation };
}

/**
 * Rest, and the state the Core holds for hours at a time.
 *
 * One body, one rotation, no dot breaking rank: the life comes from the whole
 * cluster drifting on two slow frequencies and the ring tilting as though the
 * thing were turning in space, never from a dot doing something of its own. A
 * formation that reorganises while idle reads as an agent working, which is a
 * claim an idle CRM must not make.
 */
function calm(index: number, time: number, breathe: number): DotTarget {
  const angle = (index / DOTS) * Math.PI * 2;
  const spin = time * 0.42 + angle;
  // Never flat enough for the four to collapse into a lump.
  const tilt = 0.78 + Math.sin(time * 0.16) * 0.16;
  const driftX =
    Math.cos(time * 0.19) * R * 0.07 + Math.cos(time * 0.07) * R * 0.04;
  const driftY =
    Math.sin(time * 0.16) * R * 0.06 + Math.sin(time * 0.09) * R * 0.03;
  /*
   * The breath at rest is deliberately at the edge of noticing: the formation
   * swells by a few percent and the dots by a little less. Anything larger and
   * the Core looks like it is doing something, which is the one thing an idle
   * agent must not look like — and the drift and tilt above are already carrying
   * the life, so this only has to keep the body from being static.
   */
  const radius = R * (0.67 + breathe * 0.025);
  return at(
    Math.cos(spin) * radius + driftX,
    Math.sin(spin) * radius * tilt + driftY,
    0.86 + breathe * 0.05,
  );
}

/**
 * Intake. Satellites peel off the wall and dock into a central mass that swells
 * with every one it takes in, then compresses back down.
 *
 * One at a time, deliberately: four arriving together is a pulse, and a pulse
 * says nothing about how much came in. Arriving in sequence is what reads as
 * volume rather than as rhythm.
 */
function absorb(index: number, time: number): DotTarget {
  if (index === 0) {
    const cycle = (time * 0.42) % 1;
    const swell = cycle * 0.95 - ease((cycle - 0.82) / 0.18) * 0.95;
    const squeeze = ease((cycle - 0.84) / 0.16) * 0.24;
    return at(0, 0, 1.75 + swell + squeeze, 1.75 + swell - squeeze);
  }
  const satellite = index - 1;
  const satellites = DOTS - 1;
  const cycle =
    ((time * 0.42 * satellites + satellite) % satellites) / satellites;
  const travel = ease(cycle);
  const radius = R * 1.25 * (1 - travel);
  const angle = (satellite / satellites) * Math.PI * 2 + time * 0.35;
  // Eaten as it arrives, so docking is absorption and not a dot parking.
  const scale = 1.15 * (1 - travel * 0.72);
  return at(Math.cos(angle) * radius, Math.sin(angle) * radius, scale);
}

/** Reasoning: all four fuse into one working mass and spiral. */
function churn(index: number, time: number): DotTarget {
  const angle = (index / DOTS) * Math.PI * 2;
  const gather = Math.sin(time * 0.9) * 0.5 + 0.5;
  const radius = R * (0.12 + gather * 0.78);
  const spin = time * 2.2 + angle;
  return at(
    Math.cos(spin) * radius,
    Math.sin(spin) * radius,
    1.05 + (1 - gather) * 0.35,
  );
}

/**
 * Drafting: the mass pinches off one piece at a time and sends it out on a long
 * arc, stretching as it separates while the rest recoil and close again.
 *
 * The recoil is the point. A piece leaving a body that does not react is a dot
 * moving; a body that kicks back has mass, and mass is what makes the piece read
 * as having come OUT of something.
 */
function emit(index: number, time: number): DotTarget {
  const slot = Math.floor(time * 1.25) % DOTS;
  const p = (time * 1.25) % 1;
  if (index === slot) {
    const stretch = Math.sin(p * Math.PI);
    const shrink = 1 - ease((p - 0.8) / 0.2) * 0.55;
    return at(
      Math.sin(p * Math.PI) * R * 0.8,
      -R * 0.78 + p * R * 1.62,
      (1.2 - stretch * 0.34) * shrink,
      (1.2 + stretch * 0.62) * shrink,
    );
  }
  const kick = Math.max(0, 1 - p / 0.3) ** 2;
  const angle = (index / DOTS) * Math.PI * 2 + time * 0.9;
  const radius = R * (0.2 + kick * 0.16);
  return at(
    Math.cos(angle) * radius,
    Math.sin(angle) * radius - kick * 7,
    1.35 + kick * 0.3,
    1.35 - kick * 0.22,
  );
}

/**
 * Applied: the dots draw a check mark, short stroke then long, both growing out
 * of the bottom vertex — then it holds, clears and repeats.
 *
 * The only state that draws a SYMBOL rather than a formation, and the only one
 * the engine takes the goo filter off for: a fused check mark is a blob with
 * corners. It earns the exception by being the state that has to read as
 * finished, and nothing abstract reads as finished.
 */
function resolve(index: number, time: number): DotTarget {
  const cycle = (time * 0.34) % 1;
  const short = ease((cycle - 0.03) / 0.16);
  const long = ease((cycle - 0.17) / 0.22);
  const out = ease((cycle - 0.88) / 0.12);
  // A(-60,4) → B(-20,44) → C(62,-42): the vertex both strokes grow from.
  const vertexX = -20;
  const vertexY = 44;
  if (index === 0) {
    const length = 56.6 * short * (1 - out);
    return at(
      vertexX - (0.707 * length) / 2,
      vertexY - (0.707 * length) / 2,
      length / 38,
      1,
      45,
    );
  }
  if (index === 1) {
    const length = 118.9 * long * (1 - out);
    return at(
      vertexX + (0.69 * length) / 2,
      vertexY - (0.724 * length) / 2,
      length / 38,
      1,
      -46.4,
    );
  }
  return at(vertexX, vertexY, 0.52 * short * (1 - out), 1);
}

/** Flagged: held apart under tension, shivering, and never orbiting. */
function alert(index: number, time: number): DotTarget {
  const angle = (index / DOTS) * Math.PI * 2;
  const jitter = Math.sin(time * 16 + index * 2.4) * 0.06;
  const lock = angle + Math.sin(time * 2 + index) * 0.05;
  return at(
    Math.cos(lock) * R * (1.05 + jitter),
    Math.sin(lock) * R * (1.05 + jitter),
    0.8,
  );
}

/**
 * Disconnected: two halves that keep reaching for each other. The thread between
 * them stretches, thins, snaps, and they fall back.
 *
 * Slow pull, fast snap — the asymmetry is what makes it read as a connection
 * failing rather than as two masses bouncing.
 */
function severed(index: number, time: number): DotTarget {
  const cycle = (time * 0.5) % 1;
  const reach =
    cycle < 0.55 ? ease(cycle / 0.55) : 1 - ease((cycle - 0.55) / 0.14);
  const recoil =
    cycle > 0.55 && cycle < 0.75
      ? Math.sin(((cycle - 0.55) / 0.2) * Math.PI)
      : 0;
  const side = index < 2 ? -1 : 1;
  const gap = R * (0.92 - reach * 0.44) + recoil * 9;
  const scale = 0.86 - reach * 0.08;
  return at(
    side * gap,
    ((index % 2) - 0.5) * 30 * (1 - reach * 0.35),
    // Stretched toward the gap while reaching, so a neck forms and breaks.
    scale + reach * 0.42,
    scale - reach * 0.14,
  );
}

/** Error: all four collapse into one mass with a slow heartbeat. */
function fail(_index: number, time: number): DotTarget {
  const pulse = Math.sin(time * 2.2) * 0.5 + 0.5;
  return at(0, 0, 2.15 + pulse * 0.14);
}

/** The placement of one dot, for one state, at one moment. */
export function dotTarget(
  motion: CoreMotion,
  index: number,
  time: number,
  breathe: number,
): DotTarget {
  switch (motion) {
    case "absorb":
      return absorb(index, time);
    case "churn":
      return churn(index, time);
    case "emit":
      return emit(index, time);
    case "resolve":
      return resolve(index, time);
    case "alert":
      return alert(index, time);
    case "severed":
      return severed(index, time);
    case "fail":
      return fail(index, time);
    default:
      return calm(index, time, breathe);
  }
}

/**
 * The liquid's current at a given moment: where the body of fluid inside the
 * glass has drifted to, as a fraction of the working radius.
 *
 * Three frequencies per axis, none a multiple of another, so the fluid never
 * returns to the same place on a beat a viewer can learn. It is slow — an order
 * below the slowest formation — because liquid in a sealed ball moves because the
 * ball moved, not because the liquid wants to.
 *
 * The same value moves the liquid layer AND is added to every dot's placement,
 * which is the whole trick: the dots are not drawn on top of the fluid, they are
 * carried by it. Decouple the two and the ball reads as dots in front of a
 * texture — the thing this replaced.
 */
export function current(time: number): Readonly<{ x: number; y: number }> {
  return {
    x:
      Math.sin(time * 0.11) * 0.5 +
      Math.sin(time * 0.19 + 1.3) * 0.28 +
      Math.sin(time * 0.07 + 2.7) * 0.22,
    y:
      Math.cos(time * 0.13 + 0.6) * 0.46 +
      Math.cos(time * 0.23 + 2.1) * 0.24 +
      Math.sin(time * 0.05 + 4.2) * 0.3,
  };
}

/** Masses of liquid in the ball. Three: two can only ever pass, three mix. */
export const BLOBS = 3;

/**
 * Where one mass of liquid is, and how it is deformed, at a given moment.
 *
 * The fluid is not a texture that drifts — that was the first attempt and it
 * read as fog behind glass. It is a few large masses that travel on their own
 * long paths, overlap, and get STRETCHED along the direction they are moving,
 * which is what a liquid does and a gradient does not. The goo filter fuses them
 * where they meet, so the merging is surface tension rather than two shapes
 * crossing.
 *
 * Deliberately slower than any dot formation, and slower per mass than the
 * current that carries them: fluid in a sealed ball moves because the ball moved.
 * Each mass gets its own irrational-ish frequency pair so the three never line up
 * into a pattern a viewer can learn.
 */
export function liquidBlob(index: number, time: number): DotTarget {
  const phase = index * 2.1;
  const speed = 0.5 + index * 0.11;
  const slow = time * 0.07 * speed;
  const x =
    Math.sin(slow * 1.7 + phase) * 0.42 + Math.cos(slow * 0.9 + phase) * 0.2;
  const y =
    Math.cos(slow * 1.3 + phase * 1.4) * 0.4 +
    Math.sin(slow * 2.1 + phase) * 0.18;
  // The velocity, taken a beat later: the stretch has to follow where the mass is
  // GOING, and a mass that stretches across its own path reads as a wobbling blob
  // rather than as something moving through liquid.
  const ahead = slow + 0.12;
  const vx =
    homeX +
    Math.sin(ahead * 1.7 + phase) * 0.26 +
    Math.cos(ahead * 0.9 + phase) * 0.14 -
    x;
  const vy =
    homeY +
    Math.cos(ahead * 1.3 + phase * 1.4) * 0.24 +
    Math.sin(ahead * 2.1 + phase) * 0.12 -
    y;
  const pace = Math.min(1, Math.hypot(vx, vy) * 9);
  return {
    x: x * R,
    y: y * R,
    sx: 1 + pace * 0.26,
    sy: 1 - pace * 0.14,
    // Turned into its own travel, so the stretch lies along the path.
    rotation: (Math.atan2(vy, vx) * 180) / Math.PI,
  };
}

/**
 * The breath: two frequencies through an asymmetric curve, so the inhale is
 * quicker than the exhale.
 *
 * A single sine is a machine cycling; the uneven curve is what reads as alive.
 * Exported because the shell's own scale and opacity ride the same value — the
 * body and the dots inside it must breathe together or the glass looks loose.
 */
export function breath(time: number): number {
  const first = Math.sin(time * 0.85);
  const second = Math.sin(time * 0.37 + 1.7) * 0.45;
  return (((first + second) / 1.45) * 0.5 + 0.5) ** 0.72;
}
