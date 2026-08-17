// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { describe, expect, it } from "vitest";
import { railTrail } from "./nav";

describe("the rail and a composed unit", () => {
  // The product's own destinations, and no group an installation can add to.
  // A unit had a group here once; the composed set is no longer an input to
  // this level at all, which is what the missing argument to railTrail says.
  it("carries only the groups the product names", () => {
    const [primary] = railTrail({ screen: "home" });
    expect(primary.groups.map((group) => group.headingKey)).toEqual([
      undefined,
      "nav.group.records",
      "nav.group.work",
      "nav.group.intelligence",
    ]);
  });

  // The regression the rail row used to prevent, now prevented differently: a
  // unit screen still has no row of its own, so without an answer here the rail
  // would mark nothing current and the page would read as if it sat outside the
  // app. Settings is where the unit is offered and where the reader came from.
  //
  // This is the DATA half only. `settings` is a string, and whether any element
  // on screen resolves it is a different question that reads identically from
  // here — the Settings door is what answers it, and shell.test.tsx asserts
  // that at render. Neither half is sufficient alone; this file has shipped the
  // half-truth before.
  it("marks Settings current on a unit's route", () => {
    const [primary] = railTrail({ screen: "ext", id: "notes" });
    expect(primary.activeId).toBe("settings");
  });

  // Including the malformed one: `#/ext` with no unit renders the not-found
  // surface, and a reader who typed it is still somewhere in Settings' world
  // rather than nowhere.
  it("marks Settings current on a unit route naming no unit", () => {
    const [primary] = railTrail({ screen: "ext" });
    expect(primary.activeId).toBe("settings");
  });

  it("leaves the product's rows marking themselves", () => {
    const [primary] = railTrail({ screen: "deals" });
    expect(primary.activeId).toBe("deals");
  });
});
