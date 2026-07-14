import { describe, expect, it } from "vitest";
import { ENTITY, ENTITY_KINDS } from "./entity";

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
});
