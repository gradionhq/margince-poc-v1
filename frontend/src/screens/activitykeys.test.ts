import { describe, expect, it } from "vitest";
import { dealWinKeys, entityTimelineKeys, taskWriteKeys } from "./activitykeys";

// The company, contact and project pages draw the timeline's first page from
// their composite 360 read and fetch nothing under the per-record activities
// key until the reader asks for more. A write that invalidated only the
// latter succeeded and showed nothing.

describe("which reads a timeline write has to invalidate", () => {
  it("names the composite read that seeds the timeline, spelled as that page spells it", () => {
    expect(entityTimelineKeys("organization", "o1")).toEqual([
      ["activities", "organization", "o1"],
      ["organization360", "o1"],
    ]);
    expect(entityTimelineKeys("person", "p1")).toEqual([
      ["activities", "person", "p1"],
      ["person360", "p1"],
    ]);
    expect(entityTimelineKeys("project", "j1")).toEqual([
      ["activities", "project", "j1"],
      ["project", "j1", "360"],
    ]);
  });

  it("names only the record's own timeline for a kind with no seeded page", () => {
    for (const kind of ["deal", "lead"] as const) {
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

  it("a won deal reaches the project list, the project's page and the company page that embeds it", () => {
    expect(dealWinKeys({ project_id: "j1", organization_id: "o1" })).toEqual([
      ["projects"],
      ["project", "j1"],
      ["organization360", "o1"],
    ]);
  });

  it("a won deal naming no project still refreshes the project list and nothing it cannot name", () => {
    expect(dealWinKeys({ project_id: null, organization_id: null })).toEqual([
      ["projects"],
    ]);
    expect(dealWinKeys(undefined)).toEqual([["projects"]]);
  });
});
