import { describe, expect, it } from "vitest";
import { ENTITY, ENTITY_KINDS, SCREEN_ENTITY } from "./entity";

describe("ENTITY registry", () => {
  it("covers exactly the four record kinds (no activity)", () => {
    expect([...ENTITY_KINDS]).toEqual([
      "person",
      "organization",
      "deal",
      "lead",
    ]);
    expect(Object.keys(ENTITY).sort()).toEqual([
      "deal",
      "lead",
      "organization",
      "person",
    ]);
  });

  it("maps each kind to its 360 route", () => {
    expect(ENTITY.person.route("p-1")).toEqual({
      screen: "contacts",
      id: "p-1",
    });
    expect(ENTITY.organization.route("o-1")).toEqual({
      screen: "companies",
      id: "o-1",
    });
    expect(ENTITY.deal.route("d-1")).toEqual({ screen: "deals", id: "d-1" });
    expect(ENTITY.lead.route("l-1")).toEqual({ screen: "leads", id: "l-1" });
  });

  it("reverses every route into SCREEN_ENTITY, with nothing left over", () => {
    expect(SCREEN_ENTITY).toEqual({
      contacts: "person",
      companies: "organization",
      deals: "deal",
      leads: "lead",
    });
    // Derived, not restated: adding a kind to ENTITY must extend the reverse map
    // on its own, or the breadcrumb quietly falls back to a raw uuid.
    for (const kind of ENTITY_KINDS) {
      expect(SCREEN_ENTITY[ENTITY[kind].route("x").screen]).toBe(kind);
    }
  });

  it("leaves a screen with no record segment unresolved", () => {
    expect(SCREEN_ENTITY.reports).toBeUndefined();
    expect(SCREEN_ENTITY.tasks).toBeUndefined();
  });
});
