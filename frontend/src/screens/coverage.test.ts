import { describe, expect, it } from "vitest";
import type { components } from "../api/schema";
import { byReach, missingRoles, reachOf } from "./coverage";

type Contact = NonNullable<
  components["schemas"]["Organization360"]["people"]
>["data"][number];

const FACTORS = { recency: 0, frequency: 0, reciprocity: 0, direction: 0 };

function contact(over: Partial<Contact> = {}): Contact {
  return {
    person_id: "p1",
    full_name: "Christian Hagemeyer",
    strength: { score: 0, bucket: "dormant", factors: FACTORS },
    deal_roles: [],
    consent: {},
    ...over,
  };
}

const withTraffic = (out: number, back: number, over: Partial<Contact> = {}) =>
  contact({
    strength: {
      score: back > 0 ? 40 : 2,
      bucket: back > 0 ? "warm" : "weak",
      factors: FACTORS,
      outbound_90d: out,
      inbound_90d: back,
    },
    ...over,
  });

describe("reachOf", () => {
  it("separates never approached from written to and ignored", () => {
    // The two look identical in a contact list and call for opposite moves:
    // one is a free approach, the other is a decision to follow up again.
    expect(reachOf(withTraffic(0, 0))).toBe("untried");
    expect(reachOf(withTraffic(3, 0))).toBe("silent");
  });

  it("counts one reply as answered, whatever the score", () => {
    expect(reachOf(withTraffic(9, 1))).toBe("answered");
  });
});

describe("byReach", () => {
  it("puts the way in first and the unapproached ahead of the ignored", () => {
    const people = [
      withTraffic(3, 0, { person_id: "silent" }),
      withTraffic(0, 0, { person_id: "untried" }),
      withTraffic(2, 2, { person_id: "answered" }),
    ];
    expect([...people].sort(byReach).map((p) => p.person_id)).toEqual([
      "answered",
      "untried",
      "silent",
    ]);
  });
});

describe("missingRoles", () => {
  const open = new Set(["d-open"]);

  it("names the committee roles nobody holds on an open deal", () => {
    expect(missingRoles([contact()], open, false)).toEqual([
      "champion",
      "economic_buyer",
    ]);
  });

  it("ignores a role held on a deal that is not open", () => {
    // A champion on a deal that closed last year says nothing about the one
    // running now, and counting them hid the gap on the accounts that have it.
    const held = contact({
      deal_roles: [{ deal_id: "d-closed", role: "champion" }],
    });
    expect(missingRoles([held], open, false)).toContain("champion");
  });

  it("says nothing when the role is held on the open deal", () => {
    const held = contact({
      deal_roles: [{ deal_id: "d-open", role: "champion" }],
    });
    expect(missingRoles([held], open, false)).toEqual(["economic_buyer"]);
  });

  it("makes no claim about a gap from a truncated contact list", () => {
    // The twenty-sixth contact is exactly where the missing champion is.
    expect(missingRoles([contact()], open, true)).toEqual([]);
  });

  it("reports no gap on an account with no open deal", () => {
    // A committee gap is a statement about a deal. With nothing running there
    // is no committee to be short of.
    expect(missingRoles([contact()], new Set(), false)).toEqual([]);
  });
});
