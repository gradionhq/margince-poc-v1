// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { describe, expect, it } from "vitest";
import type { MarginceCoreState } from "./margince-core";
import { BEHAVIOUR, FEED_INGEST, still } from "./margince-core-motion";

/** The closed vocabulary, spelled out once so the count below cannot drift
 *  quietly if a state is renamed rather than added or removed. */
const STATES: readonly MarginceCoreState[] = [
  "idle",
  "ingest",
  "working",
  "warning",
  "error",
];

describe("the Core's motion table", () => {
  it("covers exactly the five states of the closed vocabulary, no more", () => {
    expect(Object.keys(BEHAVIOUR).sort()).toEqual([...STATES].sort());
  });

  it("gives every state a distinct motion signature", () => {
    // Colour may carry state only because motion carries it first: two states
    // differing only in hue are two states a colour-blind reader cannot tell
    // apart, and the tuple below is the whole of what the eye reads before it
    // reads a hue at all.
    const signatures = STATES.map((state) => {
      const { level, speed, pulse, ingest } = BEHAVIOUR[state];
      return JSON.stringify([level, speed, pulse, ingest]);
    });
    expect(new Set(signatures).size).toBe(signatures.length);
  });

  it("tints only the two states that stop", () => {
    const stops: readonly MarginceCoreState[] = ["warning", "error"];
    const live: readonly MarginceCoreState[] = ["idle", "ingest", "working"];
    for (const state of stops) {
      expect(BEHAVIOUR[state].tint, `${state} must tint`).toBeGreaterThan(0);
    }
    for (const state of live) {
      expect(BEHAVIOUR[state].tint, `${state} must not tint`).toBe(0);
    }
  });

  it("reserves full intake for ingest alone", () => {
    for (const [state, behaviour] of Object.entries(BEHAVIOUR)) {
      if (state === "ingest") {
        expect(behaviour.ingest).toBe(1);
      } else {
        expect(behaviour.ingest, state).toBeLessThan(1);
      }
    }
  });

  it("makes working the fastest state and idle the least energetic", () => {
    const speeds = STATES.map((state) => BEHAVIOUR[state].speed);
    expect(BEHAVIOUR.working.speed).toBe(Math.max(...speeds));

    const levels = STATES.map((state) => BEHAVIOUR[state].level);
    expect(BEHAVIOUR.idle.level).toBe(Math.min(...levels));
  });

  it("holds a still state at its live level, tint and tintCol, with everything that moves zeroed", () => {
    // A reader with reduced motion still has to be told WHICH of the five
    // states this is, so the still frame keeps the state's own colour and
    // energy and only drops the dials that are motion by definition.
    for (const state of STATES) {
      const live = BEHAVIOUR[state];
      const held = still(state);
      expect(held.level).toBe(live.level);
      expect(held.tint).toBe(live.tint);
      expect(held.tintCol).toEqual(live.tintCol);
      expect(held.speed).toBe(0);
      expect(held.pulse).toBe(0);
      expect(held.ingest).toBe(0);
    }
  });

  it("sets the fed intake floor strictly between rest and ingest's own", () => {
    expect(FEED_INGEST).toBeGreaterThan(0);
    expect(FEED_INGEST).toBeLessThan(BEHAVIOUR.ingest.ingest);
  });

  it("keeps every tint colour channel in the 0..1 range a shader uniform expects", () => {
    for (const [state, behaviour] of Object.entries(BEHAVIOUR)) {
      for (const channel of behaviour.tintCol) {
        expect(channel, state).toBeGreaterThanOrEqual(0);
        expect(channel, state).toBeLessThanOrEqual(1);
      }
    }
  });
});
