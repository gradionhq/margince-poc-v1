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
