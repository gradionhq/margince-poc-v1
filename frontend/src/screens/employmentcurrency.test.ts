import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { components } from "../api/schema";
import {
  currentEmployer,
  formerEmployers,
  stillHeld,
  today,
} from "./employmentcurrency";

type Employment = components["schemas"]["Person360Employment"];

// The client twin of people.EmploymentIsCurrentSQL. It exists because the flag is
// written once and never rewritten, so a screen that trusted it would name a
// company the server's own contact count had already stopped counting — and it is
// tested directly because the boundary is one day wide and a rendering test would
// only ever exercise whichever side of it the fixture happened to sit on.
//
// The clock is PINNED. These assertions are about a date comparison, so a real
// clock would make them pass or fail depending on the hour the suite ran, which
// is the flake this module was written to prevent elsewhere.
const NOON = new Date("2026-08-19T12:00:00");

function employment(over: Partial<Employment>): Employment {
  return {
    relationship_id: "rel-1",
    organization_id: "o-1",
    is_current_primary: false,
    ended_at: null,
    ...over,
  };
}

beforeEach(() => {
  vi.useFakeTimers();
  vi.setSystemTime(NOON);
});

afterEach(() => {
  vi.useRealTimers();
});

describe("today", () => {
  it("is the LOCAL day, zero-padded, in ISO order", () => {
    // Order and padding both matter: the value is compared lexicographically
    // below and sent to the server as a `date`.
    expect(today()).toBe("2026-08-19");
  });

  it("pads a single-digit month and day", () => {
    vi.setSystemTime(new Date("2026-01-02T23:30:00"));
    expect(today()).toBe("2026-01-02");
  });
});

describe("stillHeld", () => {
  it("holds a job with no end date", () => {
    expect(stillHeld(employment({ ended_at: null }))).toBe(true);
  });

  it("holds a job whose last day is still ahead — a notice period", () => {
    expect(stillHeld(employment({ ended_at: "2026-11-17T00:00:00Z" }))).toBe(
      true,
    );
  });

  // The two days either side of the boundary. A comparison written the other way
  // round passes every other case in this file and gets exactly these wrong.
  it("does NOT hold a job whose last day is today", () => {
    expect(stillHeld(employment({ ended_at: "2026-08-19T00:00:00Z" }))).toBe(
      false,
    );
  });

  it("holds a job whose last day is tomorrow", () => {
    expect(stillHeld(employment({ ended_at: "2026-08-20T00:00:00Z" }))).toBe(
      true,
    );
  });

  it("does NOT hold a job whose last day has passed", () => {
    expect(stillHeld(employment({ ended_at: "2026-07-20T00:00:00Z" }))).toBe(
      false,
    );
  });
});

describe("currentEmployer", () => {
  const flaggedAndOngoing = employment({
    relationship_id: "rel-current",
    is_current_primary: true,
  });
  const flaggedButPassed = employment({
    relationship_id: "rel-stale",
    is_current_primary: true,
    ended_at: "2026-07-20T00:00:00Z",
  });
  const unflagged = employment({ relationship_id: "rel-side" });

  it("is the flagged employment that is still theirs", () => {
    expect(
      currentEmployer([unflagged, flaggedAndOngoing])?.relationship_id,
    ).toBe("rel-current");
  });

  // The whole reason this module exists: the flag alone is not enough.
  it("is undefined when the flagged employment's last day has passed", () => {
    expect(currentEmployer([flaggedButPassed, unflagged])).toBeUndefined();
  });

  it("is undefined when nothing is flagged", () => {
    expect(currentEmployer([unflagged])).toBeUndefined();
  });

  it("is undefined when there are no employments at all", () => {
    expect(currentEmployer(undefined)).toBeUndefined();
    expect(currentEmployer([])).toBeUndefined();
  });
});

describe("formerEmployers", () => {
  const current = employment({
    relationship_id: "rel-current",
    is_current_primary: true,
  });
  const past = employment({
    relationship_id: "rel-past",
    ended_at: "2026-01-31T00:00:00Z",
  });

  it("is everything the current employer is not, so the two partition the rows", () => {
    const rows = [current, past];
    const former = formerEmployers(rows);
    expect(former.map((e) => e.relationship_id)).toEqual(["rel-past"]);
    expect(former.length + 1).toBe(rows.length);
  });

  // A stale flag must not hide a row from BOTH lists — that is how somebody
  // disappears off their own record entirely.
  it("includes a flagged employment whose last day has passed", () => {
    const stale = employment({
      relationship_id: "rel-stale",
      is_current_primary: true,
      ended_at: "2026-07-20T00:00:00Z",
    });
    expect(formerEmployers([stale]).map((e) => e.relationship_id)).toEqual([
      "rel-stale",
    ]);
  });

  it("is empty when there are no employments", () => {
    expect(formerEmployers(undefined)).toEqual([]);
  });
});
