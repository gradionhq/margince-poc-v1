// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Calendar days, as the reader keeps them. Two halves of ONE contract: the day
// a reader picks in a form, and the day a screen files an instant under. They
// agree only while both are read in the same zone, which is why they are spelled
// here together instead of once per screen — the tasks list grouped by UTC
// calendar day while the form minted its instant from local wall time, and every
// reader west of UTC watched a task they had just filed for today appear under
// Upcoming.
//
// Both are pure and take their zone (or the reader's own wall clock) as input,
// so nothing here has to be tested against the machine's own zone.

// The calendar day an instant falls on, in a named IANA zone, as `yyyy-mm-dd` so
// two of them compare as strings without a second parse.
//
// Assembled from the parts rather than from a locale whose short date happens to
// come out ISO-ordered. What the parts guarantee is the ORDER and the padding,
// which is the whole reason this string is comparable — a locale's date format is
// data, it varies with the runtime's ICU, and a bucketing rule that reads
// "yesterday" because a format changed under it would be invisible.
// One formatter per zone: constructing an Intl.DateTimeFormat is the expensive
// part, and a task list asks this question once per row.
const dayFormatters = new Map<string, Intl.DateTimeFormat>();

function dayFormatter(zone: string): Intl.DateTimeFormat {
  const cached = dayFormatters.get(zone);
  if (cached) {
    return cached;
  }
  const made = new Intl.DateTimeFormat("en-CA", {
    timeZone: zone,
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
  });
  dayFormatters.set(zone, made);
  return made;
}

export function calendarDay(at: Date, zone: string): string {
  const parts = new Map(
    dayFormatter(zone)
      .formatToParts(at)
      .map((part) => [part.type, part.value]),
  );
  const [year, month, day] = [
    parts.get("year"),
    parts.get("month"),
    parts.get("day"),
  ];
  if (!year || !month || !day) {
    // Unreachable for the options above, and worth refusing rather than
    // returning "undefined-undefined-undefined", which would compare equal to
    // itself and quietly bucket every task into one day.
    throw new Error(`no calendar day for zone ${zone}`);
  }
  return `${year}-${month}-${day}`;
}

// The wire instant for a due date the reader picked as a calendar day — the
// `yyyy-mm-dd` a date input yields, which every caller has already checked is
// non-empty.
//
// A task stays due until that day ends WHERE THE READER IS, so the instant is
// the local end of day. Midnight would file a task picked for today as overdue
// at breakfast, and `new Date(day)` on the bare date reads it as UTC midnight,
// which does the same thing a whole zone offset earlier — east of UTC that is
// overdue on waking, west of UTC it is the previous calendar day.
export function dueInstant(day: string): string {
  return new Date(`${day}T23:59:59`).toISOString();
}

// The wall-clock value a `datetime-local` input shows for an instant, in the
// reader's OWN zone, as the `yyyy-mm-ddThh:mm` that control accepts.
//
// It is the exact inverse of the composer's `scheduleFields`, which reads such a
// value back by handing it to `new Date(...)` and so resolves it against the
// browser's zone. The pair has to agree: a reschedule form seeded from a
// differently-zoned reading would open on a moment an hour off the one it is
// about to resubmit, and the reader would move a message they never moved.
//
// Assembled from the local getters rather than by slicing an ISO string.
// `toISOString()` is UTC, so west of UTC that would seed the picker with
// yesterday's date and an evening send would read as due the day before.
export function localDateTimeValue(utcIso: string): string {
  const at = new Date(utcIso);
  const pad = (value: number) => String(value).padStart(2, "0");
  const day = `${at.getFullYear()}-${pad(at.getMonth() + 1)}-${pad(at.getDate())}`;
  return `${day}T${pad(at.getHours())}:${pad(at.getMinutes())}`;
}

// The instant that is NOON of a picked calendar day in a named zone — for a
// backdated entry whose screens render dates in that zone (the record pages'
// RECORD_ZONE): filing it at the zone's own midday is what keeps it on the
// picked day both there and in the wall clock of any writer within twelve
// hours of that zone.
//
// Found without a timezone table: read what wall time the zone shows at UTC
// noon of that day, then shift the instant by the difference from the noon
// that was wanted. One correction is exact because a zone's offset does not
// change within the couple of hours the shift moves through — DST switches
// happen at night, and the target is midday.
export function middayInstant(day: string, zone: string): string {
  const utcNoon = new Date(`${day}T12:00:00Z`);
  const parts = new Map(
    new Intl.DateTimeFormat("en-CA", {
      timeZone: zone,
      year: "numeric",
      month: "2-digit",
      day: "2-digit",
      hour: "2-digit",
      minute: "2-digit",
      // h23, not `hour12: false`: some ICU builds resolve the latter to h24,
      // which prints midnight as "24" and breaks the ISO string below.
      hourCycle: "h23",
    })
      .formatToParts(utcNoon)
      .map((part) => [part.type, part.value]),
  );
  const shown = new Date(
    `${parts.get("year")}-${parts.get("month")}-${parts.get("day")}T${parts.get("hour")}:${parts.get("minute")}:00Z`,
  );
  const wanted = new Date(`${day}T12:00:00Z`);
  return new Date(
    utcNoon.getTime() - (shown.getTime() - wanted.getTime()),
  ).toISOString();
}
