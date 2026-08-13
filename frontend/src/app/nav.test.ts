// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { describe, expect, it } from "vitest";
import { railTrail } from "./nav";

describe("the composed units group", () => {
  const notes = {
    name: "notes",
    verbs: [
      {
        operationId: "notesList",
        route: "/ext/notes/list",
        method: "GET",
        title: "List notes",
        version: "1.0.0",
        rbacObject: "ext_notes_note",
      },
    ],
  };

  // The vanilla tree composes none, which is why the pinned Records / Work /
  // Intelligence order needed no revising — and why a test that only looked at
  // the default lane would never see this group at all.
  it("is absent when the installation composed no unit", () => {
    const [primary] = railTrail({ screen: "home" }, undefined, []);
    expect(primary.groups.map((group) => group.headingKey)).toEqual([
      undefined,
      "nav.group.records",
      "nav.group.work",
      "nav.group.intelligence",
    ]);
  });

  it("is the LAST group, after the ten the product names", () => {
    const [primary] = railTrail({ screen: "home" }, undefined, [notes]);
    const headings = primary.groups.map((group) => group.headingKey);
    expect(headings.at(-1)).toBe("nav.group.units");
    expect(headings.slice(0, -1)).toEqual([
      undefined,
      "nav.group.records",
      "nav.group.work",
      "nav.group.intelligence",
    ]);
  });

  it("names each row by the unit and routes it under #/ext", () => {
    const [primary] = railTrail({ screen: "home" }, undefined, [notes]);
    const units = primary.groups.at(-1);
    expect(units?.items).toHaveLength(1);
    // The unit's own text, not a translated key: an installation's surface is
    // named by the installation.
    expect(units?.items[0].label).toBe("notes");
    expect(units?.items[0].id).toBe("ext/notes");
  });

  it("gives a unit one row however many verbs it publishes", () => {
    const [primary] = railTrail({ screen: "home" }, undefined, [
      {
        ...notes,
        verbs: [...notes.verbs, { ...notes.verbs[0], operationId: "notesAdd" }],
      },
    ]);
    expect(primary.groups.at(-1)?.items).toHaveLength(1);
  });

  // A unit's screen is a destination, never a badge or a phone-bar slot: what
  // wants a person's attention is the product's judgement, and it cannot make
  // one for a surface it did not write.
  it("claims no badge and no phone-bar slot", () => {
    const [primary] = railTrail({ screen: "home" }, undefined, [notes]);
    expect(primary.badgeIds?.has("ext/notes")).toBe(false);
    expect(primary.barIds?.has("ext/notes")).toBe(false);
  });
});
