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

  it("names the display-name field per kind and carries a label key + icon", () => {
    expect(ENTITY.person.displayNameField).toBe("full_name");
    expect(ENTITY.organization.displayNameField).toBe("display_name");
    expect(ENTITY.deal.displayNameField).toBe("name");
    expect(ENTITY.lead.displayNameField).toBe("full_name");
    for (const kind of ENTITY_KINDS) {
      expect(ENTITY[kind].labelKey).toMatch(/^entity\./);
      expect(typeof ENTITY[kind].icon).toBe("object"); // lucide forwardRef component
    }
  });
});
