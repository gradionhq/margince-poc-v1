// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { useCallback, useEffect, useRef, useState } from "react";
import type { MarginceCoreState } from "../design-system/margince-core";

/**
 * A scripted run of the agent surface, for looking at it.
 *
 * REVIEW SCAFFOLDING, and it is gated with the state switcher it sits beside
 * (app/ui-preview.ts). Every line it says is invented: no installation is doing
 * this work, and the surface's whole value the rest of the time is that it only
 * ever reports what it read. So this exists to answer one question a still
 * screenshot cannot, which is what the thing LOOKS LIKE while it works: the orb
 * going from rest into intake, the line naming one item after another, the state
 * settling back down.
 *
 * The states a reviewer can hold one at a time are static by design. This is the
 * other half: the choreography between them, which is the part nobody can judge
 * from a chip.
 */

/** One beat of the run: what the orb is doing, and what the line says. */
type Beat = Readonly<{
  state: MarginceCoreState;
  said: string;
  /** How long this beat holds, in milliseconds. */
  ms: number;
}>;

/**
 * The run itself, in the vocabulary the real line uses.
 *
 * Paced deliberately unevenly. A run where every beat lasts the same time reads
 * as a slideshow; real work arrives in bursts, and the bursts are what make the
 * surface feel like it is keeping up rather than performing.
 */
const RUN: readonly Beat[] = [
  { state: "ingest", said: "Reading captured mail", ms: 1100 },
  { state: "ingest", said: "Ingesting zenloop", ms: 620 },
  { state: "ingest", said: "Ingesting KoreaPartner Co., Ltd.", ms: 480 },
  { state: "ingest", said: "Ingesting DACHPartner GmbH", ms: 440 },
  { state: "ingest", said: "Reading the zenloop website", ms: 900 },
  { state: "working", said: "Scoring how well zenloop fits", ms: 1200 },
  { state: "working", said: "Drafting a follow-up to Lisa Rentrop", ms: 1300 },
  { state: "working", said: "Logging an activity on zenloop", ms: 700 },
  { state: "warning", said: "2 duplicate pairs to decide", ms: 1600 },
  { state: "idle", said: "Nothing needs you", ms: 1400 },
];

export type DemoRun = Readonly<{
  /** The state to draw, or null when no run is playing. */
  state: MarginceCoreState | null;
  /** The line to draw, or null when no run is playing. */
  said: string | null;
  playing: boolean;
  toggle: () => void;
}>;

/**
 * Plays the run, one beat at a time, and stops cleanly.
 *
 * One timer, re-armed per beat rather than a list of timers set up front: a run
 * that is stopped half way has to leave nothing behind, and a schedule laid out
 * in advance keeps firing into a component that has moved on.
 */
export function useDemoRun(): DemoRun {
  const [at, setAt] = useState<number | null>(null);
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);

  useEffect(() => {
    if (at === null) {
      return;
    }
    const beat = RUN[at];
    if (beat === undefined) {
      setAt(null);
      return;
    }
    timer.current = setTimeout(() => setAt(at + 1), beat.ms);
    return () => {
      if (timer.current !== null) {
        clearTimeout(timer.current);
        timer.current = null;
      }
    };
  }, [at]);

  const toggle = useCallback(() => {
    setAt((current) => (current === null ? 0 : null));
  }, []);

  const beat = at === null ? undefined : RUN[at];
  return {
    state: beat?.state ?? null,
    said: beat?.said ?? null,
    playing: at !== null,
    toggle,
  };
}
