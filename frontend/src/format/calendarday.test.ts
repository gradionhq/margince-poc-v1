// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { describe, expect, it } from "vitest";
import { calendarDay, dueInstant, middayInstant } from "./calendarday";

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

describe("middayInstant", () => {
  it("lands at the zone's own noon of the picked day, either side of a DST switch", () => {
    // Berlin is +01:00 in January and +02:00 in July.
    expect(middayInstant("2026-01-15", "Europe/Berlin")).toBe(
      "2026-01-15T11:00:00.000Z",
    );
    expect(middayInstant("2026-07-05", "Europe/Berlin")).toBe(
      "2026-07-05T10:00:00.000Z",
    );
  });

  it("keeps the picked day in the record zone AND for writers far west of it", () => {
    // The scenario that broke writer-local noon: a writer in Honolulu (UTC-10)
    // backdating an entry a Berlin-rendered timeline then filed under the
    // NEXT day. Minted at Berlin noon, both zones read the picked day.
    const at = new Date(middayInstant("2026-07-10", "Europe/Berlin"));
    expect(calendarDay(at, "Europe/Berlin")).toBe("2026-07-10");
    expect(calendarDay(at, "Pacific/Honolulu")).toBe("2026-07-10");
  });

  it("holds for zones on half-hour offsets and across the date line", () => {
    for (const zone of [
      "Asia/Kolkata",
      "Pacific/Kiritimati",
      "Pacific/Honolulu",
    ]) {
      const at = new Date(middayInstant("2026-03-29", zone));
      expect(calendarDay(at, zone)).toBe("2026-03-29");
    }
  });
});
