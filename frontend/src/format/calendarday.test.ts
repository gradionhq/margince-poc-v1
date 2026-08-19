// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { describe, expect, it } from "vitest";
import { calendarDay, dueInstant } from "./calendarday";

// The zone the machine running this suite happens to be in. Every assertion
// below is written against it rather than against a fixed offset, because the
// invariant is "the day the reader picked is the day the reader reads" — a suite
// that only holds in Europe/Berlin would pass on one laptop and prove nothing.
const readerZone = Intl.DateTimeFormat().resolvedOptions().timeZone;

describe("calendarDay", () => {
  it("names the day the instant falls on in the given zone, not in UTC", () => {
    // 02:00 UTC on the 5th is still the evening of the 4th in New York.
    const at = new Date("2026-07-05T02:00:00Z");
    expect(calendarDay(at, "UTC")).toBe("2026-07-05");
    expect(calendarDay(at, "America/New_York")).toBe("2026-07-04");
    expect(calendarDay(at, "Asia/Tokyo")).toBe("2026-07-05");
  });

  it("returns ISO-ordered days, so two of them compare as strings", () => {
    const earlier = calendarDay(new Date("2026-07-04T12:00:00Z"), "UTC");
    const later = calendarDay(new Date("2026-07-05T12:00:00Z"), "UTC");
    expect(earlier).toBe("2026-07-04");
    expect(earlier < later).toBe(true);
  });
});

describe("dueInstant", () => {
  it("files the picked day under that same day in the reader's zone", () => {
    for (const day of ["2026-01-15", "2026-07-05", "2026-12-31"]) {
      expect(calendarDay(new Date(dueInstant(day)), readerZone)).toBe(day);
    }
  });

  it("lands at the END of the picked day, so a task filed for today is not overdue by breakfast", () => {
    const at = new Date(dueInstant("2026-07-05"));
    expect(at.getHours()).toBe(23);
    expect(at.getMinutes()).toBe(59);
    expect(at.getSeconds()).toBe(59);
  });

  it("is a UTC instant on the wire whatever zone minted it", () => {
    expect(dueInstant("2026-07-05")).toMatch(
      /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$/,
    );
  });
});
