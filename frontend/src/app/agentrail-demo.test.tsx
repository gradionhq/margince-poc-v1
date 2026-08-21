// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

/** @vitest-environment jsdom */

import { act, cleanup, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { Button } from "../design-system/atoms";
import { useDemoRun } from "./agentrail-demo";

// The scripted run is review scaffolding, and it is still code that runs in a
// build somebody looks at. What these hold is the part a reviewer would be
// misled by if it broke: that it PLAYS (the states change on their own rather
// than sitting on the first beat), that it ENDS, and that stopping it leaves
// the surface reporting what it actually read rather than the last invented
// line.

function Run() {
  const demo = useDemoRun();
  return (
    <div>
      <Button onClick={demo.toggle}>toggle</Button>
      <span data-testid="state">{demo.state ?? "-"}</span>
      <span data-testid="said">{demo.said ?? "-"}</span>
      <span data-testid="playing">{String(demo.playing)}</span>
    </div>
  );
}

const shown = (id: string) => screen.getByTestId(id).textContent ?? "";

/**
 * Clicks the control the way the panel's own button does.
 *
 * A bare dispatch inside `act`, not `userEvent`: user-event awaits its own
 * `setTimeout` between the events of one click, and the run under test re-arms
 * every beat from an effect. Handing user-event this suite's fake clock makes
 * the two advance each other and the click never resolves. Nothing here turns
 * on pointer or focus order — the subject is the beat chain — so the deciding
 * property is that a click lands at a moment this test chose.
 */
function toggle() {
  act(() => {
    screen.getByRole("button", { name: "toggle" }).click();
  });
}

/**
 * Runs the clock forward, in slices.
 *
 * A beat arms the next one from an EFFECT, which React only runs once the
 * commit for that beat has flushed. One long jump therefore fires a single
 * timer and stops; the slices give each beat its commit before the next tick of
 * the clock reaches it.
 */
function passes(ms: number) {
  const SLICE = 50;
  for (let spent = 0; spent < ms; spent += SLICE) {
    act(() => {
      vi.advanceTimersByTime(SLICE);
    });
  }
}

beforeEach(() => {
  vi.useFakeTimers();
});

afterEach(() => {
  cleanup();
  vi.useRealTimers();
});

describe("useDemoRun", () => {
  it("reports nothing at all until a reviewer asks for a run", () => {
    render(<Run />);
    expect(shown("playing")).toBe("false");
    expect(shown("state")).toBe("-");
    expect(shown("said")).toBe("-");
  });

  it("opens the run on intake, which is where the agent's own work starts", () => {
    render(<Run />);
    toggle();
    expect(shown("playing")).toBe("true");
    expect(shown("state")).toBe("ingest");
    expect(shown("said")).toBe("Reading captured mail");
  });

  it("moves through the beats on its own, without another click", () => {
    // The whole point of the control: a state a reviewer can only hold still is
    // a state nobody can judge the motion of.
    render(<Run />);
    toggle();
    const first = shown("said");
    passes(1200);
    expect(shown("said")).not.toBe(first);
  });

  it("reaches the working beats, so the run is not only intake", () => {
    render(<Run />);
    toggle();
    passes(4000);
    expect(shown("state")).toBe("working");
  });

  it("ends on its own and hands the surface back", () => {
    // It must END. A run that left the section holding an invented state would
    // make every later reading of that section a lie nobody could see.
    render(<Run />);
    toggle();
    passes(60_000);
    expect(shown("playing")).toBe("false");
    expect(shown("state")).toBe("-");
    expect(shown("said")).toBe("-");
  });

  it("stops the moment it is asked to, mid-run", () => {
    render(<Run />);
    toggle();
    passes(1500);
    expect(shown("playing")).toBe("true");
    toggle();
    expect(shown("playing")).toBe("false");
    expect(shown("state")).toBe("-");
  });

  it("starts again from the top after it has been stopped", () => {
    render(<Run />);
    toggle();
    passes(4000);
    toggle();
    toggle();
    expect(shown("said")).toBe("Reading captured mail");
  });

  it("leaves no timer behind when it unmounts mid-run", () => {
    // A beat that fires into an unmounted component is a React warning at best
    // and a leak at worst, and this one re-arms itself on every beat.
    const { unmount } = render(<Run />);
    toggle();
    unmount();
    expect(vi.getTimerCount()).toBe(0);
  });
});
