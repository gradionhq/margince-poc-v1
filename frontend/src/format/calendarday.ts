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

// The calendar day an instant falls on, in a named IANA zone. `en-CA` is the one
// locale whose short date is already ISO-ordered, so two of these compare as
// strings without a second parse.
export function calendarDay(at: Date, zone: string): string {
  return at.toLocaleDateString("en-CA", { timeZone: zone });
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
