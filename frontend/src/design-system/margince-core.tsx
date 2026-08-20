// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { useEffect, useRef, useState } from "react";
import { useCoreEngine } from "./margince-core-engine";
import "./margince-core.css";

/**
 * The Margince Core — WDS-CORE-1..4 (ADR-0076).
 *
 * The product's one piece of AI identity, shown by the unauthenticated surface,
 * the session splash, onboarding and the workspace rail: a glass ball with four
 * ribbons turning inside it, all threaded through one shared focus, which is
 * why they cross at a single hot point. The ribbons are drawn on the GPU
 * (`margince-core-shader.ts`), moved by the engine beside it, and what each
 * state DOES is one table (`margince-core-motion.ts`).
 *
 * Four things about it are load-bearing rather than stylistic:
 *
 *  - **One implementation** (WDS-CORE-1). A caller passes `state` and never
 *    restyles. Sizing through the documented `--coreSize` / `--coreGlass`
 *    custom properties is configuration; anything past that is a caller
 *    restyling a shared primitive.
 *  - **The state list is closed** (WDS-CORE-2), and it is the AGENT'S WORK
 *    LIFECYCLE: idle → ingest → working, and back to idle when a run settles.
 *    `warning` covers every way the agent stops on something a human must
 *    look at — a contradiction, an unreachable source, a licence it lacks —
 *    and `error` is the one way a run breaks outright. Callers use the Core as
 *    a status channel, and a status channel with an open vocabulary is one
 *    nobody can test and no second caller can reuse. There is no `listening` or
 *    `speaking`: Margince's agent runs overnight over captured activity and
 *    stages proposals a human confirms — it never holds a conversation, and a
 *    state naming one would be the product claiming something it does not do.
 *  - **State is MOTION first**, colour second. One behaviour per state, so the
 *    three that share the palette's green — idle, ingest and working — are
 *    still three distinguishable things. Only the two that stop leave the
 *    green: amber for `warning`, red for `error`.
 *  - **It is `aria-hidden`** (WDS-CORE-4). Every state it shows is also stated
 *    in text by the surface around it, which is what makes it safe to be this
 *    decorative — and why it carries no click: the caller's own button owns
 *    that.
 *
 * WHAT A HOST WITHOUT WEBGL2 GETS. The static dress below: the same ball, the
 * same state colour, no motion. That is not a courtesy path, it is what jsdom
 * renders in every test, what a locked-down browser renders, and what a context
 * lost to a GPU reset falls back to. `data-core-state` is on the root either
 * way, so a surface reading the Core's state off the DOM cannot tell the
 * difference.
 */
export type MarginceCoreState =
  | "idle"
  | "ingest"
  | "working"
  | "warning"
  | "error";

/**
 * Which way round the ball is dressed.
 *
 * On a dark surface the Core is emissive: it adds its own light and the ribbons
 * are the object. On paper it goes opaque and dark, with the ribbons glowing
 * inside it, because an emissive ball on white is a smudge.
 *
 * `auto` follows the app theme, which is right for anything sitting on the page.
 * The workspace rail is the case that needs telling: it is dark green in BOTH
 * themes, so a Core there is always on a dark surface.
 */
export type CoreSurface = "auto" | "dark";

/** Reads the theme the shell has already resolved onto the root element. */
function useDarkPage(): boolean {
  const [dark, setDark] = useState(
    () => document.documentElement.dataset.theme === "dark",
  );
  useEffect(() => {
    const root = document.documentElement;
    // The attribute is the shell's own output (app/theme.ts), and the Core's
    // stylesheet keys its own dark values off the same one. Observing it rather
    // than subscribing to the store keeps the design system from importing the
    // app it dresses.
    const observer = new MutationObserver(() => {
      setDark(root.dataset.theme === "dark");
    });
    observer.observe(root, {
      attributes: true,
      attributeFilter: ["data-theme"],
    });
    return () => observer.disconnect();
  }, []);
  return dark;
}

export function MarginceCoreScene({
  state = "idle",
  progress,
  size = "hero",
  feed = false,
  surface = "auto",
  className = "",
}: Readonly<{
  state?: MarginceCoreState;
  /** 0..1. Draws the ring; omit it and no ring renders (WDS-CORE-2). */
  progress?: number;
  size?: "hero" | "md";
  /**
   * Context arriving at the Core, which the shader draws as the intake
   * whirlpool. OFF by default, because a caller that has not said material is
   * arriving is a caller with nothing arriving: an orb that runs intake while it
   * waits for somebody to sign in is claiming work no installation is doing.
   */
  feed?: boolean;
  /** What the Core is sitting on. See `CoreSurface`. */
  surface?: CoreSurface;
  className?: string;
}>) {
  const canvas = useRef<HTMLCanvasElement>(null);
  const darkPage = useDarkPage();
  const paper = surface === "auto" && !darkPage;
  const live = useCoreEngine(canvas, state, {
    paper,
    feed,
    // The hero is the Core as the subject of a page rather than as a light in the
    // chrome, and it wears the same state with more life in it.
    hero: size === "hero",
  });

  const classes = [
    "core",
    size === "md" ? "core-md" : "",
    live ? "core-live" : "core-still",
    className,
  ]
    .filter(Boolean)
    .join(" ");

  return (
    <div className={classes} data-core-state={state} aria-hidden="true">
      <span className="core-glow" />
      <div className="core-shell">
        <canvas className="core-canvas" ref={canvas} />
        {/* The static dress, under the canvas and covered by it while the
            engine is live: shell, rim and glass, in the state's own colour. It
            is always in the markup rather than swapped in on failure, because a
            context can be lost between two frames and a hole where the Core was
            is worse than a still one. */}
        <div className="core-rim" />
        <div className="core-glass" />
      </div>
      {progress === undefined ? null : (
        <svg className="core-progress" viewBox="0 0 100 100" aria-hidden="true">
          <circle cx="50" cy="50" r="48.5" pathLength="100" />
          <circle
            className="core-progress-value"
            cx="50"
            cy="50"
            r="48.5"
            pathLength="100"
            strokeDasharray={`${Math.max(0, Math.min(1, progress)) * 100} 100`}
          />
        </svg>
      )}
    </div>
  );
}
