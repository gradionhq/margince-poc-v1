// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// The filter tree a human edits, and the one the server stores.
//
// These are the SAME shape on purpose. A node is a group (`and`/`or` over
// children) or a leaf (`field`/`op`/`value`), which is exactly
// storekit.Predicate — so encoding is near-identity rather than a translation.
// A translation layer would be a second spelling of the predicate the engine
// already owns, and the whole point of that engine is that there is one: what a
// filter may say, what it selects, and what an export of it contains cannot
// differ.
//
// The one thing the editor needs that the wire must never carry is stable
// identity. React needs a key, and "remove THIS clause" needs to name a node
// that may be structurally identical to its sibling. So every node carries an
// `id` here, and `encode` strips it — both halves live in this file so a caller
// cannot do one without the other.
//
// No React in this module. The tree is where the real logic is (nesting,
// immutable update, encoding), and it should be provable without rendering
// anything.

/** The operators the engine admits (LVS-PARAM-1). Closed — nothing invented. */
export type FilterOp =
  | "eq"
  | "neq"
  | "gt"
  | "lt"
  | "gte"
  | "lte"
  | "in"
  | "contains"
  | "exists";

/** A leaf's operand: a JSON scalar, or a list for `in`. */
export type LeafValue =
  | string
  | number
  | boolean
  | readonly string[]
  | readonly number[];

export type Leaf = Readonly<{
  id: string;
  field: string;
  op: FilterOp;
  value: LeafValue;
}>;

export type Group = Readonly<{
  id: string;
  /** Which way this group joins its children. One or the other, never both. */
  join: "and" | "or";
  children: readonly Node[];
}>;

export type Node = Leaf | Group;

export function isGroup(node: Node): node is Group {
  return "children" in node;
}

/** The wire shape — storekit.Predicate, with no editor identity in it. */
export type EncodedPredicate = Readonly<{
  and?: readonly EncodedPredicate[];
  or?: readonly EncodedPredicate[];
  field?: string;
  op?: FilterOp;
  value?: LeafValue;
}>;

// Ids are editor-local and never reach the server, so a counter is enough and
// is preferable to a random source: a test can assert an exact tree, and two
// runs of the same edits produce the same ids.
let nextId = 0;
function mintID(): string {
  nextId += 1;
  return `n${nextId}`;
}

/** Resets id minting. For tests that assert exact trees. */
export function resetIDsForTest(): void {
  nextId = 0;
}

export function newLeaf(field: string, op: FilterOp, value: LeafValue): Leaf {
  return { id: mintID(), field, op, value };
}

export function newGroup(
  join: "and" | "or",
  children: readonly Node[] = [],
): Group {
  return { id: mintID(), join, children };
}

/**
 * Replace one node anywhere in the tree, by id, and answer a new tree.
 *
 * The single primitive every edit is expressed through — add, remove, retype a
 * clause, flip a group — because one recursive walk that is right is better than
 * four that are nearly right. `replace` returning null removes the node, which
 * is what makes removal the same operation as update.
 */
export function replaceNode(
  tree: Node,
  id: string,
  replace: (found: Node) => Node | null,
): Node | null {
  if (tree.id === id) {
    return replace(tree);
  }
  if (!isGroup(tree)) {
    return tree;
  }
  const children: Node[] = [];
  for (const child of tree.children) {
    const next = replaceNode(child, id, replace);
    if (next !== null) {
      children.push(next);
    }
  }
  return { ...tree, children };
}

/** Append a node to the group with this id. */
export function addToGroup(tree: Node, groupID: string, node: Node): Node {
  const next = replaceNode(tree, groupID, (found) =>
    isGroup(found) ? { ...found, children: [...found.children, node] } : found,
  );
  // The root is never removed, and replaceNode only answers null when its own
  // callback does — which this one cannot.
  return next ?? tree;
}

/** Flip a group between ALL·AND and ANY·OR. */
export function toggleJoin(tree: Node, groupID: string): Node {
  const next = replaceNode(tree, groupID, (found) =>
    isGroup(found)
      ? { ...found, join: found.join === "and" ? "or" : "and" }
      : found,
  );
  return next ?? tree;
}

/** Remove a node by id. The root cannot be removed; removing it is a no-op. */
export function removeNode(tree: Node, id: string): Node {
  if (tree.id === id) {
    return tree;
  }
  return replaceNode(tree, id, () => null) ?? tree;
}

/**
 * The wire form: the same tree with every editor id stripped, at every depth.
 *
 * `exists` carries its boolean like any other operand — the engine reads
 * `value: false` as "has no value here", so omitting it would change the
 * meaning rather than tidy the payload.
 */
export function encode(tree: Node): EncodedPredicate {
  if (!isGroup(tree)) {
    return { field: tree.field, op: tree.op, value: tree.value };
  }
  const children = tree.children.map(encode);
  return tree.join === "and" ? { and: children } : { or: children };
}

/**
 * Whether this tree is worth sending — the refusals a caller can see coming, so
 * the save button answers them instead of the server.
 *
 * There are two. An empty group compiles to a shape the engine refuses
 * (`filter_shape_invalid`), and a leaf whose operand does not suit its operator
 * is refused per-leaf (`filter_value_invalid`).
 */
export function isComplete(tree: Node): boolean {
  if (isGroup(tree)) {
    return tree.children.length > 0 && tree.children.every(isComplete);
  }
  return tree.field !== "" && hasUsableOperand(tree.op, tree.value);
}

/**
 * Whether this operand can satisfy this operator.
 *
 * This is the field-type-INDEPENDENT half of what storekit checks, which is all
 * this module can know: `exists` takes a boolean, `in` a non-empty list,
 * `contains` a non-empty string. Whether a comparison's operand suits its
 * particular field — a date that parses, a currency in whole minor units — needs
 * the field catalogue the server owns, so those stay the server's 422. The `in`
 * list's length cap stays there too: the limit is storekit's own constant, and a
 * copy of it here would be a second number with nothing holding the two equal.
 *
 * `false` is a complete answer for `exists` — it is how a reader asks for rows
 * with nothing in that field — so this asks about the operand's TYPE, never its
 * truthiness.
 *
 * For the comparisons a blank box reads as unfilled rather than as a value, and
 * that is deliberately stricter than the server, which would accept `name eq ""`
 * on a text field. A reader who means "no value here" has `exists` for it, and
 * counting a blank as an answer would enable Save by tabbing past a field.
 */
function hasUsableOperand(op: FilterOp, value: LeafValue): boolean {
  switch (op) {
    case "exists":
      return typeof value === "boolean";
    case "in":
      return Array.isArray(value) && value.length > 0;
    case "contains":
      return typeof value === "string" && value !== "";
    default:
      return !Array.isArray(value) && value !== "";
  }
}

/**
 * The fields this tree names, in the order a reader wrote them, each once.
 *
 * It lives here because it is a question about the tree, and the tree's shape is
 * this module's business — a caller walking `and`/`or` itself would be the second
 * place that knows how a group nests.
 *
 * Reading order rather than sorted: the clause somebody wrote first is the one
 * they were thinking about, so a surface deriving columns from this shows the
 * field they led with on the left. Deduped because naming a field twice — a range
 * across two clauses — is one column, not two.
 */
export function fieldsNamed(tree: Node): string[] {
  const seen = new Set<string>();
  const walk = (node: Node) => {
    if (!isGroup(node)) {
      if (node.field !== "") {
        seen.add(node.field);
      }
      return;
    }
    for (const child of node.children) {
      walk(child);
    }
  };
  walk(tree);
  return [...seen];
}
