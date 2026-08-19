import type { components } from "../api/schema";

type Employment = components["schemas"]["Person360Employment"];

// The client half of people.EmploymentIsCurrentSQL. `is_current_primary` records
// WHICH employer represents somebody and is written once; whether that job is
// still theirs is a function of today, so every reader derives it — on this side
// too, or the 360 header names a company the account's own contact count has
// already stopped counting them at.
//
// Its own module because three screens read it: the rail's current marker and
// its "add company" default, and the 360's employer line and former-employer
// list. Two copies of a date rule is one more than the number that can be right.

// today is the reader's OWN day, and it is also what the rail SENDS as `ended_at`
// when somebody ends an employment — one spelling, so the date written and the
// date compared against cannot be a day apart.
// `toISOString` would be UTC, which is a different day from the reader's for
// part of every day and a different day again from the database's.
//
// A client-supplied day can still differ from the database's for somebody far
// from the deployment; nothing here can close that, and the flag is only ever
// used for DISPLAY on this side — the server decides what it stores.
export function today(): string {
  return new Date().toLocaleDateString("en-CA");
}

// stillHeld: the job has not ended yet. A last day that has ARRIVED is a
// departure, matching `> current_date` on the server; only a future one is a
// notice period, and somebody serving one still works there.
export function stillHeld(employment: Employment): boolean {
  if (!employment.ended_at) {
    return true;
  }
  return employment.ended_at.slice(0, 10) > today();
}

// currentEmployer is the one employment that represents this person right now —
// flagged AND not yet ended. Undefined is a real answer: somebody between jobs,
// or whose only employer's last day has passed.
export function currentEmployer(
  employments: ReadonlyArray<Employment> | undefined,
): Employment | undefined {
  return employments?.find(
    (employment) => employment.is_current_primary && stillHeld(employment),
  );
}

// formerEmployers is the complement, so the two together are every row exactly
// once. A job whose last day has passed belongs here even while it still carries
// the flag — that is precisely the row the server stopped counting.
export function formerEmployers(
  employments: ReadonlyArray<Employment> | undefined,
): ReadonlyArray<Employment> {
  const current = currentEmployer(employments);
  return (employments ?? []).filter((employment) => employment !== current);
}
