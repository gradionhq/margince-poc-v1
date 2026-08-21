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
