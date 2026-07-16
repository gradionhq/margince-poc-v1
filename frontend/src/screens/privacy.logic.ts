export type DsrStatus = "open" | "in_progress" | "fulfilled" | "rejected";
export type DsrKind = "access" | "rectify" | "erasure";
export type DsrStatusFacet = "all" | DsrStatus;

export const DSR_STATUS_FACETS: readonly DsrStatusFacet[] = [
  "all",
  "open",
  "in_progress",
  "fulfilled",
  "rejected",
];

// Mirrors the server's closed status machine (consent/dsr.go:58-61). A
// closed request never reopens — a new concern is a new request. Offering a
// transition the store rejects would be a 422 the human never needed to see.
const TRANSITIONS: Record<DsrStatus, readonly DsrStatus[]> = {
  open: ["in_progress", "fulfilled", "rejected"],
  in_progress: ["fulfilled", "rejected"],
  fulfilled: [],
  rejected: [],
};

export function nextStatuses(current: DsrStatus): readonly DsrStatus[] {
  return TRANSITIONS[current];
}

export function isTerminal(status: DsrStatus): boolean {
  return TRANSITIONS[status].length === 0;
}

// Pure: the caller supplies now (format/now.ts#useNow) so nothing here races
// a real clock. A closed request is never overdue — the deadline it met (or
// missed) stopped mattering when it closed.
export function isOverdue(
  dueAtIso: string,
  status: DsrStatus,
  nowMs: number,
): boolean {
  if (isTerminal(status)) {
    return false;
  }
  return Date.parse(dueAtIso) < nowMs;
}

// Erasure reads danger, a rectification reads warn, other DSR kinds neutral.
export function dsrKindTone(kind: string): "danger" | "warn" | undefined {
  if (kind === "erasure") {
    return "danger";
  }
  if (kind === "rectify") {
    return "warn";
  }
  return undefined;
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

// A DSR due date is a statutory deadline, not an instant, and the operator
// picks it as a calendar day (<input type="date">) in their OWN timezone —
// the same zone privacy.tsx already renders due_at back in. Minting it as
// `new Date(dateOnly).toISOString()` reads the date-only string as UTC
// midnight, which silently rolls the calendar day back a day for anyone west
// of UTC (mint and render would then disagree on which day the operator
// picked). This mints the actual UTC instant for 23:59:59.999 local time in
// `zone` instead, so what the operator picks is what the row later shows.
// Two passes: the zone's offset can itself change within the correction (a
// DST transition landing exactly at this day's end) — re-deriving it from
// the corrected instant resolves that edge case.
export function endOfDayInZone(dateOnly: string, zone: string): string {
  const [year, month, day] = dateOnly.split("-").map(Number);
  // Whole seconds only while resolving the offset: Intl.DateTimeFormat
  // never reports fractional seconds, so a sub-second component here would
  // desync the wall-clock comparison from the reconstructed one by up to a
  // second. No real zone's offset changes within a second, so the trailing
  // .999 is safe to reattach after the instant is resolved.
  const wallClockMs = Date.UTC(year, month - 1, day, 23, 59, 59);
  let utcMs = wallClockMs;
  for (let pass = 0; pass < 2; pass += 1) {
    utcMs = wallClockMs - zoneOffsetMs(utcMs, zone);
  }
  return new Date(utcMs + 999).toISOString();
}
