// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { useEffect, useRef, useState } from "react";
import type { MarginceCoreState } from "./margince-core";
import { type CoreFrame, createCoreRenderer } from "./margince-core-gl";
import {
  asHero,
  type CoreBehaviour,
  EASE,
  FEED_INGEST,
  FRAME_MS,
  MAX_STEP,
  PHASE_SEED,
  rowFor,
  still,
} from "./margince-core-motion";
import { usePrefersReducedMotion } from "./motion";
import {
  isWindowFocused,
  retainWindowFocusSignal,
  subscribeToWindowFocus,
} from "./window-focus";

/**
 * The Core's engine: it decides WHEN the sphere moves and how it gets from one
 * state to the next. What it looks like is the shader's business
 * (`margince-core-shader.ts`), and the numbers each state is made of are the
 * table's (`margince-core-motion.ts`).
 *
 * Three rules hold this file together.
 *
 * **Nothing snaps.** Every dial eases toward the current state's target, so a
 * state change reads as the material changing rather than a switch being thrown.
 * The phase is INTEGRATED rather than derived from elapsed time, which is what
 * lets `speed` change without the motion teleporting.
 *
 * **The loop parks whenever nobody is watching, and only ever resumes on an
 * EVENT.** A window without focus, a state that has finished easing and is not
 * advancing, a document hidden behind another tab: in each case the frames are
 * work with nothing to show for it. The condition a loop parks on must have an
 * announced end, or the sphere freezes for good.
 *
 * **It survives the machine.** No WebGL2, a refused compile, a context lost to
 * a GPU reset: the hook reports that it is not live and the component wears its
 * static dress instead.
 */

/** The eased state of the object, as opposed to where it is heading. */
type Dials = {
  level: number;
  speed: number;
  pulse: number;
  ingest: number;
  tint: number;
  tintCol: [number, number, number];
};

/** Close enough that another frame would not change a pixel. */
const SETTLED = 1e-4;

function dialsFrom(behaviour: CoreBehaviour): Dials {
  return {
    level: behaviour.level,
    speed: behaviour.speed,
    pulse: behaviour.pulse,
    ingest: behaviour.ingest,
    tint: behaviour.tint,
    tintCol: [...behaviour.tintCol],
  };
}

/** Moves one dial a fraction of the way to its target, and says if it moved. */
function ease(from: number, to: number, rate: number): number {
  return from + (to - from) * rate;
}

function step(dials: Dials, target: CoreBehaviour): boolean {
  dials.level = ease(dials.level, target.level, EASE.level);
  dials.speed = ease(dials.speed, target.speed, EASE.speed);
  dials.pulse = ease(dials.pulse, target.pulse, EASE.pulse);
  dials.ingest = ease(dials.ingest, target.ingest, EASE.ingest);
  dials.tint = ease(dials.tint, target.tint, EASE.tint);
  for (let i = 0; i < 3; i++) {
    dials.tintCol[i] = ease(dials.tintCol[i], target.tintCol[i], EASE.tintCol);
  }
  return (
    Math.abs(dials.level - target.level) > SETTLED ||
    Math.abs(dials.speed - target.speed) > SETTLED ||
    Math.abs(dials.pulse - target.pulse) > SETTLED ||
    Math.abs(dials.ingest - target.ingest) > SETTLED ||
    Math.abs(dials.tint - target.tint) > SETTLED ||
    dials.tintCol.some(
      (channel, i) => Math.abs(channel - target.tintCol[i]) > SETTLED,
    )
  );
}

/** What the hook's callers can change under a running loop. */
type Wanted = {
  behaviour: CoreBehaviour;
  paper: number;
};

type Loop = Readonly<{
  wake: () => void;
  /** Re-sizes the drawing buffer and draws once, whether parked or not. */
  remeasure: () => void;
  stop: () => void;
}>;

function runCoreLoop(
  canvas: HTMLCanvasElement,
  wanted: { current: Wanted },
): Loop | null {
  const renderer = createCoreRenderer(canvas);
  if (!renderer) {
    return null;
  }
  const dials = dialsFrom(wanted.current.behaviour);
  /* The light in the shader comes from a fixed direction. It used to follow the
     cursor, and what that produced was an object that reacted to a pointer
     crossing the page on its way somewhere else, which reads as twitchy rather
     than alive: the orb has its own motion and does not need a second source. */
  const mouse: readonly [number, number] = [0, 0];
  let phase = PHASE_SEED;
  let paper = wanted.current.paper;
  let handle = 0;
  let last = 0;
  let drawnAt = 0;
  let stopped = false;

  const measure = () => {
    // The Core is a ball, so one side serves: whichever the box gives, the
    // square drawing buffer is sized from it.
    const box = canvas.getBoundingClientRect();
    renderer.resize(Math.max(box.width, box.height), devicePixelRatio || 1);
  };

  /** Advances every dial one frame and reports whether anything is still
   *  changing. False is the signal to park: the object has arrived and is not
   *  advancing under its own speed. */
  const advance = (now: number): boolean => {
    const target = wanted.current.behaviour;
    const moving = step(dials, target);
    paper = ease(paper, wanted.current.paper, EASE.tint);
    const dt = last === 0 ? 0 : Math.min((now - last) / 1000, MAX_STEP);
    last = now;
    phase += dt * dials.speed;
    return (
      moving ||
      dials.speed > SETTLED ||
      Math.abs(paper - wanted.current.paper) > SETTLED
    );
  };

  const shown = (): CoreFrame => ({
    level: dials.level,
    phase,
    pulse: dials.pulse,
    ingest: dials.ingest,
    tint: dials.tint,
    tintCol: dials.tintCol,
    mouse,
    paper,
  });

  const tick = (now: number) => {
    // The Core never truly rests: its quietest state still drifts, so this loop
    // runs for as long as the app is open, on every screen. At the display's own
    // rate that is a fragment-heavy shader redrawn sixty times a second forever,
    // which costs a laptop real battery and costs a software renderer (CI, a
    // machine with no GPU) enough to slow the page around it. The motion here is
    // a slow drift, and a slow drift does not need every frame: the loop keeps
    // asking the browser for one, and DRAWS on the ones far enough apart.
    //
    // The budget covers the DRAW alone. `advance` eases the dials one step per
    // call, so skipping the call would stretch every transition by however much
    // the display outruns the budget: the orb would take twice as long to reach
    // a state on a 60 Hz screen as the motion is written for, which is a
    // different animation rather than a cheaper one.
    handle = requestAnimationFrame(tick);
    const drifting = advance(now);
    if (now - drawnAt >= FRAME_MS || !drifting) {
      drawnAt = now;
      renderer.draw(shown());
    }
    // Parked only after the frame that decides it, so a park is a park: the
    // next callback is never already in flight when the loop stops wanting one.
    if (!drifting || stopped) {
      cancelAnimationFrame(handle);
      handle = 0;
    }
  };

  const wake = () => {
    if (stopped || handle !== 0 || !isWindowFocused()) {
      return;
    }
    measure();
    last = 0;
    drawnAt = 0;
    handle = requestAnimationFrame(tick);
  };

  /**
   * A size change has to land whether the loop is running or not.
   *
   * `wake` cannot carry this: it returns early when a frame is already
   * scheduled, which is exactly the case while the orb is animating, so a
   * running Core would keep the buffer it was born with and the browser would
   * scale it up. That is not a subtle artefact, it is a visibly soft ball.
   */
  const remeasure = () => {
    if (stopped) {
      return;
    }
    measure();
    renderer.draw(shown());
  };

  const stop = () => {
    stopped = true;
    if (handle !== 0) {
      cancelAnimationFrame(handle);
      handle = 0;
    }
    renderer.dispose();
  };

  measure();
  // One frame straight away, so a Core that mounts parked (an unfocused window,
  // reduced motion) is drawn rather than showing an empty canvas.
  renderer.draw(shown());
  wake();
  return { wake, remeasure, stop };
}

/**
 * The target row for a Core, given everything outside the state table that has
 * a say in it.
 *
 * `feed` is the caller telling us context is ARRIVING, which is a fact about the
 * surface rather than about the agent's state: a dock that is being fed while
 * the agent rests is a real combination. It raises the intake floor and leaves
 * everything else the state's own.
 */
function wants(
  state: MarginceCoreState,
  reduced: boolean,
  feed: boolean,
  hero: boolean,
): CoreBehaviour {
  const row = reduced ? still(state) : rowFor(state);
  const base = hero && !reduced ? asHero(row) : row;
  if (!feed || reduced) {
    return base;
  }
  return { ...base, ingest: Math.max(base.ingest, FEED_INGEST) };
}

/**
 * Mounts the engine on a canvas and reports whether it is live.
 *
 * False means the host cannot run it, and the component owes the reader its
 * static dress. It is false on the first render by definition: a context cannot
 * be made before the canvas is in the document.
 *
 * The window-focus signal is held for the orb's whole life rather than inside
 * the loop, because the stylesheet's half of the same stillness is driven by the
 * `data-window-blurred` attribute that signal maintains, and that half runs even
 * where this loop is parked.
 */
export function useCoreEngine(
  canvasRef: React.RefObject<HTMLCanvasElement | null>,
  state: MarginceCoreState,
  options: Readonly<{ paper: boolean; feed: boolean; hero: boolean }>,
): boolean {
  const reduced = usePrefersReducedMotion();
  const [live, setLive] = useState(false);
  const { paper, feed, hero } = options;
  const wanted = useRef<Wanted>({
    behaviour: wants(state, reduced, feed, hero),
    paper: paper ? 1 : 0,
  });

  useEffect(retainWindowFocusSignal, []);

  // Targets are written through a ref, so a state change bends a running loop
  // instead of tearing the GL context down and building another one. The
  // dependencies are the primitives the target is DERIVED from, not the derived
  // object: a fresh table row every render would rerun this every render.
  const loopRef = useRef<Loop | null>(null);
  useEffect(() => {
    wanted.current.behaviour = wants(state, reduced, feed, hero);
    wanted.current.paper = paper ? 1 : 0;
    loopRef.current?.wake();
  }, [state, reduced, feed, paper, hero]);

  useEffect(() => {
    const canvas = canvasRef.current;
    if (!canvas) {
      return;
    }
    const loop = runCoreLoop(canvas, wanted);
    loopRef.current = loop;
    setLive(loop !== null);
    if (!loop) {
      return () => {
        loopRef.current = null;
      };
    }
    const observer = new ResizeObserver(() => {
      loop.remeasure();
      loop.wake();
    });
    observer.observe(canvas);
    const releaseFocus = subscribeToWindowFocus((focused) => {
      if (focused) {
        loop.wake();
      }
    });
    // A context lost to a GPU reset is not recoverable in place: the program and
    // the buffers are gone with it. Reporting it not-live puts the static dress
    // back, which is the honest picture of what the machine can draw.
    const onLost = (event: Event) => {
      event.preventDefault();
      // Stop as well as report. Reporting alone leaves `tick` re-arming itself
      // for every state whose eased speed is still above rest, drawing each
      // frame into a context that no longer exists; and nothing unmounts the
      // canvas on a loss, so only unmount would ever have ended it.
      loop.stop();
      setLive(false);
    };
    canvas.addEventListener("webglcontextlost", onLost);
    return () => {
      canvas.removeEventListener("webglcontextlost", onLost);
      releaseFocus();
      observer.disconnect();
      loop.stop();
      loopRef.current = null;
    };
    // Only the canvas: the loop reads everything else through the ref above, so
    // a state change bends the running loop instead of rebuilding the GL context
    // under it.
  }, [canvasRef]);

  return live;
}
