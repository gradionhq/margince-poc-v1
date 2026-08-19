// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { beforeEach, describe, expect, it } from "vitest";
import {
  addToGroup,
  decode,
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

// A saved view's `query` is an open JSON object the server persists verbatim, so
// what `decode` is handed is untrusted: an older build's shape, a fixture, a
// hand-written row. The property that matters is the round trip — a view that
// restores a filter DIFFERENT from the one it was saved from selects rows nobody
// asked for, under a name the reader trusts.
describe("decode", () => {
  it("restores what encode wrote, at every depth", () => {
    const saved = newGroup("or", [
      newLeaf("city", "eq", "Berlin"),
      newGroup("and", [
        newLeaf("cf_tier", "in", ["gold", "silver"]),
        newLeaf("cf_score", "gte", 40),
        newLeaf("tag", "exists", false),
      ]),
    ]);

    const restored = decode(encode(saved));

    // Compared through `encode` rather than by identity: the ids are minted
    // fresh on the way back in, and that is the one thing that MUST differ.
    expect(restored).not.toBeNull();
    expect(encode(restored as Node)).toEqual(encode(saved));
  });

  it("mints fresh ids rather than restoring a tree with none", () => {
    // Every node needs an id for React to key it and for "remove THIS clause" to
    // name it, and the wire form deliberately carries none.
    const restored = decode(
      encode(newGroup("and", [newLeaf("city", "eq", "B")])),
    );

    expect(restored).not.toBeNull();
    const group = restored as Group;
    expect(group.id).not.toBe("");
    expect(group.children[0]?.id).not.toBe("");
    expect(group.children[0]?.id).not.toBe(group.id);
  });

  // Each of these is a stored blob a real row could carry, and every one of them
  // has to answer null: the caller drops the view, which is the only honest
  // outcome — a view that restores a filter it does not name is worse than one
  // that is not offered.
  it.each([
    ["not an object at all", "and"],
    ["null", null],
    ["an array", [{ field: "city", op: "eq", value: "B" }]],
    ["an empty group", { and: [] }],
    ["a group joined two ways at once", { and: [], or: [] }],
    ["a group whose children are not a list", { and: { field: "city" } }],
    ["a leaf naming no field", { field: "", op: "eq", value: "B" }],
    ["a leaf with no field key", { op: "eq", value: "B" }],
    [
      "an operator the engine does not have",
      { field: "c", op: "like", value: "B" },
    ],
    ["an operand that is an object", { field: "c", op: "eq", value: { a: 1 } }],
    ["a mixed list", { field: "c", op: "in", value: ["a", 1] }],
    ["a null operand", { field: "c", op: "eq", value: null }],
  ])("refuses %s", (_name, stored) => {
    expect(decode(stored)).toBeNull();
  });

  it("refuses a whole tree when one clause inside it is unreadable", () => {
    // Dropping the bad clause instead would silently WIDEN the filter: an `and`
    // missing a leaf selects more rows than the one that was saved.
    expect(
      decode({
        and: [
          { field: "city", op: "eq", value: "Berlin" },
          { field: "city", op: "like", value: "Ber" },
        ],
      }),
    ).toBeNull();
  });

  it("restores a shape-valid clause the Save button would still refuse", () => {
    // `contains ""` is readable and incomplete, and those are different
    // questions: the tree decodes, and `isComplete` is what withholds Save.
    const restored = decode({
      and: [{ field: "c", op: "contains", value: "" }],
    });

    expect(restored).not.toBeNull();
    expect(isComplete(restored as Node)).toBe(false);
  });
});
