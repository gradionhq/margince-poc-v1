import { describe, expect, it } from "vitest";
import type { components } from "../api/schema";
import {
  counterpartyRef,
  edgeOptions,
  endpointBody,
  type RelationshipScope,
} from "./relationships";

type Relationship = components["schemas"]["Relationship"];

// The relationship picker's kind→endpoint mapping mirrors the backend
// rel_*_shape CHECK constraints (migration 0007). These pure-function specs
// pin that mapping so the UI can never offer a (scope, kind) it can't satisfy
// — the mismatch that used to reach the server as a "endpoint shape is
// required" 422. Interactive coverage of the picker lives in people.test.tsx
// / organizations.test.tsx; this file is the invariant itself.

const personScope: RelationshipScope = { person_id: "p-1" };
const orgScope: RelationshipScope = { organization_id: "o-1" };

function baseRel(over: Partial<Relationship>): Relationship {
  return {
    id: "rel-1",
    workspace_id: "w-1",
    kind: "employment",
    is_current_primary: false,
    source: "manual",
    captured_by: "human:u-1",
    version: 1,
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
    ...over,
  };
}

describe("edgeOptions — creatable kinds per scope", () => {
  it("a person anchors employment (→org) and deal_stakeholder (→deal), nothing org↔org", () => {
    expect(edgeOptions(personScope)).toEqual([
      { kind: "employment", entity: "organization", field: "organization_id" },
      { kind: "deal_stakeholder", entity: "deal", field: "deal_id" },
    ]);
  });

  it("an org anchors employment (→person) and the three org↔org kinds (→counterparty), never deal_stakeholder", () => {
    expect(edgeOptions(orgScope)).toEqual([
      { kind: "employment", entity: "person", field: "person_id" },
      {
        kind: "partner_of",
        entity: "organization",
        field: "counterparty_org_id",
      },
      {
        kind: "referred_by",
        entity: "organization",
        field: "counterparty_org_id",
      },
      {
        kind: "co_sell_with",
        entity: "organization",
        field: "counterparty_org_id",
      },
    ]);
    expect(
      edgeOptions(orgScope).some((o) => o.kind === "deal_stakeholder"),
    ).toBe(false);
  });
});

describe("endpointBody — the picked id lands on exactly one field", () => {
  it("maps each field to its own key and no other", () => {
    expect(endpointBody("organization_id", "x")).toEqual({
      organization_id: "x",
    });
    expect(endpointBody("person_id", "x")).toEqual({ person_id: "x" });
    expect(endpointBody("counterparty_org_id", "x")).toEqual({
      counterparty_org_id: "x",
    });
    expect(endpointBody("deal_id", "x")).toEqual({ deal_id: "x" });
  });
});

describe("counterpartyRef — the other end of an existing edge, typed for EntityRef", () => {
  it("a deal edge resolves to the deal regardless of scope", () => {
    const rel = baseRel({ kind: "deal_stakeholder", deal_id: "d-1" });
    expect(counterpartyRef(rel, personScope)).toEqual({
      kind: "deal",
      id: "d-1",
    });
  });

  it("an org↔org edge resolves to the counterparty org", () => {
    const rel = baseRel({ kind: "partner_of", counterparty_org_id: "o-2" });
    expect(counterpartyRef(rel, orgScope)).toEqual({
      kind: "organization",
      id: "o-2",
    });
  });

  it("an employment edge resolves to whichever endpoint the scope is NOT", () => {
    const rel = baseRel({
      kind: "employment",
      person_id: "p-1",
      organization_id: "o-1",
    });
    // From the person's 360 the counterparty is the org; from the org's, the person.
    expect(counterpartyRef(rel, personScope)).toEqual({
      kind: "organization",
      id: "o-1",
    });
    expect(counterpartyRef(rel, orgScope)).toEqual({
      kind: "person",
      id: "p-1",
    });
  });

  it("returns null when no counterparty endpoint is present", () => {
    expect(
      counterpartyRef(baseRel({ person_id: "p-1" }), personScope),
    ).toBeNull();
  });
});
