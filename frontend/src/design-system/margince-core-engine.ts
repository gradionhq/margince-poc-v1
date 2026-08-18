// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { useEffect, useRef } from "react";
import type { MarginceCoreState } from "./margince-core";
import {
  BEHAVIOUR,
  breath,
  type CoreMotion,
  current,
  DOTS,
  dotTarget,
} from "./margince-core-motion";
import { usePrefersReducedMotion } from "./motion";
import {
  isWindowFocused,
  retainWindowFocusSignal,
  subscribeToWindowFocus,
} from "./window-focus";

/**
 * The Core's engine: it moves four dots and the shell they sit in, and nothing
 * else.
 *
 * Internal to `MarginceCoreScene` — the placements come from
 * `margince-core-motion.ts` (pure), the material comes from the stylesheet, and
 * what lives here is the part that has to be correct rather than pretty: WHEN a
 * frame happens, and what it is allowed to touch.
 *
 * Transform and opacity only, no layout reads inside the loop, and every
 * measurement (the orb's box, its centre, the dot size) taken from an event that
 * can change it — a resize, a scroll — never from the frame.
 *
 * It STOPS, rather than throttling, whenever nothing would change: a hidden tab,
 * a window without focus, an orb scrolled off screen, and reduced motion, where
 * one composed frame IS the whole animation. Each of those ends with an event, so
 * the loop is woken by that event rather than by asking every frame. The
 * prototype this is ported from ran one rAF per orb at module scope forever and
 * kept requesting frames in a hidden tab; on a page that shows the Core in
 * permanent chrome, that is a timer nobody can switch off.
 */

/** The size every constant in the motion table was tuned at. */
const REFERENCE_PX = 300;
/** The dot diameter at that size, which the line mode measures against. */
const REFERENCE_DOT_PX = 38;

type Dot = {
  readonly el: HTMLElement;
  x: number;
  y: number;
  sx: number;
  sy: number;
  rotation: number;
};

/** What the loop needs from the DOM, re-read only when an event says it changed. */
type Box = {
  /** Size relative to REFERENCE_PX, so one motion table drives every size. */
  scale: number;
  /** The measured width, which the fusion blur is derived from. */
  width: number;
  dotPx: number;
  /** Widest excursion still inside the shell, given optically enlarged dots. */
  spread: number;
  centreX: number;
  centreY: number;
  radius: number;
};

/**
 * The fusion blur for an orb this wide, in the filter's own user units.
 *
 * One formula rather than a filter per breakpoint. The relationship is not
 * linear: the prototype hand-tuned 6 at 300px, 2.4 at 64 and 1.1 at 34, which is
 * a blur that grows as a SHARE of the orb as the orb shrinks — small orbs carry
 * proportionally larger dots, so they need proportionally more blur to fuse at
 * all. This reproduces those three within a tenth and, unlike them, answers for
 * the sizes in between.
 */
function fusionBlur(width: number): number {
  const share = 0.02 * (1 + 0.9 * Math.max(0, 1 - width / REFERENCE_PX));
  return Math.max(0.9, width * share);
}

function measure(shell: HTMLElement, dot: HTMLElement | undefined): Box {
  const rect = shell.getBoundingClientRect();
  const scale = rect.width / REFERENCE_PX;
  const dotPx = dot?.offsetWidth || REFERENCE_DOT_PX * scale;
  // Small orbs carry proportionally LARGER dots (a dot that is right at 300px is
  // a hairline at 34px), so the formation has to tighten or the excursions push
  // the dots through the glass. The square root is the compromise that keeps the
  // gaps reading as gaps.
  const enlarged = dotPx / (REFERENCE_DOT_PX * scale || 1);
  return {
    scale,
    width: rect.width,
    dotPx,
    spread: Math.min(1.22, Math.sqrt(enlarged)),
    centreX: rect.left + rect.width / 2,
    centreY: rect.top + rect.height / 2,
    radius: rect.width / 2,
  };
}

/**
 * Whether the orb is on screen, reported as it changes.
 *
 * A missing or throwing IntersectionObserver must leave the engine RUNNING:
 * failing the other way freezes the dots wherever they stood, which is
 * indistinguishable from a broken Core.
 */
function observeOnScreen(
  shell: HTMLElement,
  onChange: (onScreen: boolean) => void,
): IntersectionObserver | null {
  if (typeof IntersectionObserver === "undefined") {
    return null;
  }
  try {
    const observer = new IntersectionObserver((entries) => {
      const latest = entries[entries.length - 1];
      if (latest) {
        onChange(latest.isIntersecting);
      }
    });
    observer.observe(shell);
    return observer;
  } catch {
    return null;
  }
}

/** Re-measure on resize, where the host has an observer for it. */
function observeSize(
  shell: HTMLElement,
  onResize: () => void,
): ResizeObserver | null {
  if (typeof ResizeObserver === "undefined") {
    return null;
  }
  try {
    const observer = new ResizeObserver(onResize);
    observer.observe(shell);
    return observer;
  } catch {
    return null;
  }
}

const lerp = (from: number, to: number, k: number) => from + (to - from) * k;

/**
 * The check mark draws real strokes, so the goo filter comes off and the dots
 * become bars. Switched on a state change only, never per frame — swapping a
 * filter is a repaint of the whole subtree.
 */
function setLineMode(shell: HTMLElement, dots: readonly Dot[], on: boolean) {
  shell.classList.toggle("core-strokes", on);
  if (on) {
    return;
  }
  for (const dot of dots) {
    dot.el.style.width = "";
    dot.el.style.height = "";
    dot.el.style.margin = "";
  }
}

function writeDot(dot: Dot, box: Box, lineMode: boolean) {
  if (!lineMode) {
    dot.el.style.transform = `translate3d(${dot.x.toFixed(2)}px,${dot.y.toFixed(2)}px,0) scale(${dot.sx.toFixed(3)},${dot.sy.toFixed(3)})`;
    return;
  }
  // A stroke is a bar whose LENGTH is the eased scale, so the same four elements
  // draw the check mark without the engine owning a second set of nodes.
  const width = Math.max(0, dot.sx * box.dotPx);
  const height = (18 / REFERENCE_DOT_PX) * box.dotPx * dot.sy;
  dot.el.style.width = `${width.toFixed(1)}px`;
  dot.el.style.height = `${height.toFixed(1)}px`;
  dot.el.style.margin = `${(-height / 2).toFixed(1)}px 0 0 ${(-width / 2).toFixed(1)}px`;
  dot.el.style.transform = `translate3d(${dot.x.toFixed(2)}px,${dot.y.toFixed(2)}px,0) rotate(${dot.rotation.toFixed(2)}deg)`;
}

/** The cursor's pull on the dots, as a fraction and a direction. */
type Lean = { strength: number; x: number; y: number };

function easeDots(
  dots: readonly Dot[],
  motion: CoreMotion,
  time: number,
  box: Box,
  lean: Lean,
  dt: number,
) {
  const rhythm = breath(time);
  const settle = dt * (motion === "alert" ? 9 : 5);
  const flow = current(time);
  for (let index = 0; index < dots.length; index += 1) {
    const dot = dots[index];
    const target = dotTarget(motion, index, time, rhythm);
    // Carried by the fluid: the current the liquid layer is drawn at, added to
    // every placement. `alert` and `fail` take a third of it — a state that is
    // deliberately going nowhere must not be seen to drift, but a mass in liquid
    // that is perfectly still reads as a mass in glue.
    const carried = motion === "alert" || motion === "fail" ? 0.34 : 1;
    let x = target.x + flow.x * 16 * carried;
    let y = target.y + flow.y * 16 * carried;
    if (lean.strength > 0.01) {
      // The nearest dots reach toward the cursor and the goo stretches between
      // them, which is the whole of the interaction: the Core notices, it does
      // not respond. It is aria-hidden and carries no click.
      const towardX = lean.x * 120 - x;
      const towardY = lean.y * 120 - y;
      const pull =
        lean.strength *
        0.4 *
        Math.max(0, 1 - Math.hypot(towardX, towardY) / 260);
      x += towardX * pull;
      y += towardY * pull;
    }
    dot.x = lerp(dot.x, x * box.scale * box.spread, settle);
    dot.y = lerp(dot.y, y * box.scale * box.spread, settle);
    dot.sx = lerp(dot.sx, target.sx, dt * 6);
    dot.sy = lerp(dot.sy, target.sy, dt * 6);
    // Rotation must not ease the long way round when a target flips sign.
    dot.rotation =
      Math.abs(target.rotation - dot.rotation) > 90
        ? target.rotation
        : lerp(dot.rotation, target.rotation, dt * 7);
  }
}

/**
 * The body of liquid inside the glass.
 *
 * It drifts on the current and turns slowly, and it is ONE element: two blurred
 * masses painted on it (see the stylesheet) are enough for a reader to see the
 * fluid move, and moving one element is one composited transform rather than a
 * second animation to keep in phase.
 */
function writeLiquid(liquid: HTMLElement | null, time: number, box: Box) {
  if (!liquid) {
    return;
  }
  const flow = current(time);
  // A fraction of the ball, so the fluid always fills it — a liquid that travels
  // far enough to show its own edge is a liquid sloshing in a half-empty ball,
  // which is a different (and much noisier) idea.
  const travel = box.width * 0.06;
  liquid.style.transform = `translate3d(${(flow.x * travel).toFixed(2)}px,${(flow.y * travel).toFixed(2)}px,0) rotate(${(time * 3.4).toFixed(2)}deg)`;
}

function writeShell(
  shell: HTMLElement,
  time: number,
  amp: number,
  lean: Lean,
  tilt: { x: number; y: number },
) {
  const rhythm = breath(time);
  const size =
    1 + lean.strength * 0.05 + (rhythm - 0.5) * 0.05 * (0.7 + amp * 0.5);
  shell.style.transform = `perspective(900px) rotateY(${(tilt.x * 10).toFixed(2)}deg) rotateX(${(-tilt.y * 10).toFixed(2)}deg) scale(${size.toFixed(4)})`;
  shell.style.opacity = (
    0.93 +
    lean.strength * 0.07 +
    (rhythm - 0.5) * 0.06
  ).toFixed(3);
}

/**
 * Drives one orb until it is torn down.
 *
 * Returns the handle the effect owns: `stop` releases every listener and pending
 * frame, `wake` asks for one if the loop is parked and something can see it.
 */
function runOrbLoop(
  shell: HTMLElement,
  dots: readonly Dot[],
  state: MarginceCoreState,
  reduced: boolean,
): Readonly<{
  stop: () => void;
  wake: () => void;
  lean: Lean;
  box: () => Box;
}> {
  const behaviour = BEHAVIOUR[state];
  // A non-zero seed: at t=0 every formation is at its own origin, where several
  // of them have all four dots stacked, and the first frame would be one blob.
  let time = 11.3;
  let last = performance.now();
  let frame = 0;
  let drawnOnce = false;
  let onScreen = true;
  let windowFocused = isWindowFocused();
  let box = measure(shell, dots[0]?.el);
  const liquid = shell.querySelector<HTMLElement>(".core-liquid");
  // The filter lives in the component's own subtree, so it is found rather than
  // passed: the engine drives the DOM the component rendered, and threading an
  // element reference through for one attribute would be a second contract
  // between them for no gain.
  const blur =
    shell.parentElement?.querySelector<SVGFEGaussianBlurElement>(
      ".core-goo-blur",
    );
  const applyBlur = () =>
    blur?.setAttribute("stdDeviation", fusionBlur(box.width).toFixed(2));
  applyBlur();
  const lean: Lean = { strength: 0, x: 0, y: 0 };
  const tilt = { x: 0, y: 0 };
  const lineMode = behaviour.motion === "resolve";
  setLineMode(shell, dots, lineMode);

  const seen = () => onScreen && !document.hidden && windowFocused;

  const draw = (now: number) => {
    const dt = Math.min(0.05, (now - last) / 1000);
    last = now;
    // Reduced motion holds the clock still, so the composed frame is the state's
    // own rest position rather than a blank orb.
    if (!reduced) {
      time += dt * (0.5 + behaviour.speed * 0.7);
    }
    tilt.x = lerp(tilt.x, lean.x * lean.strength, dt * 7);
    tilt.y = lerp(tilt.y, lean.y * lean.strength, dt * 7);
    // Under reduced motion the easing is instant: a settle animation IS motion,
    // however small, so the one frame drawn has to be the settled one.
    easeDots(dots, behaviour.motion, time, box, lean, reduced ? 1 : dt);
    writeLiquid(liquid, time, box);
    for (const dot of dots) {
      writeDot(dot, box, lineMode);
    }
    writeShell(shell, time, behaviour.amp, lean, tilt);
    drawnOnce = true;
  };

  const pump = (now: number) => {
    frame = 0;
    // The FIRST frame is owed even to an orb nobody is looking at: an unpainted
    // dot sits at the element's origin, so parking before one frame leaves four
    // dots stacked in the middle of the glass.
    if (drawnOnce && (!seen() || reduced)) {
      return;
    }
    draw(now);
    frame = requestAnimationFrame(pump);
  };

  const wake = () => {
    if (frame || !seen()) {
      return;
    }
    // The clock restarts on resume, so an orb parked for ten minutes continues
    // from where it stopped instead of jumping by the length of the pause.
    last = performance.now();
    frame = requestAnimationFrame(pump);
  };

  const remeasure = () => {
    box = measure(shell, dots[0]?.el);
    applyBlur();
    wake();
  };

  document.addEventListener("visibilitychange", wake);
  addEventListener("scroll", remeasure, { passive: true });
  const releaseFocus = subscribeToWindowFocus((focused) => {
    if (focused === windowFocused) {
      return;
    }
    windowFocused = focused;
    wake();
  });
  const onScreenObserver = observeOnScreen(shell, (visible) => {
    onScreen = visible;
    wake();
  });
  const sizeObserver = observeSize(shell, remeasure);
  frame = requestAnimationFrame(pump);

  return {
    wake,
    lean,
    box: () => box,
    stop: () => {
      cancelAnimationFrame(frame);
      document.removeEventListener("visibilitychange", wake);
      removeEventListener("scroll", remeasure);
      releaseFocus();
      onScreenObserver?.disconnect();
      sizeObserver?.disconnect();
    },
  };
}

/** Whether this host has a cursor that can hover at all. */
function hasFinePointer(): boolean {
  return globalThis.matchMedia?.("(pointer: fine)").matches === true;
}

/**
 * Proximity, not hover: the orb notices the cursor before it arrives, leans
 * toward it and brightens.
 *
 * Read from a pointer listener that is throttled to one frame and attached only
 * where a cursor exists — on a touch host there is no proximity to sense, and a
 * pointermove listener there is a cost with nothing to show for it.
 */
function trackPointer(
  loop: Readonly<{ wake: () => void; lean: Lean; box: () => Box }>,
): () => void {
  let queued = false;
  let latest: { x: number; y: number } | null = null;

  const apply = () => {
    queued = false;
    const point = latest;
    const box = loop.box();
    if (!point) {
      return;
    }
    const reach = Math.max(90, box.radius * 2);
    const dx = point.x - box.centreX;
    const dy = point.y - box.centreY;
    const distance = Math.hypot(dx, dy);
    if (distance > box.radius + reach) {
      loop.lean.strength = 0;
      loop.lean.x = 0;
      loop.lean.y = 0;
      return;
    }
    loop.lean.strength = Math.max(
      0,
      1 - Math.max(0, distance - box.radius) / reach,
    );
    loop.lean.x = dx / box.radius;
    loop.lean.y = dy / box.radius;
    loop.wake();
  };

  const onMove = (event: PointerEvent) => {
    latest = { x: event.clientX, y: event.clientY };
    if (queued) {
      return;
    }
    queued = true;
    requestAnimationFrame(apply);
  };
  const onLeave = () => {
    loop.lean.strength = 0;
    loop.lean.x = 0;
    loop.lean.y = 0;
    loop.wake();
  };

  addEventListener("pointermove", onMove, { passive: true });
  addEventListener("pointerleave", onLeave);
  return () => {
    removeEventListener("pointermove", onMove);
    removeEventListener("pointerleave", onLeave);
  };
}

/**
 * Mounts the engine on a shell element and its four dots.
 *
 * The window-focus signal is held for the orb's whole life rather than inside the
 * loop, because the stylesheet's half of the same stillness (the sheen, the
 * caustic, the feed) is driven by the `data-window-blurred` attribute that signal
 * maintains — and that half runs even where this loop is parked.
 */
export function useCoreEngine(
  shellRef: React.RefObject<HTMLElement | null>,
  state: MarginceCoreState,
) {
  const reduced = usePrefersReducedMotion();
  const dotsRef = useRef<Dot[]>([]);

  useEffect(retainWindowFocusSignal, []);

  useEffect(() => {
    const shell = shellRef.current;
    if (!shell) {
      return;
    }
    const elements = [...shell.querySelectorAll<HTMLElement>(".core-dot")];
    if (elements.length !== DOTS) {
      return;
    }
    dotsRef.current = elements.map((el) => ({
      el,
      x: 0,
      y: 0,
      sx: 1,
      sy: 1,
      rotation: 0,
    }));
    const loop = runOrbLoop(shell, dotsRef.current, state, reduced);
    const releasePointer =
      reduced || !hasFinePointer() ? null : trackPointer(loop);
    return () => {
      loop.stop();
      releasePointer?.();
    };
  }, [shellRef, state, reduced]);
}
