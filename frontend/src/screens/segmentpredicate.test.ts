// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { beforeEach, describe, expect, it } from "vitest";
import {
  addToGroup,
  encode,
  fieldsNamed,
  type Group,
  isComplete,
  isGroup,
  type Node,
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

/**
 * The tree operations answer a `Node`, so a test reaching for `children` or
 * `join` proves it got a group first. Asserting the type instead would let a
 * regression that returns a leaf — or the `null` `replaceNode` uses for removal —
 * surface as an undefined property rather than a failure naming what it got.
 */
function asGroup(node: Node | null): Group {
  if (node === null || !isGroup(node)) {
    throw new Error(`expected a group, got ${JSON.stringify(node)}`);
  }
  return node;
}

describe("editing the tree", () => {
  it("adds a clause to the group named, not to the root", () => {
    const inner = newGroup("or", [newLeaf("status", "eq", "open")]);
    const root = newGroup("and", [newLeaf("owner_id", "eq", "u1"), inner]);

    const next = addToGroup(root, inner.id, newLeaf("stage_id", "eq", "s1"));

    const nextRoot = asGroup(next);
    expect(asGroup(nextRoot.children[1] ?? null).children).toHaveLength(2);
    // The root gained nothing: an add that lands in the wrong group silently
    // changes what the filter means.
    expect(nextRoot.children).toHaveLength(2);
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

    const next = asGroup(removeNode(root, twin.id));

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

    const next = asGroup(toggleJoin(root, inner.id));

    expect(next.join).toBe("and");
    expect(asGroup(next.children[0] ?? null).join).toBe("and");
  });

  it("retypes a clause in place through the same primitive removal uses", () => {
    const leaf = newLeaf("owner_id", "eq", "u1");
    const root = newGroup("and", [leaf]);

    const next = asGroup(
      replaceNode(root, leaf.id, () =>
        newLeaf("cf_tier", "in", ["gold", "silver"]),
      ),
    );

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

  // Each operand rule below matches a refusal storekit issues per leaf
  // (`filter_value_invalid`). Calling one of these complete would enable Save
  // for a filter the server then rejects, which is the one thing this function
  // exists to prevent.
  it.each([
    ["contains with nothing typed", newLeaf("name", "contains", "")],
    ["in with an empty list", newLeaf("cf_tier", "in", [])],
    ["a comparison with an empty box", newLeaf("city", "eq", "")],
    ["a comparison handed a list", newLeaf("city", "eq", ["Berlin"])],
    [
      "exists handed something other than a boolean",
      newLeaf("tag", "exists", "yes"),
    ],
    ["a leaf naming no field", newLeaf("", "eq", "open")],
  ])("calls %s incomplete", (_name, leaf) => {
    expect(isComplete(newGroup("and", [leaf]))).toBe(false);
  });

  it("calls `exists: false` complete, since absence is what the reader asked for", () => {
    // The one operand a truthiness check would get wrong: `false` is the answer,
    // not a missing one.
    expect(isComplete(newGroup("and", [newLeaf("tag", "exists", false)]))).toBe(
      true,
    );
  });

  it("calls a zero complete, for the same reason", () => {
    expect(isComplete(newGroup("and", [newLeaf("cf_score", "gte", 0)]))).toBe(
      true,
    );
  });
});

// A surface deriving columns from the filter reads this, so what it answers has
// to survive the shapes a real tree takes: nesting, a field named twice across
// two clauses, and a half-written leaf that names nothing yet.
describe("fieldsNamed", () => {
  it("reads every depth, in the order the clauses were written", () => {
    const tree = newGroup("and", [
      newLeaf("city", "eq", "Berlin"),
      newGroup("or", [
        newLeaf("cf_tier", "in", ["gold"]),
        newGroup("and", [newLeaf("status", "eq", "open")]),
      ]),
    ]);

    expect(fieldsNamed(tree)).toEqual(["city", "cf_tier", "status"]);
  });

  it("names a field once however many clauses use it", () => {
    // A range is two clauses over one field, and a column list built from this
    // would otherwise carry that column twice.
    const tree = newGroup("and", [
      newLeaf("cf_score", "gte", 10),
      newLeaf("cf_score", "lte", 90),
    ]);

    expect(fieldsNamed(tree)).toEqual(["cf_score"]);
  });

  it("skips a leaf that names no field yet", () => {
    // The state a freshly added clause is in: it exists, but there is nothing
    // for it to show a column of.
    const tree = newGroup("and", [
      newLeaf("", "eq", ""),
      newLeaf("city", "eq", "Berlin"),
    ]);

    expect(fieldsNamed(tree)).toEqual(["city"]);
  });

  it("answers nothing for an empty tree", () => {
    expect(fieldsNamed(newGroup("and"))).toEqual([]);
  });
});
