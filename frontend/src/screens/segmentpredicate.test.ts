// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { beforeEach, describe, expect, it } from "vitest";
import {
  addToGroup,
  encode,
  isComplete,
  isGroup,
  newGroup,
  newLeaf,
  removeNode,
  replaceNode,
  resetIDsForTest,
  toggleJoin,
} from "./segmentpredicate";

// The tree is the part of this screen with real logic — nesting, immutable
// update, and the encode that must not leak editor state — so it is proven here
// without rendering anything.

beforeEach(() => {
  resetIDsForTest();
});

describe("editing the tree", () => {
  it("adds a clause to the group named, not to the root", () => {
    const inner = newGroup("or", [newLeaf("status", "eq", "open")]);
    const root = newGroup("and", [newLeaf("owner_id", "eq", "u1"), inner]);

    const next = addToGroup(root, inner.id, newLeaf("stage_id", "eq", "s1"));

    const nextInner = (next as ReturnType<typeof newGroup>).children[1];
    expect(isGroup(nextInner) && nextInner.children).toHaveLength(2);
    // The root gained nothing: an add that lands in the wrong group silently
    // changes what the filter means.
    expect((next as ReturnType<typeof newGroup>).children).toHaveLength(2);
  });

  it("leaves the original tree untouched, so an edit can be discarded", () => {
    const root = newGroup("and", [newLeaf("owner_id", "eq", "u1")]);

    addToGroup(root, root.id, newLeaf("status", "eq", "open"));

    expect(root.children).toHaveLength(1);
  });

  it("removes a clause by id, including one structurally identical to its sibling", () => {
    const first = newLeaf("status", "eq", "open");
    const twin = newLeaf("status", "eq", "open");
    const root = newGroup("and", [first, twin]);

    const next = removeNode(root, twin.id) as ReturnType<typeof newGroup>;

    // Identity, not value, is what distinguishes them — which is the whole
    // reason nodes carry an id the wire never sees.
    expect(next.children).toHaveLength(1);
    expect(next.children[0]?.id).toBe(first.id);
  });

  it("refuses to remove the root, since a builder with no tree has nothing to render", () => {
    const root = newGroup("and", [newLeaf("owner_id", "eq", "u1")]);

    expect(removeNode(root, root.id)).toBe(root);
  });

  it("flips only the group named", () => {
    const inner = newGroup("or", [newLeaf("status", "eq", "open")]);
    const root = newGroup("and", [inner]);

    const next = toggleJoin(root, inner.id) as ReturnType<typeof newGroup>;

    expect(next.join).toBe("and");
    const nextInner = next.children[0];
    expect(isGroup(nextInner) && nextInner.join).toBe("and");
  });

  it("retypes a clause in place through the same primitive removal uses", () => {
    const leaf = newLeaf("owner_id", "eq", "u1");
    const root = newGroup("and", [leaf]);

    const next = replaceNode(root, leaf.id, () =>
      newLeaf("cf_tier", "in", ["gold", "silver"]),
    ) as ReturnType<typeof newGroup>;

    const changed = next.children[0];
    expect(isGroup(changed)).toBe(false);
    expect(changed).toMatchObject({
      field: "cf_tier",
      op: "in",
      value: ["gold", "silver"],
    });
  });
});

describe("encoding to the wire", () => {
  it("strips every editor id, at every depth", () => {
    // Deliberately deep: a two-level tree would pass even if encode only
    // stripped the root, and that is the mistake this asserts against.
    const deep = newGroup("and", [
      newLeaf("owner_id", "eq", "u1"),
      newGroup("or", [
        newLeaf("status", "eq", "open"),
        newGroup("and", [
          newLeaf("cf_tier", "eq", "gold"),
          newGroup("or", [newLeaf("tag", "exists", true)]),
        ]),
      ]),
    ]);

    const wire = JSON.stringify(encode(deep));

    expect(wire).not.toContain('"id"');
    expect(wire).not.toContain("n1");
  });

  it("answers the canonical shape the engine stores", () => {
    const root = newGroup("and", [
      newLeaf("owner_id", "eq", "u1"),
      newGroup("or", [newLeaf("cf_tier", "in", ["gold"])]),
    ]);

    expect(encode(root)).toEqual({
      and: [
        { field: "owner_id", op: "eq", value: "u1" },
        { or: [{ field: "cf_tier", op: "in", value: ["gold"] }] },
      ],
    });
  });

  it("keeps a false exists operand, because dropping it would invert the clause", () => {
    // `tag exists false` means "carries no tags at all". Omitting a falsy value
    // to tidy the payload would turn it into a different question.
    const root = newGroup("and", [newLeaf("tag", "exists", false)]);

    expect(encode(root)).toEqual({
      and: [{ field: "tag", op: "exists", value: false }],
    });
  });
});

describe("knowing when a tree is worth sending", () => {
  it("calls an empty group incomplete, since the engine refuses that shape", () => {
    expect(isComplete(newGroup("and", []))).toBe(false);
  });

  it("calls a group with an empty group inside it incomplete", () => {
    expect(
      isComplete(
        newGroup("and", [newLeaf("status", "eq", "open"), newGroup("or", [])]),
      ),
    ).toBe(false);
  });

  it("calls a populated tree complete", () => {
    expect(
      isComplete(
        newGroup("and", [newGroup("or", [newLeaf("status", "eq", "open")])]),
      ),
    ).toBe(true);
  });
});
