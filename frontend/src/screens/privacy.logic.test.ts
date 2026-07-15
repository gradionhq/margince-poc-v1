import { describe, expect, it } from "vitest";
import {
  dsrKindTone,
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
