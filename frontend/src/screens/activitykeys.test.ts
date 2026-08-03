import { describe, expect, it } from "vitest";
import { entityTimelineKeys, taskWriteKeys } from "./activitykeys";

// The company timeline is rendered from the composite 360 read, not from the
// per-record activities query the person and deal screens use. A write that
// invalidated only the latter succeeded and showed nothing.

describe("which reads a timeline write has to invalidate", () => {
  it("names the 360 as well for an organization", () => {
    expect(entityTimelineKeys("organization", "o1")).toEqual([
      ["activities", "organization", "o1"],
      ["organization360", "o1"],
    ]);
  });

  it("names only the record's own timeline for the other kinds", () => {
    for (const kind of ["person", "deal", "lead"] as const) {
      expect(entityTimelineKeys(kind, "x1")).toEqual([
        ["activities", kind, "x1"],
      ]);
    }
  });

  it("adds the workspace work queue when the write is a task", () => {
    expect(taskWriteKeys("organization", "o1")).toEqual([
      ["activities", "organization", "o1"],
      ["organization360", "o1"],
      ["tasks"],
    ]);
  });
});
