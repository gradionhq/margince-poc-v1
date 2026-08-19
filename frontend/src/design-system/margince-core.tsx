// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { useId, useRef } from "react";
import { useCoreEngine } from "./margince-core-engine";
import { CoreFeed } from "./margince-core-feed";
import "./margince-core.css";

/**
 * The Margince Core — WDS-CORE-1..4 (ADR-0076).
 *
 * The product's one piece of AI identity, shown by the unauthenticated surface,
 * the session splash, onboarding and the in-app workbench: a glass ball full of
 * liquid, with four dots suspended in it that fuse and split like one material.
 * The fluid and the dots ride the same slow current (`margince-core-motion.ts`),
 * so what moves is the body of liquid and the dots go with it. Four things about it are load-bearing rather than stylistic:
 *
 *  - **One implementation** (WDS-CORE-1). A caller passes `state` and never
 *    restyles. Sizing through the documented `--coreSize` / `--coreGlass` custom
 *    properties is configuration; anything past that is a caller restyling a
 *    shared primitive.
 *  - **The state list is closed** (WDS-CORE-2), and it is the AGENT'S WORK
 *    LIFECYCLE: dormant → ingesting → reasoning → drafting → applied, plus the
 *    three ways it stops (flagged, disconnected, error). Callers use the
 *    Core as a status channel, and a status channel with an open vocabulary is one
 *    nobody can test and no second caller can reuse. There is no `listening` or
 *    `speaking`: Margince's agent runs overnight over captured activity and
 *    stages proposals a human confirms — it never holds a conversation, and a
 *    state naming one would be the product claiming something it does not do.
 *  - **State is MOTION first** (`margince-core-motion.ts`), colour second
 *    (`margince-core.css`). One movement archetype per state, so the five that
 *    share the palette's green — the whole run from rest to applied — are still
 *    five distinguishable things. Only the three that stop leave the green:
 *    amber for `flagged`, grey for `disconnected`, red for `error`.
 *  - **It is `aria-hidden`** (WDS-CORE-4). Every state it shows is also stated in
 *    text by the surface around it, which is what makes it safe to be this
 *    decorative — and why it carries no click: the dock's own button owns that.
 */
export type MarginceCoreState =
  | "dormant"
  | "ingesting"
  | "reasoning"
  | "drafting"
  | "applied"
  | "flagged"
  | "disconnected"
  | "error";

export function MarginceCoreScene({
  state = "dormant",
  progress,
  size = "hero",
  feed = true,
  className = "",
}: Readonly<{
  state?: MarginceCoreState;
  /** 0..1. Draws the ring; omit it and no ring renders (WDS-CORE-2). */
  progress?: number;
  size?: "hero" | "md";
  /** Context arriving at the Core. Off where nothing is arriving. */
  feed?: boolean;
  className?: string;
}>) {
  const shell = useRef<HTMLDivElement>(null);
  // One filter per instance, because an SVG filter is addressed by document id
  // and two Cores on a page would otherwise share — and share the blur radius,
  // which is the one thing that has to differ between a 230px hero and the 34px
  // orb in the shell's rail.
  const goo = useId();
  const pool = useId();
  useCoreEngine(shell, state);

  const classes = ["core", size === "md" ? "core-md" : "", className]
    .filter(Boolean)
    .join(" ");

  return (
    <div className={classes} data-core-state={state} aria-hidden="true">
      <span className="core-glow" />
      <div className="core-shell" ref={shell}>
        {/* The well holds what is INSIDE the glass and clips it; the glass sits
            above and is what light happens on. Two elements because a ball whose
            interior is not clipped is a disc with dots wandering off it. */}
        <div className="core-well">
          {/* The fluid the ball is FULL of: three masses travelling their own
              slow paths, fused by their own filter so they merge and part like
              liquid, and carried by the same current as the dots — which is what
              makes the dots read as suspended IN it rather than drawn over it.
              Its own filter, not the dots': fused together, the dots would stop
              being things in a fluid and become lumps of it. */}
          <div className="core-liquid" style={{ filter: `url(#${pool})` }}>
            <i className="core-blob" />
            <i className="core-blob" />
            <i className="core-blob" />
          </div>
          {/* Written out rather than mapped, and the count is not a variable: a
              dot's POSITION in this list is its identity in the motion table
              (which one leaves the mass, which stroke of the check mark it
              draws), so these four are four distinct things that happen to look
              alike. The engine refuses to run against a
              markup that does not carry exactly `DOTS` of them, which is what
              keeps this list and the motion table agreeing. */}
          <div className="core-goo" style={{ filter: `url(#${goo})` }}>
            <i className="core-dot" />
            <i className="core-dot" />
            <i className="core-dot" />
            <i className="core-dot" />
          </div>
          <div className="core-rim" />
        </div>
        <div className="core-sheen" />
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
      {feed ? <CoreFeed /> : null}
      {/*
        The fusion filter: a tight blur, then an alpha ramp steep enough to snap
        the result back to hard edges. That pairing is what makes two dots MERGE
        instead of both going soft — a blur alone is slime, and the ramp alone
        cannot join anything. `stdDeviation` is set by the engine from the orb's
        measured width, so one filter serves every size instead of a bucket per
        breakpoint.
      */}
      <svg className="core-defs" width="0" height="0" aria-hidden="true">
        <filter id={goo}>
          <feGaussianBlur
            className="core-goo-blur"
            in="SourceGraphic"
            stdDeviation="6"
            result="fused"
          />
          <feColorMatrix
            in="fused"
            mode="matrix"
            values="1 0 0 0 0  0 1 0 0 0  0 0 1 0 0  0 0 0 32 -14"
            result="mass"
          />
          {/*
            The dots painted back over the mass they formed. Without this every
            shape wears a dark halo: the blur mixes each dot's colour with the
            transparent black around it, the ramp pulls that mixture back to
            opaque, and what returns is a dimmer ring — grime on the glass rather
            than fusion. Compositing the original graphic `atop` the fused shape
            keeps the dots their own colour and leaves the blur doing the one thing
            it is here for, which is the JOINS between them.
          */}
          <feComposite in="SourceGraphic" in2="mass" operator="atop" />
        </filter>
        {/*
          The liquid's own fusion, and it is a BLUR ALONE — no alpha ramp. The ramp
          is what gives the dots an edge, and an edge is exactly what a full vessel
          must not show: it would be the surface of a liquid with air above it. It
          also darkens whatever it snaps back to opaque, which on masses this large
          read as a stain. Blurred, the three masses mix where they overlap and
          reach the glass without ever drawing a boundary.
        */}
        <filter id={pool}>
          <feGaussianBlur
            className="core-pool-blur"
            in="SourceGraphic"
            stdDeviation="16"
          />
        </filter>
      </svg>
    </div>
  );
}
