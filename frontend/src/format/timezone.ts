// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// The two zones this product renders dates in, and the rule for which one a
// screen owes its reader. `formatDate`/`formatDateTime`/`formatDateAbbrev` take
// their zone as an argument and only attach it (format.ts, zone-by-purpose) —
// picking the zone is the caller's judgement, and this file is where that
// judgement is written down once.
//
// The two purposes are NOT interchangeable, and each has its own way of being
// wrong:
//
//   RECORD_ZONE — the organization's own clock. Its dates belong to the
//   record, not to whoever is looking at it. Following the reader here
//   MISSTATES the record: a close date, a renewal, an invoice's issue day and
//   a timeline's day headings have to read the same for every colleague, or two
//   people quoting the same page quote different days. It is also the only
//   correct answer for a date-only wire value (OpenAPI `format: date`): there
//   is no instant in `2026-08-21` to localize, and reading it in a zone behind
//   UTC prints the day before.
//
//   viewerZone() — the reader's own clock, for a moment they relate to
//   themselves: when a credential they are lending expires, when a paused job
//   resumes, how stale a mailbox is, when a slot they are about to book starts.
//   Pinning these to a fixed zone shows a reader outside it a different
//   calendar day than the one the thing actually happens on, which is how the
//   consent screen came to promise an expiry on a day that had not arrived.
//
// When a site is genuinely both — a personal deadline on a record page — the
// page wins: a record surface is ONE clock, so a due date and the activity
// beside it can never be read in two different zones.
//
// "Genuinely both" means both readings are defensible, and that is a claim about
// the STORED value, not about the screen. An activity's `due_at` is minted by
// `dueInstant` as the end of the picked day in the BROWSER's zone, so the wire
// value already carries the picker's clock; RECORD_ZONE does not read the
// organization's day out of it, it reads a day the picker never chose, off by
// one for every reader outside that zone. A value with no record reading has
// nothing for the page to win against, so `due_at` takes viewerZone() wherever
// it is shown — the tasks queue, the record's next steps, the task detail —
// while the activity's `occurred_at` beside it stays on RECORD_ZONE, because
// when something happened IS a fact about the record.

// The organization's zone. A constant, and deliberately not read from the
// installation's configured timezone yet: every screen that renders a record
// date has to agree, and a per-request value that arrives late would render the
// first paint in one zone and the second in another.
export const RECORD_ZONE = "Europe/Berlin";

/**
 * The zone the reader's own browser is in, or `UTC` when it will not say.
 *
 * A function, not a constant, on purpose. A module-level constant is evaluated
 * once at import time, which is before the app has rendered anything — so it
 * would freeze the answer for the whole session, outlive a reader who changed
 * their machine's zone or crossed one, and be captured before a test that
 * pretends to be elsewhere had installed its own answer. Constructing an
 * `Intl.DateTimeFormat` per call is cheap next to the render it feeds.
 *
 * A runtime with no zone to report leaves `timeZone` empty, and an empty string
 * is not a zone Intl accepts — it throws. `UTC` is the honest fallback: it
 * names a real zone, and it is the one every wire instant is already stored in.
 */
export function viewerZone(): string {
  return Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
}

// The offset (ms) `zone` sits at relative to UTC at the instant `utcMs`
// names — positive east of UTC, negative west. Derived by re-reading the
// instant's wall clock through the zone and comparing it back against UTC,
// the standard technique for converting a zoned wall clock to an instant
// without a timezone-database library.
function zoneOffsetMs(utcMs: number, zone: string): number {
  const parts = Object.fromEntries(
    new Intl.DateTimeFormat("en-US", {
      timeZone: zone,
      hourCycle: "h23",
      year: "numeric",
      month: "2-digit",
      day: "2-digit",
      hour: "2-digit",
      minute: "2-digit",
      second: "2-digit",
    })
      .formatToParts(new Date(utcMs))
      .map((part) => [part.type, part.value]),
  );
  const asIfUtc = Date.UTC(
    Number(parts.year),
    Number(parts.month) - 1,
    Number(parts.day),
    Number(parts.hour),
    Number(parts.minute),
    Number(parts.second),
  );
  return asIfUtc - utcMs;
}

// instantInZone resolves the UTC instant at which `zone`'s wall clock reads
// the given date and time. Two passes: the zone's offset can itself change
// within the correction (a DST transition landing exactly at that wall-clock
// moment) — re-deriving it from the corrected instant resolves that edge.
// Whole seconds only: Intl.DateTimeFormat never reports fractional seconds,
// so a sub-second component would desync the wall-clock comparison from the
// reconstructed one by up to a second.
function instantInZone(
  dateOnly: string,
  zone: string,
  hour: number,
  minute: number,
  second: number,
): number {
  const [year, month, day] = dateOnly.split("-").map(Number);
  const wallClockMs = Date.UTC(year, month - 1, day, hour, minute, second);
  let utcMs = wallClockMs;
  for (let pass = 0; pass < 2; pass += 1) {
    utcMs = wallClockMs - zoneOffsetMs(utcMs, zone);
  }
  return utcMs;
}

/**
 * A calendar day, picked as a date-only string, read as a day in `zone`.
 * Minting it as `new Date(dateOnly).toISOString()` reads the string as UTC
 * midnight, which silently rolls the day back for anyone west of UTC and
 * forward for evening messages east of it: the picker and the rendered row
 * would then disagree about which day was meant.
 *
 * `startOfDayInZone` is the day's first instant; `daysLater` shifts by whole
 * calendar days, which is how an EXCLUSIVE range end is spelled from an
 * inclusive "to" day — the next day's start — without an arithmetic step
 * across a DST change.
 */
export function startOfDayInZone(
  dateOnly: string,
  zone: string,
  daysLater = 0,
): string {
  const [year, month, day] = dateOnly.split("-").map(Number);
  const shifted = new Date(Date.UTC(year, month - 1, day + daysLater))
    .toISOString()
    .slice(0, 10);
  return new Date(instantInZone(shifted, zone, 0, 0, 0)).toISOString();
}

/** The day's last instant, 23:59:59.999 on `zone`'s wall clock. */
export function endOfDayInZone(dateOnly: string, zone: string): string {
  return new Date(
    instantInZone(dateOnly, zone, 23, 59, 59) + 999,
  ).toISOString();
}
