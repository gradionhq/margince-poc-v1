import { describe, expect, it } from "vitest";
import {
  dsrKindTone,
  endOfDayInZone,
  isOverdue,
  isTerminal,
  nextStatuses,
} from "./privacy.logic";

// The DAG mirrors the server's dsrTransitions (consent/dsr.go:58-61). The UI
// must never offer a transition the store would 422.
describe("DSR status machine", () => {
  it("offers every forward move from open", () => {
    expect(nextStatuses("open")).toEqual([
      "in_progress",
      "fulfilled",
      "rejected",
    ]);
  });

  it("drops in_progress once work has started", () => {
    expect(nextStatuses("in_progress")).toEqual(["fulfilled", "rejected"]);
  });

  it("never reopens a closed request", () => {
    expect(nextStatuses("fulfilled")).toEqual([]);
    expect(nextStatuses("rejected")).toEqual([]);
    expect(isTerminal("fulfilled")).toBe(true);
    expect(isTerminal("rejected")).toBe(true);
    expect(isTerminal("open")).toBe(false);
  });
});

describe("overdue", () => {
  const due = "2026-08-01T00:00:00Z";
  const before = Date.parse("2026-07-31T23:59:00Z");
  const after = Date.parse("2026-08-01T00:01:00Z");

  it("is overdue once the statutory deadline has passed", () => {
    expect(isOverdue(due, "open", after)).toBe(true);
    expect(isOverdue(due, "in_progress", after)).toBe(true);
  });

  it("is not overdue before the deadline", () => {
    expect(isOverdue(due, "open", before)).toBe(false);
  });

  it("is never overdue once closed — a met deadline stays met", () => {
    expect(isOverdue(due, "fulfilled", after)).toBe(false);
    expect(isOverdue(due, "rejected", after)).toBe(false);
  });
});

describe("kind tone", () => {
  it("reads erasure as danger and rectify as warn", () => {
    expect(dsrKindTone("erasure")).toBe("danger");
    expect(dsrKindTone("rectify")).toBe("warn");
    expect(dsrKindTone("access")).toBeUndefined();
  });
});

describe("endOfDayInZone", () => {
  it("mints UTC midnight when the viewer's zone is UTC", () => {
    expect(endOfDayInZone("2026-07-15", "UTC")).toBe(
      "2026-07-15T23:59:59.999Z",
    );
  });

  // The load-bearing case: a negative-offset zone's end-of-day is the NEXT
  // UTC calendar day. `new Date("2026-07-15").toISOString()` (the old bug)
  // would instead land on 2026-07-15T00:00:00.000Z — a picked day that reads
  // back as Jul 14 for this viewer.
  it("rolls into the next UTC day for a negative-offset zone", () => {
    expect(endOfDayInZone("2026-07-15", "America/New_York")).toBe(
      "2026-07-16T03:59:59.999Z",
    );
  });

  // A positive-offset zone's end-of-day still lands on the SAME UTC
  // calendar day here — pins that the fix isn't a blind "+1 day", it's the
  // zone's actual offset.
  it("stays on the same UTC day for a positive-offset zone", () => {
    expect(endOfDayInZone("2026-07-15", "Europe/Berlin")).toBe(
      "2026-07-15T21:59:59.999Z",
    );
  });
});
